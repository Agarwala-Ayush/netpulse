// Package metrics exposes check results as Prometheus metrics.
//
// Naming follows the Prometheus conventions: a `netpulse_` namespace, base
// units (seconds, not milliseconds), `_total` for counters, and timestamps
// as `_timestamp_seconds`. Label cardinality is bounded by configuration:
// every label value comes from the config file (target, group, check,
// phase, record type) or a small fixed set (status) — never from responses,
// which could explode the number of series.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/version"
)

const namespace = "netpulse"

var (
	checkLabels = []string{"target", "group", "check"}
	statuses    = []checks.Status{checks.StatusOK, checks.StatusWarn, checks.StatusFail}
)

// Metrics owns a dedicated Prometheus registry and every netpulse metric.
//
// We use our own registry rather than prometheus.DefaultRegisterer: no
// hidden global state, tests can create as many instances as they like, and
// /metrics contains exactly what we chose to register.
type Metrics struct {
	registry *prometheus.Registry

	up          *prometheus.GaugeVec
	status      *prometheus.GaugeVec
	duration    *prometheus.HistogramVec
	lastLatency *prometheus.GaugeVec
	lastRun     *prometheus.GaugeVec
	runs        *prometheus.CounterVec
	phase       *prometheus.GaugeVec

	httpStatus *prometheus.GaugeVec
	tlsExpiry  *prometheus.GaugeVec
	tlsVerify  *prometheus.GaugeVec
	dnsRecords *prometheus.GaugeVec
	tcpRTT     *prometheus.GaugeVec
	tcpLoss    *prometheus.GaugeVec

	targets       prometheus.Gauge
	reloads       *prometheus.CounterVec
	reloadSuccess prometheus.Gauge
	reloadTime    prometheus.Gauge
}

// New creates and registers all metrics.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),

		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "check_up",
			Help: "1 if the last run of the check did not fail (ok or warn), 0 otherwise.",
		}, checkLabels),
		status: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "check_status",
			Help: "Current check status as a state set: exactly one of ok/warn/fail is 1.",
		}, append(checkLabels, "status")),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "check_duration_seconds",
			Help:    "Distribution of check latencies.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, checkLabels),
		lastLatency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "check_latency_seconds",
			Help: "Latency of the most recent check run.",
		}, checkLabels),
		lastRun: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "check_last_run_timestamp_seconds",
			Help: "Unix time the check last completed.",
		}, checkLabels),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "check_runs_total",
			Help: "Total check runs by resulting status.",
		}, append(checkLabels, "status")),
		phase: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "check_phase_duration_seconds",
			Help: "Duration of each phase of the most recent run (e.g. dns, connect, tls, server, transfer).",
		}, append(checkLabels, "phase")),

		httpStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "http_status_code",
			Help: "HTTP status code of the most recent response.",
		}, []string{"target", "group"}),
		tlsExpiry: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "tls_cert_expiry_timestamp_seconds",
			Help: "Unix time at which the earliest-expiring certificate in the presented chain expires.",
		}, []string{"target", "group"}),
		tlsVerify: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "tls_cert_verified",
			Help: "1 if the certificate chain verified against the system trust store for the expected hostname.",
		}, []string{"target", "group"}),
		dnsRecords: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "dns_records",
			Help: "Number of records returned by the most recent lookup, by record type.",
		}, []string{"target", "group", "type"}),
		tcpRTT: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "tcp_rtt_seconds",
			Help: "TCP connect round-trip statistics from the most recent run.",
		}, []string{"target", "group", "stat"}),
		tcpLoss: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "tcp_loss_ratio",
			Help: "Fraction (0-1) of TCP connection attempts that failed in the most recent run.",
		}, []string{"target", "group"}),

		targets: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "targets",
			Help: "Number of configured (target, check) jobs.",
		}),
		reloads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "config_reloads_total",
			Help: "Configuration reload attempts by result.",
		}, []string{"result"}),
		reloadSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "config_last_reload_successful",
			Help: "1 if the last configuration reload succeeded.",
		}),
		reloadTime: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "config_last_reload_success_timestamp_seconds",
			Help: "Unix time of the last successful configuration load.",
		}),
	}

	info := version.Get()
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "build_info",
		Help: "Build metadata; the value is always 1.",
		ConstLabels: prometheus.Labels{
			"version": info.Version, "commit": info.Commit, "goversion": info.GoVersion,
		},
	})
	buildInfo.Set(1)

	m.registry.MustRegister(
		m.up, m.status, m.duration, m.lastLatency, m.lastRun, m.runs, m.phase,
		m.httpStatus, m.tlsExpiry, m.tlsVerify, m.dnsRecords, m.tcpRTT, m.tcpLoss,
		m.targets, m.reloads, m.reloadSuccess, m.reloadTime, buildInfo,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Registry exposes the underlying registry (useful in tests).
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler returns the HTTP handler that serves /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Registry:          m.registry, // also instrument the handler itself
	})
}

