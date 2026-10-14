package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/metrics"
	"github.com/agarwala-ayush/netpulse/internal/store"
)

func newTestServer(ready bool) (*Server, *store.Store) {
	st := store.New(10)
	st.Observe(checks.Result{Target: "web", Host: "web.example", Kind: checks.KindHTTP,
		Status: checks.StatusOK, StartedAt: time.Now(), Summary: "<script>alert(1)</script>"})
	m := metrics.New()
	return New(Options{
		Title:    "Test",
		Metrics:  m.Handler(),
		Snapshot: st.Snapshot,
		Ready:    func() bool { return ready },
	}), st
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestRoutes(t *testing.T) {
	srv, _ := newTestServer(true)
	h := srv.Handler()

	tests := []struct {
		path, contentType, body string
		code                    int
	}{
		{"/", "text/html", "NetPulse", 200},
		{"/assets/app.js", "javascript", "api/v1/status", 200},
		{"/assets/app.css", "text/css", "--ok", 200},
		{"/healthz", "text/plain", "ok", 200},
		{"/readyz", "text/plain", "ready", 200},
		{"/metrics", "", "netpulse_build_info", 200},
		{"/api/v1/version", "application/json", "go_version", 200},
		{"/api/v1/status", "application/json", `"title": "Test"`, 200},
		{"/nope", "", "", 404},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, h, tc.path)
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d", rec.Code, tc.code)
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), tc.contentType) {
				t.Errorf("content-type = %q, want %q", rec.Header().Get("Content-Type"), tc.contentType)
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Errorf("body missing %q", tc.body)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("security headers missing")
			}
		})
	}
}

func TestReadyz(t *testing.T) {
	srv, _ := newTestServer(false)
	if rec := get(t, srv.Handler(), "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before first round = %d, want 503", rec.Code)
	}
}

func TestPostNotAllowed(t *testing.T) {
	srv, _ := newTestServer(true)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}

func TestStatusJSONShape(t *testing.T) {
	srv, _ := newTestServer(true)
	rec := get(t, srv.Handler(), "/api/v1/status")
	var body struct {
		Title   string
		Summary store.Summary
		Targets []store.TargetView
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Summary.OK != 1 || len(body.Targets) != 1 || body.Targets[0].Checks[0].Latest == nil {
		t.Fatalf("unexpected payload: %+v", body)
	}
}

func TestGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv, _ := newTestServer(true)
	srv.http.Addr = addr
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
}
