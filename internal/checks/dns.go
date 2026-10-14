package checks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// DefaultDNSServer is used when no resolver is configured and the system
// resolver configuration (/etc/resolv.conf) cannot be read.
const DefaultDNSServer = "1.1.1.1:53"

// DNSConfig configures a DNS check.
type DNSConfig struct {
	Host        string        // name to resolve; an IP address triggers a PTR lookup
	Server      string        // resolver "ip:port"; empty means the system resolver
	Types       []string      // record types, e.g. ["A", "AAAA"]
	Expect      []string      // values that must appear in the answers
	LatencyWarn time.Duration // warn when total resolution time exceeds this
	Timeout     time.Duration // per-query network timeout
}

// DNSChecker resolves records by talking DNS directly to a resolver.
//
// We deliberately use github.com/miekg/dns instead of net.Resolver: it lets
// us pick the exact server, query arbitrary record types, see the response
// code (NXDOMAIN vs SERVFAIL), TTLs and the round-trip time of every query.
type DNSChecker struct {
	cfg    DNSConfig
	qtypes []uint16
	server string
	client *dns.Client
}

// NewDNSChecker validates cfg and builds a checker.
func NewDNSChecker(cfg DNSConfig) (*DNSChecker, error) {
	if cfg.Host == "" {
		return nil, errors.New("dns: host is required")
	}
	if len(cfg.Types) == 0 {
		cfg.Types = []string{"A", "AAAA"}
	}
	c := &DNSChecker{cfg: cfg}

	if ip := net.ParseIP(cfg.Host); ip != nil {
		// Resolving an IP literal makes no sense; do a reverse lookup instead.
		c.qtypes = []uint16{dns.TypePTR}
	} else {
		for _, t := range cfg.Types {
			qt, ok := dns.StringToType[strings.ToUpper(t)]
			if !ok {
				return nil, fmt.Errorf("dns: unknown record type %q", t)
			}
			c.qtypes = append(c.qtypes, qt)
		}
	}

	c.server = cfg.Server
	if c.server == "" {
		c.server = SystemDNSServer()
	} else if _, _, err := net.SplitHostPort(c.server); err != nil {
		c.server = net.JoinHostPort(c.server, "53")
	}

	// Per-attempt timeout; the overall bound is the check's context deadline.
	timeout := dnsAttemptTimeout
	if cfg.Timeout > 0 {
		timeout = min(timeout, cfg.Timeout)
	}
	c.client = &dns.Client{Net: "udp", Timeout: timeout}
	return c, nil
}

// Kind implements Checker.
func (c *DNSChecker) Kind() Kind { return KindDNS }

// Check implements Checker.
func (c *DNSChecker) Check(ctx context.Context) Result {
	res := Result{Kind: KindDNS, Status: StatusOK}
	info := &DNSInfo{Server: c.server}
	res.DNS = info

	name := dns.Fqdn(c.cfg.Host)
	if ip := net.ParseIP(c.cfg.Host); ip != nil {
		rev, err := dns.ReverseAddr(ip.String())
		if err != nil {
			res.fail(err)
			return res
		}
		name = rev
	}

	start := time.Now()
	for _, qt := range c.qtypes {
		typeName := dns.TypeToString[qt]
		msg := new(dns.Msg)
		msg.SetQuestion(name, qt)
		msg.RecursionDesired = true

		in, rtt, err := c.exchange(ctx, msg)
		res.Phases = append(res.Phases, Phase{Name: typeName, Duration: rtt})
		if err != nil {
			res.Latency = time.Since(start)
			res.fail(fmt.Errorf("%s query via %s: %w", typeName, c.server, err))
			return res
		}
		if in.Rcode != dns.RcodeSuccess {
			res.Latency = time.Since(start)
			res.fail(fmt.Errorf("%s query: %s", typeName, dns.RcodeToString[in.Rcode]))
			return res
		}
		for _, rr := range in.Answer {
			info.Records = append(info.Records, DNSRecord{
				Type:  dns.TypeToString[rr.Header().Rrtype],
				Value: rrValue(rr),
				TTL:   rr.Header().Ttl,
			})
		}
	}
	res.Latency = time.Since(start)

	// NOERROR with zero answers ("NODATA") is normal for a single type, e.g.
	// a host without IPv6, but zero answers across every type means the name
	// is effectively unresolvable.
	if len(info.Records) == 0 {
		res.fail(fmt.Errorf("no records returned for %s", strings.TrimSuffix(name, ".")))
		return res
	}

	values := make([]string, 0, len(info.Records))
	for _, r := range info.Records {
		values = append(values, r.Value)
	}
	for _, want := range c.cfg.Expect {
		if !slices.Contains(values, strings.TrimSuffix(want, ".")) {
			res.fail(fmt.Errorf("expected record %q not found in answers", want))
			return res
		}
	}

	res.Summary = dnsSummary(info.Records, res.Latency)
	res.AddDetail("resolver", c.server)
	for _, r := range info.Records {
		res.AddDetail(r.Type, fmt.Sprintf("%s (ttl %ds)", r.Value, r.TTL))
	}
	if c.cfg.LatencyWarn > 0 && res.Latency > c.cfg.LatencyWarn {
		res.warn(fmt.Sprintf("resolution took %s (threshold %s)",
			FormatDuration(res.Latency), FormatDuration(c.cfg.LatencyWarn)))
	}
	return res
}

