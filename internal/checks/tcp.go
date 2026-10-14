package checks

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"time"
)

// TCPConfig configures a TCP connect latency check.
type TCPConfig struct {
	Address       string        // host:port
	Count         int           // number of connection attempts
	ProbeInterval time.Duration // pause between attempts
	LatencyWarn   time.Duration // warn when average RTT exceeds this
}

// TCPChecker measures TCP handshake latency, a portable alternative to ICMP
// ping.
//
// Why not ICMP? Sending ICMP echo needs a raw socket (root or CAP_NET_RAW on
// Linux), or unprivileged ping sockets enabled through the
// net.ipv4.ping_group_range sysctl. Many networks also drop ICMP entirely.
// A TCP SYN → SYN/ACK round trip needs no privileges, travels the same path
// as real traffic to the service port, and approximates network RTT well.
type TCPChecker struct {
	cfg      TCPConfig
	resolver *net.Resolver
}

// NewTCPChecker validates cfg and builds a checker.
func NewTCPChecker(cfg TCPConfig) (*TCPChecker, error) {
	if _, _, err := net.SplitHostPort(cfg.Address); err != nil {
		return nil, fmt.Errorf("tcp: address must be host:port: %w", err)
	}
	if cfg.Count <= 0 {
		cfg.Count = 3
	}
	if cfg.ProbeInterval < 0 {
		cfg.ProbeInterval = 0
	}
	return &TCPChecker{cfg: cfg, resolver: net.DefaultResolver}, nil
}

// Kind implements Checker.
func (c *TCPChecker) Kind() Kind { return KindTCP }

// Check implements Checker.
func (c *TCPChecker) Check(ctx context.Context) Result {
	res := Result{Kind: KindTCP, Status: StatusOK}
	host, port, _ := net.SplitHostPort(c.cfg.Address)

	// Resolve once up front so that every sample measures only the TCP
	// handshake, not DNS. (DNS has its own dedicated check.)
	ip := host
	if net.ParseIP(host) == nil {
		resolveStart := time.Now()
		addrs, err := c.resolver.LookupIPAddr(ctx, host)
		res.Phases = append(res.Phases, Phase{Name: "resolve", Duration: time.Since(resolveStart)})
		if err != nil {
			res.fail(fmt.Errorf("resolve %s: %w", host, err))
			return res
		}
		ip = pickAddr(addrs).String()
	}
	target := net.JoinHostPort(ip, port)

	var (
		samples []time.Duration
		sent    int
		lastErr error
		dialer  net.Dialer
	)
	for i := range c.cfg.Count {
		if i > 0 && c.cfg.ProbeInterval > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(c.cfg.ProbeInterval):
			}
		}
		if ctx.Err() != nil {
			break // deadline reached: report what we have
		}
		sent++
		t0 := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", target)
		rtt := time.Since(t0)
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.Close()
		samples = append(samples, rtt)
	}

	info := &TCPInfo{Address: target, Sent: sent, Received: len(samples)}
	res.TCP = info
	if sent > 0 {
		info.Loss = float64(sent-len(samples)) / float64(sent)
	}

	if len(samples) == 0 {
		if lastErr == nil {
			lastErr = errors.New("no attempts completed before the deadline")
		}
		res.fail(fmt.Errorf("connect %s: %w", target, unwrapOpError(lastErr)))
		res.Summary = fmt.Sprintf("%d/%d connects failed: %s", sent, sent, Shorten(unwrapOpError(lastErr).Error(), 60))
		return res
	}

	info.Min, info.Avg, info.Max, info.Jitter = rttStats(samples)
	res.Latency = info.Avg
	res.Summary = fmt.Sprintf("avg %s · min %s · max %s · loss %.0f%%",
		FormatDuration(info.Avg), FormatDuration(info.Min), FormatDuration(info.Max), info.Loss*100)
	res.AddDetail("address", target)
	res.AddDetail("samples", fmt.Sprintf("%d/%d succeeded", info.Received, info.Sent))
	res.AddDetail("rtt min/avg/max/jitter", fmt.Sprintf("%s / %s / %s / %s",
		FormatDuration(info.Min), FormatDuration(info.Avg), FormatDuration(info.Max), FormatDuration(info.Jitter)))

	// The initial TCP retransmission timeout is 1s (RFC 6298). A sample of
	// ~1s+ alongside fast ones almost always means the first SYN or SYN/ACK
	// was lost and retransmitted: packet loss hidden inside a "successful"
	// connect. Surfacing it explains otherwise puzzling latency spikes.
	if info.Max >= time.Second && info.Min < info.Max/4 {
		res.AddDetail("note", "a ~1s+ sample alongside fast ones suggests a lost SYN was retransmitted (packet loss)")
	}
	if info.Loss > 0 {
		res.warn(fmt.Sprintf("%.0f%% of connection attempts failed (last error: %v)", info.Loss*100, unwrapOpError(lastErr)))
	}
	if c.cfg.LatencyWarn > 0 && info.Avg > c.cfg.LatencyWarn {
		res.warn(fmt.Sprintf("average RTT %s exceeds %s", FormatDuration(info.Avg), FormatDuration(c.cfg.LatencyWarn)))
	}
	return res
}

// rttStats returns min, mean, max and jitter. Jitter is the standard
// deviation of the samples — the same "mdev" figure that ping(8) prints.
func rttStats(samples []time.Duration) (minD, avg, maxD, jitter time.Duration) {
	minD, maxD = samples[0], samples[0]
	var sum float64
	for _, s := range samples {
		minD = min(minD, s)
		maxD = max(maxD, s)
		sum += float64(s)
	}
	mean := sum / float64(len(samples))
	var variance float64
	for _, s := range samples {
		d := float64(s) - mean
		variance += d * d
	}
	variance /= float64(len(samples))
	return minD, time.Duration(mean), maxD, time.Duration(math.Sqrt(variance))
}

// pickAddr prefers IPv4: many hosts publish AAAA records while the machine
// running the check has no working IPv6 route, which would show up as 100%
// loss and a false alarm.
func pickAddr(addrs []net.IPAddr) net.IP {
	for _, a := range addrs {
		if a.IP.To4() != nil {
			return a.IP
		}
	}
	return addrs[0].IP
}
