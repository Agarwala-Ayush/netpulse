// Package config loads, defaults and validates netpulse configuration.
//
// Configuration is YAML. Each target is a host plus the set of checks to run
// against it; a check is enabled simply by being present:
//
//	targets:
//	  - name: github
//	    host: github.com
//	    dns: {}                       # all defaults
//	    http: { url: https://github.com, expect_status: [200] }
//	    tls: { warn_days: 21 }
//	    tcp: { port: 22 }
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/miekg/dns"
	"gopkg.in/yaml.v3"
)

// Built-in defaults.
const (
	DefaultInterval    = 30 * time.Second
	DefaultTimeout     = 10 * time.Second
	DefaultConcurrency = 16
	DefaultListen      = ":9469"
	DefaultHistory     = 120
	DefaultTitle       = "NetPulse"

	DefaultDNSLatencyWarn  = 300 * time.Millisecond
	DefaultHTTPLatencyWarn = 1500 * time.Millisecond
	DefaultTLSLatencyWarn  = 1000 * time.Millisecond
	DefaultTCPLatencyWarn  = 250 * time.Millisecond
	DefaultTLSWarnDays     = 30
	DefaultTLSCritDays     = 7
	DefaultTCPCount        = 3
	DefaultTCPProbeGap     = 100 * time.Millisecond
	DefaultPort            = 443
)

// Config is the root of the configuration file.
type Config struct {
	Interval    Duration     `yaml:"interval"`
	Timeout     Duration     `yaml:"timeout"`
	Concurrency int          `yaml:"concurrency"`
	Server      ServerConfig `yaml:"server"`
	Targets     []Target     `yaml:"targets"`
}

// ServerConfig controls the HTTP server used by `netpulse serve`.
type ServerConfig struct {
	Listen  string `yaml:"listen"`
	Title   string `yaml:"title"`
	History int    `yaml:"history"` // results kept per check for the dashboard
}

// Target is one host and the checks to run against it.
type Target struct {
	Name     string   `yaml:"name"`
	Host     string   `yaml:"host"`
	Group    string   `yaml:"group"`
	Interval Duration `yaml:"interval"` // overrides the global interval
	Timeout  Duration `yaml:"timeout"`  // overrides the global timeout

	DNS  *DNSCheck  `yaml:"dns"`
	HTTP *HTTPCheck `yaml:"http"`
	TLS  *TLSCheck  `yaml:"tls"`
	TCP  *TCPCheck  `yaml:"tcp"`
}

// DNSCheck configures DNS resolution.
type DNSCheck struct {
	Server      string   `yaml:"server"`
	Types       []string `yaml:"types"`
	Expect      []string `yaml:"expect"`
	LatencyWarn Duration `yaml:"latency_warn"`
}

// HTTPCheck configures an HTTP(S) request.
type HTTPCheck struct {
	URL                string            `yaml:"url"`
	Method             string            `yaml:"method"`
	Headers            map[string]string `yaml:"headers"`
	Body               string            `yaml:"body"`
	ExpectStatus       []int             `yaml:"expect_status"`
	BodyContains       string            `yaml:"body_contains"`
	FollowRedirects    *bool             `yaml:"follow_redirects"` // pointer: distinguish unset from false
	CAFile             string            `yaml:"ca_file"`
	InsecureSkipVerify bool              `yaml:"insecure_skip_verify"`
	LatencyWarn        Duration          `yaml:"latency_warn"`
}

// TLSCheck configures certificate inspection.
type TLSCheck struct {
	Port        int      `yaml:"port"`
	ServerName  string   `yaml:"server_name"`
	CAFile      string   `yaml:"ca_file"`
	WarnDays    int      `yaml:"warn_days"`
	CritDays    int      `yaml:"crit_days"`
	LatencyWarn Duration `yaml:"latency_warn"`
}

// TCPCheck configures connect latency probing.
type TCPCheck struct {
	Port          int      `yaml:"port"`
	Count         int      `yaml:"count"`
	ProbeInterval Duration `yaml:"probe_interval"`
	LatencyWarn   Duration `yaml:"latency_warn"`
}