// exchange sends msg over UDP and handles the two classic UDP pitfalls:
//
//  1. Loss: UDP has no retransmission, so a single dropped datagram would
//     otherwise burn the entire check timeout. Like real stub resolvers
//     (glibc retries every 5s by default; we are more aggressive), we give
//     each attempt a short timeout and retry while the deadline allows.
//  2. Truncation: answers larger than the UDP payload limit come back with
//     the TC bit set; RFC 7766 says to retry over TCP.
func (c *DNSChecker) exchange(ctx context.Context, msg *dns.Msg) (*dns.Msg, time.Duration, error) {
	var lastErr error
	for attempt := 1; attempt <= dnsAttempts; attempt++ {
		in, rtt, err := c.client.ExchangeContext(ctx, msg, c.server)
		if err == nil {
			if in.Truncated {
				tcp := &dns.Client{Net: "tcp", Timeout: c.client.Timeout}
				return tcp.ExchangeContext(ctx, msg, c.server)
			}
			return in, rtt, nil
		}
		lastErr = err
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() || ctx.Err() != nil {
			break // only timeouts are worth retrying, and only before the deadline
		}
	}
	var ne net.Error
	if errors.As(lastErr, &ne) && ne.Timeout() {
		return nil, 0, fmt.Errorf("no response (timeout after %d attempts)", dnsAttempts)
	}
	return nil, 0, lastErr
}

// dnsAttempts and dnsAttemptTimeout bound UDP retries: 3 tries × 2s fits
// comfortably inside the default 10s check timeout.
const (
	dnsAttempts       = 3
	dnsAttemptTimeout = 2 * time.Second
)

// rrValue extracts the human meaningful part of a resource record.
func rrValue(rr dns.RR) string {
	switch v := rr.(type) {
	case *dns.A:
		return v.A.String()
	case *dns.AAAA:
		return v.AAAA.String()
	case *dns.CNAME:
		return strings.TrimSuffix(v.Target, ".")
	case *dns.MX:
		return fmt.Sprintf("%d %s", v.Preference, strings.TrimSuffix(v.Mx, "."))
	case *dns.NS:
		return strings.TrimSuffix(v.Ns, ".")
	case *dns.PTR:
		return strings.TrimSuffix(v.Ptr, ".")
	case *dns.TXT:
		return strings.Join(v.Txt, "")
	case *dns.CAA:
		return fmt.Sprintf("%d %s %q", v.Flag, v.Tag, v.Value)
	default:
		// Strip the header ("name ttl class type") from the presentation form.
		hdr := rr.Header().String()
		return strings.TrimSpace(strings.TrimPrefix(rr.String(), hdr))
	}
}

func dnsSummary(records []DNSRecord, d time.Duration) string {
	counts := map[string]int{}
	var order []string
	for _, r := range records {
		if counts[r.Type] == 0 {
			order = append(order, r.Type)
		}
		counts[r.Type]++
	}
	parts := make([]string, 0, len(order))
	for _, t := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[t], t))
	}
	first := records[0].Value
	for _, r := range records {
		if r.Type == "A" || r.Type == "AAAA" {
			first = r.Value
			break
		}
	}
	return fmt.Sprintf("%s → %s", strings.Join(parts, ", "), Shorten(first, 40))
}

var (
	systemServerOnce sync.Once
	systemServer     string
)

// SystemDNSServer returns the first nameserver from /etc/resolv.conf, or
// DefaultDNSServer when that file is missing (e.g. on Windows).
func SystemDNSServer() string {
	systemServerOnce.Do(func() {
		systemServer = DefaultDNSServer
		conf, err := dns.ClientConfigFromFile("/etc/resolv.conf")
		if err == nil && len(conf.Servers) > 0 {
			systemServer = net.JoinHostPort(conf.Servers[0], conf.Port)
		}
	})
	return systemServer
}
