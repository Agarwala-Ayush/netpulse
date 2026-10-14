package config

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// QuickOptions tune a configuration built from command-line arguments.
type QuickOptions struct {
	Checks    []string // which checks to enable; empty means all four
	DNSServer string
	Interval  Duration
	Timeout   Duration
}

// FromHosts builds a configuration from bare hosts or URLs given on the
// command line, so that `netpulse check github.com https://example.org/health`
// works with no config file at all.
//
//   - "github.com"              → DNS, HTTPS GET /, TLS :443, TCP :443
//   - "https://host:8443/path"  → HTTP uses the exact URL; TLS/TCP use :8443
//   - "http://host"             → plain HTTP; the TLS check is skipped
//   - "host:22"                 → TCP to port 22; HTTP/TLS skipped (not a web port)
func FromHosts(args []string, opts QuickOptions) (*Config, error) {
	enabled := func(kind string) bool {
		return len(opts.Checks) == 0 || slices.Contains(opts.Checks, kind)
	}
	cfg := &Config{Interval: opts.Interval, Timeout: opts.Timeout}
	names := map[string]int{}

	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		var (
			host    string
			port    = DefaultPort
			rawURL  string
			isHTTPS = true
			webPort = true
		)
		if strings.Contains(arg, "://") {
			u, err := url.Parse(arg)
			if err != nil || u.Hostname() == "" {
				return nil, fmt.Errorf("invalid URL %q", arg)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				return nil, fmt.Errorf("unsupported scheme in %q (use http or https)", arg)
			}
			host, rawURL, isHTTPS = u.Hostname(), arg, u.Scheme == "https"
			if !isHTTPS {
				port = 80
			}
			if p := u.Port(); p != "" {
				port, _ = strconv.Atoi(p)
			}
		} else if h, p, err := net.SplitHostPort(arg); err == nil {
			host = h
			port, err = strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("invalid port in %q", arg)
			}
			webPort = port == 443 || port == 8443
		} else {
			host = arg
		}

		t := Target{Name: host, Host: host, Group: "adhoc"}
		if n := names[host]; n > 0 {
			t.Name = fmt.Sprintf("%s-%d", host, n+1)
		}
		names[host]++

		if enabled("dns") {
			t.DNS = &DNSCheck{Server: opts.DNSServer}
		}
		if enabled("http") && webPort {
			if rawURL == "" {
				rawURL = fmt.Sprintf("https://%s/", net.JoinHostPort(host, strconv.Itoa(port)))
				if port == DefaultPort {
					rawURL = "https://" + host + "/"
				}
			}
			t.HTTP = &HTTPCheck{URL: rawURL}
		}
		if enabled("tls") && isHTTPS && webPort {
			t.TLS = &TLSCheck{Port: port}
		}
		if enabled("tcp") {
			t.TCP = &TCPCheck{Port: port}
		}
		cfg.Targets = append(cfg.Targets, t)
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