// Load reads, parses, defaults and validates a configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path comes from the operator
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse parses YAML bytes, then applies defaults and validates.
//
// ${VAR} references are expanded from the environment first, so secrets
// such as auth headers never need to live in the file itself.
func Parse(raw []byte) (*Config, error) {
	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	// Reject unknown keys: a typo like "expect_staus" must be an error, not
	// a silently ignored assertion that leaves a check weaker than intended.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing yaml: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ApplyDefaults fills every unset field with its built-in default so the
// rest of the program never has to deal with zero values.
func (c *Config) ApplyDefaults() {
	if c.Interval == 0 {
		c.Interval = Duration(DefaultInterval)
	}
	if c.Timeout == 0 {
		c.Timeout = Duration(min(DefaultTimeout, c.Interval.D()))
	}
	if c.Concurrency == 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.Server.Listen == "" {
		c.Server.Listen = DefaultListen
	}
	if c.Server.Title == "" {
		c.Server.Title = DefaultTitle
	}
	if c.Server.History == 0 {
		c.Server.History = DefaultHistory
	}

	for i := range c.Targets {
		t := &c.Targets[i]
		t.Host = strings.TrimSpace(t.Host)
		if t.Name == "" {
			t.Name = t.Host
		}
		if t.Interval == 0 {
			t.Interval = c.Interval
		}
		if t.Timeout == 0 {
			t.Timeout = Duration(min(c.Timeout.D(), t.Interval.D()))
		}
		if d := t.DNS; d != nil {
			if len(d.Types) == 0 {
				d.Types = []string{"A", "AAAA"}
			}
			if d.LatencyWarn == 0 {
				d.LatencyWarn = Duration(DefaultDNSLatencyWarn)
			}
		}
		if h := t.HTTP; h != nil {
			if h.URL == "" {
				h.URL = "https://" + t.Host + "/"
			}
			if h.Method == "" {
				h.Method = http.MethodGet
			}
			if h.FollowRedirects == nil {
				follow := true
				h.FollowRedirects = &follow
			}
			if h.LatencyWarn == 0 {
				h.LatencyWarn = Duration(DefaultHTTPLatencyWarn)
			}
		}
		if s := t.TLS; s != nil {
			if s.Port == 0 {
				s.Port = DefaultPort
			}
			if s.WarnDays == 0 {
				s.WarnDays = DefaultTLSWarnDays
			}
			if s.CritDays == 0 {
				s.CritDays = DefaultTLSCritDays
			}
			if s.LatencyWarn == 0 {
				s.LatencyWarn = Duration(DefaultTLSLatencyWarn)
			}
		}
		if p := t.TCP; p != nil {
			if p.Port == 0 {
				p.Port = DefaultPort
			}
			if p.Count == 0 {
				p.Count = DefaultTCPCount
			}
			if p.ProbeInterval == 0 {
				p.ProbeInterval = Duration(DefaultTCPProbeGap)
			}
			if p.LatencyWarn == 0 {
				p.LatencyWarn = Duration(DefaultTCPLatencyWarn)
			}
		}
	}
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Validate reports every problem in the configuration at once (joined with
// errors.Join) rather than stopping at the first, so a user can fix a broken
// file in one pass.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Interval.D() < time.Second {
		add("interval: must be at least 1s, got %s", c.Interval.D())
	}
	if c.Timeout.D() <= 0 {
		add("timeout: must be positive")
	}
	if c.Concurrency < 1 || c.Concurrency > 4096 {
		add("concurrency: must be between 1 and 4096, got %d", c.Concurrency)
	}
	if c.Server.History < 1 || c.Server.History > 10000 {
		add("server.history: must be between 1 and 10000, got %d", c.Server.History)
	}
	if len(c.Targets) == 0 {
		add("targets: at least one target is required")
	}

	seen := map[string]bool{}
	for i, t := range c.Targets {
		p := fmt.Sprintf("targets[%d]", i)
		if t.Name != "" {
			p = fmt.Sprintf("targets[%d] (%s)", i, t.Name)
		}
		switch {
		case t.Host == "":
			add("%s: host is required", p)
		case strings.Contains(t.Host, "://") || strings.ContainsAny(t.Host, " /"):
			add("%s: host must be a bare hostname or IP (put URLs under http.url), got %q", p, t.Host)
		}
		if !nameRE.MatchString(t.Name) {
			add("%s: name may only contain letters, digits, '.', '_' and '-'", p)
		}
		if seen[t.Name] {
			add("%s: duplicate target name %q", p, t.Name)
		}
		seen[t.Name] = true
		if t.Interval.D() < time.Second {
			add("%s: interval must be at least 1s", p)
		}
		if t.Timeout.D() <= 0 || t.Timeout > t.Interval {
			add("%s: timeout (%s) must be positive and not exceed interval (%s)", p, t.Timeout.D(), t.Interval.D())
		}
		if t.DNS == nil && t.HTTP == nil && t.TLS == nil && t.TCP == nil {
			add("%s: no checks configured (add at least one of dns, http, tls, tcp)", p)
		}

		if d := t.DNS; d != nil {
			for _, typ := range d.Types {
				if _, ok := dns.StringToType[strings.ToUpper(typ)]; !ok {
					add("%s: dns.types: unknown record type %q", p, typ)
				}
			}
		}
		if h := t.HTTP; h != nil {
			u, err := url.Parse(h.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				add("%s: http.url: must be an absolute http(s) URL, got %q", p, h.URL)
			}
			switch strings.ToUpper(h.Method) {
			case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
				http.MethodPatch, http.MethodDelete, http.MethodOptions:
			default:
				add("%s: http.method: unsupported method %q", p, h.Method)
			}
			for _, s := range h.ExpectStatus {
				if s < 100 || s > 599 {
					add("%s: http.expect_status: %d is not a valid HTTP status", p, s)
				}
			}
		}
		if s := t.TLS; s != nil {
			if s.Port < 1 || s.Port > 65535 {
				add("%s: tls.port: must be 1-65535, got %d", p, s.Port)
			}
			if s.CritDays < 0 || s.WarnDays < s.CritDays {
				add("%s: tls: warn_days (%d) must be >= crit_days (%d) >= 0", p, s.WarnDays, s.CritDays)
			}
		}
		if q := t.TCP; q != nil {
			if q.Port < 1 || q.Port > 65535 {
				add("%s: tcp.port: must be 1-65535, got %d", p, q.Port)
			}
			if q.Count < 1 || q.Count > 100 {
				add("%s: tcp.count: must be 1-100, got %d", p, q.Count)
			}
		}
	}
	return errors.Join(errs...)
}

// Kinds returns the names of the checks enabled on a target, in display order.
func (t Target) Kinds() []string {
	var out []string
	if t.DNS != nil {
		out = append(out, "dns")
	}
	if t.HTTP != nil {
		out = append(out, "http")
	}
	if t.TLS != nil {
		out = append(out, "tls")
	}
	if t.TCP != nil {
		out = append(out, "tcp")
	}
	return out
}

// CheckCount returns the total number of checks across all targets.
func (c *Config) CheckCount() int {
	n := 0
	for _, t := range c.Targets {
		n += len(t.Kinds())
	}
	return n
}
