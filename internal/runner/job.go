// Package runner executes checks concurrently.
//
// It offers two modes:
//
//   - RunOnce: run every job exactly once with bounded parallelism and
//     return the results (used by `netpulse check`).
//   - Scheduler: run every job forever on its own interval, feeding results
//     to sinks such as Prometheus metrics and the dashboard store (used by
//     `netpulse serve` and `netpulse watch`).
//
// Both share Execute, which wraps a single check with a timeout and panic
// recovery so that one misbehaving probe can never take down the process.
package runner

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/config"
	"github.com/agarwala-ayush/netpulse/internal/version"
)

// Job is one check bound to one target, plus its scheduling parameters.
type Job struct {
	Target   string
	Group    string
	Host     string
	Interval time.Duration
	Timeout  time.Duration
	Checker  checks.Checker
}

// ID uniquely identifies the job, e.g. "github/http".
func (j Job) ID() string { return j.Target + "/" + string(j.Checker.Kind()) }

// BuildJobs turns a validated configuration into runnable jobs, one per
// (target, check) pair.
func BuildJobs(cfg *config.Config) ([]Job, error) {
	userAgent := "netpulse/" + version.Get().Version + " (+https://github.com/agarwala-ayush/netpulse)"

	var jobs []Job
	var errs []error
	for _, t := range cfg.Targets {
		add := func(c checks.Checker, err error) {
			if err != nil {
				errs = append(errs, fmt.Errorf("target %s: %w", t.Name, err))
				return
			}
			jobs = append(jobs, Job{
				Target:   t.Name,
				Group:    t.Group,
				Host:     t.Host,
				Interval: t.Interval.D(),
				Timeout:  t.Timeout.D(),
				Checker:  c,
			})
		}
		if d := t.DNS; d != nil {
			add(checks.NewDNSChecker(checks.DNSConfig{
				Host:        t.Host,
				Server:      d.Server,
				Types:       d.Types,
				Expect:      d.Expect,
				LatencyWarn: d.LatencyWarn.D(),
				Timeout:     t.Timeout.D(),
			}))
		}
		if h := t.HTTP; h != nil {
			add(checks.NewHTTPChecker(checks.HTTPConfig{
				URL:                h.URL,
				Method:             h.Method,
				Headers:            h.Headers,
				Body:               h.Body,
				ExpectStatus:       h.ExpectStatus,
				BodyContains:       h.BodyContains,
				FollowRedirects:    h.FollowRedirects == nil || *h.FollowRedirects,
				InsecureSkipVerify: h.InsecureSkipVerify,
				LatencyWarn:        h.LatencyWarn.D(),
				UserAgent:          userAgent,
				Roots:              loadRoots(h.CAFile, &errs, t.Name),
			}))
		}
		if s := t.TLS; s != nil {
			add(checks.NewTLSChecker(checks.TLSConfig{
				Address:     net.JoinHostPort(t.Host, strconv.Itoa(s.Port)),
				ServerName:  s.ServerName,
				WarnDays:    s.WarnDays,
				CritDays:    s.CritDays,
				LatencyWarn: s.LatencyWarn.D(),
				Roots:       loadRoots(s.CAFile, &errs, t.Name),
			}))
		}
		if p := t.TCP; p != nil {
			add(checks.NewTCPChecker(checks.TCPConfig{
				Address:       net.JoinHostPort(t.Host, strconv.Itoa(p.Port)),
				Count:         p.Count,
				ProbeInterval: p.ProbeInterval.D(),
				LatencyWarn:   p.LatencyWarn.D(),
			}))
		}
	}
	return jobs, errors.Join(errs...)
}

// Execute runs a single job with its timeout and stamps the result with the
// job's identity. It always returns a Result, even if the checker panics.
func Execute(ctx context.Context, j Job) (res checks.Result) {
	ctx, cancel := context.WithTimeout(ctx, j.Timeout)
	defer cancel()

	started := time.Now()
	defer func() {
		if p := recover(); p != nil {
			res = checks.Result{
				Kind:    j.Checker.Kind(),
				Status:  checks.StatusFail,
				Error:   fmt.Sprintf("internal error: check panicked: %v", p),
				Summary: "internal error (panic recovered)",
			}
		}
		res.Target, res.Group, res.Host, res.StartedAt = j.Target, j.Group, j.Host, started
		if res.Latency == 0 && res.Status == checks.StatusFail {
			res.Latency = time.Since(started)
		}
		// Make timeouts obvious: a deadline error deep inside a library can
		// otherwise surface as a confusing "i/o timeout" or "operation was
		// canceled" message.
		if res.Status == checks.StatusFail && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.Summary = fmt.Sprintf("timed out after %s", j.Timeout)
		}
	}()
	return j.Checker.Check(ctx)
}

// loadRoots reads a PEM bundle of CA certificates for targets that use a
// private PKI (e.g. an internal CA). An empty path means "use the system
// trust store" and returns nil. Errors are appended to errs.
func loadRoots(path string, errs *[]error, target string) *x509.CertPool {
	if path == "" {
		return nil
	}
	pem, err := os.ReadFile(path) //nolint:gosec // path comes from the operator's config
	if err != nil {
		*errs = append(*errs, fmt.Errorf("target %s: ca_file: %w", target, err))
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		*errs = append(*errs, fmt.Errorf("target %s: ca_file %s: no PEM certificates found", target, path))
		return nil
	}
	return pool
}