// Observe records one check result. It implements runner.Sink.
func (m *Metrics) Observe(r checks.Result) {
	if r.Status == checks.StatusUnknown {
		return
	}
	kind := string(r.Kind)
	lv := []string{r.Target, r.Group, kind}

	up := 0.0
	if r.Status != checks.StatusFail {
		up = 1
	}
	m.up.WithLabelValues(lv...).Set(up)
	for _, s := range statuses {
		v := 0.0
		if s == r.Status {
			v = 1
		}
		m.status.WithLabelValues(r.Target, r.Group, kind, s.String()).Set(v)
	}
	m.runs.WithLabelValues(r.Target, r.Group, kind, r.Status.String()).Inc()
	m.lastRun.WithLabelValues(lv...).Set(float64(r.StartedAt.Add(r.Latency).UnixMilli()) / 1000)
	if r.Latency > 0 {
		m.duration.WithLabelValues(lv...).Observe(r.Latency.Seconds())
		m.lastLatency.WithLabelValues(lv...).Set(r.Latency.Seconds())
	}
	for _, p := range r.Phases {
		m.phase.WithLabelValues(r.Target, r.Group, kind, p.Name).Set(p.Duration.Seconds())
	}

	switch {
	case r.HTTP != nil:
		m.httpStatus.WithLabelValues(r.Target, r.Group).Set(float64(r.HTTP.StatusCode))
	case r.TLS != nil:
		m.tlsExpiry.WithLabelValues(r.Target, r.Group).Set(float64(r.TLS.EarliestExpiry.Unix()))
		m.tlsVerify.WithLabelValues(r.Target, r.Group).Set(boolFloat(r.TLS.Verified))
	case r.DNS != nil:
		// Drop previous per-type series first, so a record type that has
		// disappeared (e.g. AAAA removed) doesn't keep reporting its old count.
		m.dnsRecords.DeletePartialMatch(prometheus.Labels{"target": r.Target, "group": r.Group})
		counts := map[string]int{}
		for _, rec := range r.DNS.Records {
			counts[rec.Type]++
		}
		for typ, n := range counts {
			m.dnsRecords.WithLabelValues(r.Target, r.Group, typ).Set(float64(n))
		}
	case r.TCP != nil:
		m.tcpLoss.WithLabelValues(r.Target, r.Group).Set(r.TCP.Loss)
		if r.TCP.Received > 0 {
			for stat, d := range map[string]time.Duration{
				"min": r.TCP.Min, "avg": r.TCP.Avg, "max": r.TCP.Max, "jitter": r.TCP.Jitter,
			} {
				m.tcpRTT.WithLabelValues(r.Target, r.Group, stat).Set(d.Seconds())
			}
		}
	}
}

// Reset clears every per-target series. It is called on configuration
// reload so that targets removed from the config stop being exported
// instead of lingering forever with their last value ("stale series").
func (m *Metrics) Reset() {
	for _, v := range []interface{ Reset() }{
		m.up, m.status, m.duration, m.lastLatency, m.lastRun, m.runs, m.phase,
		m.httpStatus, m.tlsExpiry, m.tlsVerify, m.dnsRecords, m.tcpRTT, m.tcpLoss,
	} {
		v.Reset()
	}
}

// SetJobs records how many jobs are configured.
func (m *Metrics) SetJobs(n int) { m.targets.Set(float64(n)) }

// ConfigLoaded records the outcome of a configuration (re)load.
func (m *Metrics) ConfigLoaded(ok bool) {
	if ok {
		m.reloads.WithLabelValues("success").Inc()
		m.reloadSuccess.Set(1)
		m.reloadTime.SetToCurrentTime()
		return
	}
	m.reloads.WithLabelValues("failure").Inc()
	m.reloadSuccess.Set(0)
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
