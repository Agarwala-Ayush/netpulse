// Package server implements the HTTP interface of `netpulse serve`:
//
//	GET /                 web dashboard (static files embedded in the binary)
//	GET /metrics          Prometheus / OpenMetrics exposition
//	GET /healthz          liveness: the process is up and serving
//	GET /readyz           readiness: every check has reported at least once
//	GET /api/v1/status    JSON snapshot of all targets, checks and history
//	GET /api/v1/version   build information
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/store"
	"github.com/agarwala-ayush/netpulse/internal/version"
)

//go:embed web
var webFS embed.FS

// Options configures the server.
type Options struct {
	Addr     string
	Title    string
	Metrics  http.Handler
	Snapshot func() store.Snapshot
	Ready    func() bool
	Logger   *slog.Logger
}

// Server wraps http.Server with netpulse's routes and middleware.
type Server struct {
	http   *http.Server
	opts   Options
	logger *slog.Logger
}

// New builds a server; call ListenAndServe to start it.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{opts: opts, logger: opts.Logger}
	s.http = &http.Server{
		Addr:    opts.Addr,
		Handler: s.routes(),
		// Timeouts protect against slow-loris style clients holding
		// connections open forever. WriteTimeout is generous because
		// /metrics can be large with many targets.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
	}
	return s
}

// Handler exposes the router (used by tests via httptest).
func (s *Server) Handler() http.Handler { return s.http.Handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // the embed directive guarantees the directory exists
	}
	fileServer := http.FileServerFS(static)

	// Go 1.22+ ServeMux patterns: method matching and {$} for an exact path.
	mux.Handle("GET /{$}", fileServer)
	mux.Handle("GET /assets/", fileServer)
	mux.Handle("GET /metrics", s.opts.Metrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.opts.Ready != nil && !s.opts.Ready() {
			writeText(w, http.StatusServiceUnavailable, "not ready: waiting for first round of checks")
			return
		}
		writeText(w, http.StatusOK, "ready")
	})
	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, version.Get())
	})

	return s.recoverer(s.logRequests(securityHeaders(mux)))
}

type statusResponse struct {
	Title string `json:"title"`
	store.Snapshot
	Version string `json:"version"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{
		Title:    s.opts.Title,
		Snapshot: s.opts.Snapshot(),
		Version:  version.Get().Version,
	})
}

// ListenAndServe starts serving and blocks until ctx is cancelled, then
// shuts down gracefully: it stops accepting new connections and waits up to
// 10s for in-flight requests (e.g. a Prometheus scrape) to finish.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	s.logger.Info("http server listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- s.http.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.logger.Info("http server shutting down")
		return s.http.Shutdown(shutdownCtx)
	}
}

// --- middleware -----------------------------------------------------------

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.logger.Debug("http request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start).Round(time.Microsecond),
			"remote", r.RemoteAddr)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity per net/http docs
					panic(p)
				}
				s.logger.Error("panic in http handler", "panic", p, "stack", string(debug.Stack()))
				writeText(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets conservative defaults. The dashboard loads nothing
// from third parties, so a strict Content-Security-Policy costs nothing.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeText(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg + "\n"))
}
