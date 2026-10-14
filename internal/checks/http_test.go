package checks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPCheck(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"status":"ok"}`) })
	mux.HandleFunc("/missing", http.NotFound)
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(60 * time.Millisecond)
		_, _ = io.WriteString(w, "slow")
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", http.StatusFound)
	})
	mux.HandleFunc("/echo-header", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Token")+" "+r.UserAgent())
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tests := []struct {
		name   string
		cfg    HTTPConfig
		status Status
		errHas string
	}{
		{"ok", HTTPConfig{URL: srv.URL + "/ok"}, StatusOK, ""},
		{"404 fails by default", HTTPConfig{URL: srv.URL + "/missing"}, StatusFail, "unexpected status 404"},
		{"404 accepted when expected", HTTPConfig{URL: srv.URL + "/missing", ExpectStatus: []int{404}}, StatusOK, ""},
		{"body assertion passes", HTTPConfig{URL: srv.URL + "/ok", BodyContains: `"status":"ok"`}, StatusOK, ""},
		{"body assertion fails", HTTPConfig{URL: srv.URL + "/ok", BodyContains: "healthy"}, StatusFail, "does not contain"},
		{"latency warning", HTTPConfig{URL: srv.URL + "/slow", LatencyWarn: 10 * time.Millisecond}, StatusWarn, ""},
		{"redirect followed", HTTPConfig{URL: srv.URL + "/redirect", FollowRedirects: true, ExpectStatus: []int{200}}, StatusOK, ""},
		{"redirect not followed", HTTPConfig{URL: srv.URL + "/redirect", ExpectStatus: []int{200}}, StatusFail, "unexpected status 302"},
		{"headers sent", HTTPConfig{URL: srv.URL + "/echo-header", Headers: map[string]string{"X-Token": "s3cret"},
			UserAgent: "netpulse-test", BodyContains: "s3cret netpulse-test"}, StatusOK, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewHTTPChecker(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			res := c.Check(context.Background())
			if res.Status != tc.status {
				t.Fatalf("status = %s, want %s (summary %q, error %q)", res.Status, tc.status, res.Summary, res.Error)
			}
			if tc.errHas != "" && !strings.Contains(res.Error, tc.errHas) {
				t.Errorf("error %q does not contain %q", res.Error, tc.errHas)
			}
			if res.HTTP == nil {
				t.Fatal("expected HTTP info")
			}
			if res.Latency <= 0 {
				t.Error("latency not recorded")
			}
		})
	}
}

func TestHTTPCheckPhasesOverTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()

	// httptest's certificate is signed by its own CA; trust it explicitly.
	c, err := NewHTTPChecker(HTTPConfig{URL: srv.URL, Roots: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
	if err != nil {
		t.Fatal(err)
	}
	res := c.Check(context.Background())
	if res.Status != StatusOK {
		t.Fatalf("status %s: %s", res.Status, res.Error)
	}
	names := map[string]bool{}
	for _, p := range res.Phases {
		names[p.Name] = true
	}
	for _, want := range []string{"connect", "tls", "server"} {
		if !names[want] {
			t.Errorf("missing phase %q in %v", want, res.Phases)
		}
	}

	// Without the CA, verification must fail.
	strict, _ := NewHTTPChecker(HTTPConfig{URL: srv.URL})
	if res := strict.Check(context.Background()); res.Status != StatusFail {
		t.Fatalf("expected certificate failure, got %s", res.Status)
	}
	// insecure_skip_verify opts out of verification.
	insecure, _ := NewHTTPChecker(HTTPConfig{URL: srv.URL, InsecureSkipVerify: true})
	if res := insecure.Check(context.Background()); res.Status != StatusOK {
		t.Fatalf("insecure check should pass, got %s: %s", res.Status, res.Error)
	}
}

func TestHTTPCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	c, _ := NewHTTPChecker(HTTPConfig{URL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := c.Check(ctx)
	if res.Status != StatusFail || !strings.Contains(res.Error, "timeout") {
		t.Fatalf("want timeout failure, got %s: %s", res.Status, res.Error)
	}
}

func TestHTTPCheckBodyIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 3*maxBodyBytes))
	}))
	defer srv.Close()
	c, _ := NewHTTPChecker(HTTPConfig{URL: srv.URL})
	res := c.Check(context.Background())
	if res.HTTP.ContentLength != maxBodyBytes {
		t.Fatalf("read %d bytes, want cap of %d", res.HTTP.ContentLength, maxBodyBytes)
	}
}

func TestNewHTTPCheckerValidation(t *testing.T) {
	for _, u := range []string{"ftp://example.com", "://bad", "https://"} {
		if _, err := NewHTTPChecker(HTTPConfig{URL: u}); err == nil {
			t.Errorf("expected error for %q", u)
		}
	}
}
