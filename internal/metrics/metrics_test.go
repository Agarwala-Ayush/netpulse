package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/agarwala-ayush/netpulse/internal/checks"
)

func TestObserve(t *testing.T) {
	m := New()
	expiry := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	m.Observe(checks.Result{
		Target: "web", Group: "prod", Kind: checks.KindHTTP, Status: checks.StatusOK,
		Latency: 250 * time.Millisecond, StartedAt: time.Now(),
		Phases: []checks.Phase{{Name: "dns", Duration: 10 * time.Millisecond}},
		HTTP:   &checks.HTTPInfo{StatusCode: 200},
	})
	m.Observe(checks.Result{
		Target: "web", Group: "prod", Kind: checks.KindTLS, Status: checks.StatusFail, StartedAt: time.Now(),
		TLS: &checks.TLSInfo{EarliestExpiry: expiry, Verified: false},
	})

	if v := testutil.ToFloat64(m.up.WithLabelValues("web", "prod", "http")); v != 1 {
		t.Errorf("http up = %v", v)
	}
	if v := testutil.ToFloat64(m.up.WithLabelValues("web", "prod", "tls")); v != 0 {
		t.Errorf("tls up = %v", v)
	}
	if v := testutil.ToFloat64(m.status.WithLabelValues("web", "prod", "tls", "fail")); v != 1 {
		t.Errorf("tls status{fail} = %v", v)
	}
	if v := testutil.ToFloat64(m.status.WithLabelValues("web", "prod", "tls", "ok")); v != 0 {
		t.Errorf("tls status{ok} = %v", v)
	}
	if v := testutil.ToFloat64(m.httpStatus.WithLabelValues("web", "prod")); v != 200 {
		t.Errorf("http status = %v", v)
	}
	if v := testutil.ToFloat64(m.tlsExpiry.WithLabelValues("web", "prod")); v != float64(expiry.Unix()) {
		t.Errorf("tls expiry = %v", v)
	}
	if v := testutil.ToFloat64(m.phase.WithLabelValues("web", "prod", "http", "dns")); v != 0.01 {
		t.Errorf("dns phase = %v", v)
	}
	if v := testutil.ToFloat64(m.runs.WithLabelValues("web", "prod", "http", "ok")); v != 1 {
		t.Errorf("runs = %v", v)
	}
}

func TestResetDropsSeries(t *testing.T) {
	m := New()
	m.Observe(checks.Result{Target: "gone", Kind: checks.KindTCP, Status: checks.StatusOK, StartedAt: time.Now(),
		Latency: time.Millisecond, TCP: &checks.TCPInfo{Received: 1, Avg: time.Millisecond}})
	if n := testutil.CollectAndCount(m.up); n != 1 {
		t.Fatalf("want 1 series, got %d", n)
	}
	m.Reset()
	if n := testutil.CollectAndCount(m.up); n != 0 {
		t.Fatalf("series survived reset: %d", n)
	}
}

func TestDNSRecordSeriesReplaced(t *testing.T) {
	m := New()
	obs := func(types ...string) {
		var recs []checks.DNSRecord
		for _, typ := range types {
			recs = append(recs, checks.DNSRecord{Type: typ})
		}
		m.Observe(checks.Result{Target: "d", Kind: checks.KindDNS, Status: checks.StatusOK, StartedAt: time.Now(),
			DNS: &checks.DNSInfo{Records: recs}})
	}
	obs("A", "AAAA")
	obs("A")
	if n := testutil.CollectAndCount(m.dnsRecords); n != 1 {
		t.Fatalf("stale AAAA series should be removed, have %d series", n)
	}
}

func TestHandlerExposesMetrics(t *testing.T) {
	m := New()
	m.Observe(checks.Result{Target: "x", Kind: checks.KindDNS, Status: checks.StatusOK, StartedAt: time.Now(), Latency: time.Millisecond})
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{"netpulse_check_up", "netpulse_build_info", "go_goroutines", "netpulse_check_duration_seconds_bucket"} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %s", want)
		}
	}
}
