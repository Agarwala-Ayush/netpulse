package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTLSCheckThresholds(t *testing.T) {
	pki := newTestPKI(t)
	now := time.Now()
	day := 24 * time.Hour

	tests := []struct {
		name       string
		notAfter   time.Time
		wantStatus Status
		wantInErr  string
	}{
		{"healthy", now.Add(90 * day), StatusOK, ""},
		{"inside warn window", now.Add(20 * day), StatusWarn, ""},
		{"inside critical window", now.Add(3 * day), StatusFail, "critical threshold"},
		{"expired", now.Add(-2 * day), StatusFail, "expired"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr := serveTLS(t, pki.leaf(t, "netpulse.test", now.Add(-30*day), tc.notAfter))
			c, err := NewTLSChecker(TLSConfig{
				Address: addr, ServerName: "netpulse.test",
				WarnDays: 30, CritDays: 7, Roots: pki.pool,
			})
			if err != nil {
				t.Fatal(err)
			}
			res := c.Check(context.Background())
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s (summary %q, error %q)", res.Status, tc.wantStatus, res.Summary, res.Error)
			}
			if tc.wantInErr != "" && !strings.Contains(res.Error, tc.wantInErr) {
				t.Errorf("error %q does not mention %q", res.Error, tc.wantInErr)
			}
			if res.TLS == nil || res.TLS.Subject != "netpulse.test" {
				t.Fatalf("missing or wrong TLS info: %+v", res.TLS)
			}
			if len(res.Phases) != 2 {
				t.Errorf("want connect+handshake phases, got %v", res.Phases)
			}
		})
	}
}

func TestTLSCheckHostnameMismatch(t *testing.T) {
	pki := newTestPKI(t)
	addr := serveTLS(t, pki.leaf(t, "right.test", time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour)))
	c, _ := NewTLSChecker(TLSConfig{Address: addr, ServerName: "wrong.test", Roots: pki.pool})
	res := c.Check(context.Background())
	if res.Status != StatusFail || !strings.Contains(res.Error, "not valid for") {
		t.Fatalf("want hostname failure, got %s: %s", res.Status, res.Error)
	}
	if res.TLS == nil || res.TLS.Verified {
		t.Fatal("certificate details should still be reported, with Verified=false")
	}
}

func TestTLSCheckUntrustedCA(t *testing.T) {
	pki := newTestPKI(t)
	addr := serveTLS(t, pki.leaf(t, "netpulse.test", time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour)))
	// No Roots: the system trust store does not know our test CA.
	c, _ := NewTLSChecker(TLSConfig{Address: addr, ServerName: "netpulse.test"})
	res := c.Check(context.Background())
	if res.Status != StatusFail || !strings.Contains(res.Error, "unknown authority") {
		t.Fatalf("want unknown authority failure, got %s: %s", res.Status, res.Error)
	}
}

func TestTLSCheckConnectionRefused(t *testing.T) {
	c, _ := NewTLSChecker(TLSConfig{Address: closedPort(t)})
	res := c.Check(context.Background())
	if res.Status != StatusFail || res.TLS != nil {
		t.Fatalf("want fail without TLS info, got %s %+v", res.Status, res.TLS)
	}
}

func TestNewTLSCheckerValidation(t *testing.T) {
	if _, err := NewTLSChecker(TLSConfig{Address: "no-port"}); err == nil {
		t.Fatal("expected error for address without port")
	}
}
