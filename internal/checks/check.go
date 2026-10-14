// Package checks implements the individual network probes that netpulse runs:
// DNS resolution, HTTP(S) requests, TLS certificate inspection and TCP
// connect latency.
//
// Every probe implements the Checker interface and reports a Result. A
// Checker never returns a Go error: failures are data ("the site is down")
// rather than exceptional conditions, so they are encoded in Result.Status
// and Result.Error. This keeps the runner, metrics and UI code uniform.
package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Kind identifies a type of check.
type Kind string

// Supported check kinds.
const (
	KindDNS  Kind = "dns"
	KindHTTP Kind = "http"
	KindTLS  Kind = "tls"
	KindTCP  Kind = "tcp"
)

// AllKinds lists every check kind in canonical display order.
var AllKinds = []Kind{KindDNS, KindHTTP, KindTLS, KindTCP}

// ParseKind converts a string such as "http" into a Kind.
func ParseKind(s string) (Kind, error) {
	k := Kind(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range AllKinds {
		if k == known {
			return k, nil
		}
	}
	return "", fmt.Errorf("unknown check %q (valid: dns, http, tls, tcp)", s)
}

// Status is the health verdict of a single check execution.
//
// The numeric ordering matters: a higher value is "worse", which lets us
// aggregate many results with a simple max (see Worst).
type Status int

// Possible statuses, from best to worst.
const (
	StatusUnknown Status = iota // not executed yet
	StatusOK
	StatusWarn
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	case StatusFail:
		return "fail"
	default:
		return "unknown"
	}
}

// MarshalText makes Status serialise as "ok"/"warn"/"fail" in JSON.
func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText is the inverse of MarshalText.
func (s *Status) UnmarshalText(b []byte) error {
	switch string(b) {
	case "ok":
		*s = StatusOK
	case "warn":
		*s = StatusWarn
	case "fail":
		*s = StatusFail
	default:
		*s = StatusUnknown
	}
	return nil
}

// Worst returns the more severe of two statuses.
func Worst(a, b Status) Status {
	if b > a {
		return b
	}
	return a
}

// Phase is a named slice of time within a check, e.g. "tls" or "ttfb".
type Phase struct {
	Name     string        `json:"name"`
	Duration time.Duration `json:"-"`
}

// MarshalJSON renders the duration in milliseconds, which is what humans and
// dashboards want; time.Duration's default nanosecond integer is not.
func (p Phase) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name string  `json:"name"`
		MS   float64 `json:"ms"`
	}{p.Name, ms(p.Duration)})
}

// Detail is an ordered key/value pair shown in verbose output and the UI.
type Detail struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// HTTPInfo holds HTTP specific data used by metrics and the UI.
type HTTPInfo struct {
	StatusCode    int    `json:"status_code"`
	Proto         string `json:"proto"`
	ContentLength int64  `json:"content_length"`
	FinalURL      string `json:"final_url"`
	Redirects     int    `json:"redirects"`
}

// TLSInfo holds certificate data used by metrics and the UI.
type TLSInfo struct {
	Subject        string    `json:"subject"`
	Issuer         string    `json:"issuer"`
	DNSNames       []string  `json:"dns_names"`
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`       // leaf certificate expiry
	EarliestExpiry time.Time `json:"earliest_expiry"` // earliest expiry in the presented chain
	DaysLeft       float64   `json:"days_left"`       // based on EarliestExpiry
	Version        string    `json:"version"`
	CipherSuite    string    `json:"cipher_suite"`
	ChainLength    int       `json:"chain_length"`
	Verified       bool      `json:"verified"`
}

// TCPInfo holds connect-latency statistics.
type TCPInfo struct {
	Address  string        `json:"address"`
	Sent     int           `json:"sent"`
	Received int           `json:"received"`
	Min      time.Duration `json:"-"`
	Avg      time.Duration `json:"-"`
	Max      time.Duration `json:"-"`
	Jitter   time.Duration `json:"-"`
	Loss     float64       `json:"loss"` // 0.0 – 1.0
}

// MarshalJSON renders the RTT statistics in milliseconds.
func (t TCPInfo) MarshalJSON() ([]byte, error) {
	type alias TCPInfo
	return json.Marshal(struct {
		alias
		MinMS    float64 `json:"min_ms"`
		AvgMS    float64 `json:"avg_ms"`
		MaxMS    float64 `json:"max_ms"`
		JitterMS float64 `json:"jitter_ms"`
	}{alias(t), ms(t.Min), ms(t.Avg), ms(t.Max), ms(t.Jitter)})
}

// DNSRecord is a single answer from a DNS query.
type DNSRecord struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   uint32 `json:"ttl"`
}

// DNSInfo holds the resolver used and every record returned.
type DNSInfo struct {
	Server  string      `json:"server"`
	Records []DNSRecord `json:"records"`
}

// Result is the outcome of one execution of one check against one target.
type Result struct {
	Target    string        `json:"target"`
	Group     string        `json:"group,omitempty"`
	Host      string        `json:"host"`
	Kind      Kind          `json:"check"`
	Status    Status        `json:"status"`
	Latency   time.Duration `json:"-"`
	Summary   string        `json:"summary"`
	Error     string        `json:"error,omitempty"`
	Phases    []Phase       `json:"phases,omitempty"`
	Details   []Detail      `json:"details,omitempty"`
	StartedAt time.Time     `json:"started_at"`

	HTTP *HTTPInfo `json:"http,omitempty"`
	TLS  *TLSInfo  `json:"tls,omitempty"`
	TCP  *TCPInfo  `json:"tcp,omitempty"`
	DNS  *DNSInfo  `json:"dns,omitempty"`
}

// MarshalJSON adds a latency_ms field.
func (r Result) MarshalJSON() ([]byte, error) {
	type alias Result // alias drops the method set, avoiding infinite recursion
	return json.Marshal(struct {
		alias
		LatencyMS float64 `json:"latency_ms"`
	}{alias(r), ms(r.Latency)})
}

// ID uniquely identifies the (target, check) pair a result belongs to.
func (r Result) ID() string { return r.Target + "/" + string(r.Kind) }

// AddDetail appends a key/value detail, skipping empty values.
func (r *Result) AddDetail(key, value string) {
	if value == "" {
		return
	}
	r.Details = append(r.Details, Detail{Key: key, Value: value})
}

// fail marks the result failed with the given error.
func (r *Result) fail(err error) {
	r.Status = StatusFail
	r.Error = err.Error()
	if r.Summary == "" {
		r.Summary = Shorten(err.Error(), 90)
	}
}

// warn downgrades an OK result to WARN, recording why.
func (r *Result) warn(reason string) {
	if r.Status < StatusWarn {
		r.Status = StatusWarn
	}
	r.AddDetail("warning", reason)
}

// Checker is implemented by every probe type.
type Checker interface {
	// Kind returns the type of check.
	Kind() Kind
	// Check performs one probe. It must honour ctx cancellation and must
	// not panic; the runner recovers panics anyway as a last line of defence.
	Check(ctx context.Context) Result
}

// Shorten truncates s to at most n runes, appending an ellipsis.
func Shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// FormatDuration renders a duration compactly for terminal output:
// 850µs, 12.4ms, 1.32s.
func FormatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < 10*time.Millisecond:
		return fmt.Sprintf("%.1fms", ms(d))
	case d < time.Second:
		return fmt.Sprintf("%.0fms", ms(d))
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
