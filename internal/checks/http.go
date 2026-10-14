package checks

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxBodyBytes caps how much of a response body we read. We need the body to
// measure transfer time and for body_contains assertions, but a health check
// must never be able to exhaust memory by pointing at a large download.
const maxBodyBytes = 1 << 20 // 1 MiB

// maxRedirects mirrors the net/http default.
const maxRedirects = 10

// HTTPConfig configures an HTTP(S) check.
type HTTPConfig struct {
	URL                string
	Method             string
	Headers            map[string]string
	Body               string
	ExpectStatus       []int // empty means any 2xx or 3xx
	BodyContains       string
	FollowRedirects    bool
	InsecureSkipVerify bool
	LatencyWarn        time.Duration
	UserAgent          string
	Roots              *x509.CertPool // trust anchors; nil means the system trust store
}

// HTTPChecker performs an HTTP request and measures each phase of it.
type HTTPChecker struct {
	cfg    HTTPConfig
	client *http.Client
}

// NewHTTPChecker validates cfg and builds a checker.
func NewHTTPChecker(cfg HTTPConfig) (*HTTPChecker, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("http: invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("http: url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("http: url has no host")
	}
	if cfg.Method == "" {
		cfg.Method = http.MethodGet
	}
	cfg.Method = strings.ToUpper(cfg.Method)
	if cfg.UserAgent == "" {
		cfg.UserAgent = "netpulse"
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second, // the per-check context deadline is the real bound
			KeepAlive: -1,
		}).DialContext,
		// Every probe must open a brand new connection. With keep-alives a
		// second run would reuse the socket and report 0ms for DNS, connect
		// and TLS, hiding exactly the latency we want to observe.
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			RootCAs:            cfg.Roots,
			InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // explicit, opt-in per target
		},
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if !cfg.FollowRedirects {
				return http.ErrUseLastResponse
			}
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
	return &HTTPChecker{cfg: cfg, client: client}, nil
}

// Kind implements Checker.
func (c *HTTPChecker) Kind() Kind { return KindHTTP }

// httpTimings records timestamps from httptrace hooks. With redirects the
// hooks fire once per hop; later hops overwrite earlier ones, so the phase
// breakdown describes the final request while Latency covers the whole chain.
type httpTimings struct {
	dnsStart, dnsDone         time.Time
	connectStart, connectDone time.Time
	tlsStart, tlsDone         time.Time
	wroteRequest, firstByte   time.Time
}

// Check implements Checker.
func (c *HTTPChecker) Check(ctx context.Context) Result {
	res := Result{Kind: KindHTTP, Status: StatusOK}

	var body io.Reader
	if c.cfg.Body != "" {
		body = strings.NewReader(c.cfg.Body)
	}
	req, err := http.NewRequestWithContext(ctx, c.cfg.Method, c.cfg.URL, body)
	if err != nil {
		res.fail(err)
		return res
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	for k, v := range c.cfg.Headers {
		if strings.EqualFold(k, "host") {
			req.Host = v // Go ignores a "Host" entry in req.Header
			continue
		}
		req.Header.Set(k, v)
	}

	var t httpTimings
	redirects := -1
	trace := &httptrace.ClientTrace{
		GetConn:              func(string) { redirects++ },
		DNSStart:             func(httptrace.DNSStartInfo) { t.dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { t.dnsDone = time.Now() },
		ConnectStart:         func(_, _ string) { t.connectStart = time.Now() },
		ConnectDone:          func(_, _ string, _ error) { t.connectDone = time.Now() },
		TLSHandshakeStart:    func() { t.tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { t.tlsDone = time.Now() },
		WroteRequest:         func(httptrace.WroteRequestInfo) { t.wroteRequest = time.Now() },
		GotFirstResponseByte: func() { t.firstByte = time.Now() },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	start := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		res.Latency = time.Since(start)
		res.Phases = t.phases(time.Now())
		res.fail(simplifyURLError(err))
		return res
	}
	defer resp.Body.Close()

	content, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	end := time.Now()
	res.Latency = end.Sub(start)
	res.Phases = t.phases(end)
	res.HTTP = &HTTPInfo{
		StatusCode:    resp.StatusCode,
		Proto:         resp.Proto,
		ContentLength: int64(len(content)),
		FinalURL:      resp.Request.URL.String(),
		Redirects:     max(redirects, 0),
	}

	res.Summary = fmt.Sprintf("%d %s · %s · %s", resp.StatusCode,
		http.StatusText(resp.StatusCode), resp.Proto, humanBytes(len(content)))
	res.AddDetail("url", c.cfg.URL)
	if res.HTTP.FinalURL != c.cfg.URL {
		res.AddDetail("final url", res.HTTP.FinalURL)
	}
	res.AddDetail("status", strconv.Itoa(resp.StatusCode))
	res.AddDetail("protocol", resp.Proto)
	res.AddDetail("server", resp.Header.Get("Server"))
	res.AddDetail("content-type", resp.Header.Get("Content-Type"))
	if resp.TLS != nil {
		res.AddDetail("tls", tls.VersionName(resp.TLS.Version))
	}

	if readErr != nil {
		res.fail(fmt.Errorf("reading body: %w", readErr))
		return res
	}
	if !c.statusAccepted(resp.StatusCode) {
		res.fail(fmt.Errorf("unexpected status %d (want %s)", resp.StatusCode, c.expectDescription()))
		res.Summary = fmt.Sprintf("%d %s · expected %s", resp.StatusCode,
			http.StatusText(resp.StatusCode), c.expectDescription())
		return res
	}
	if c.cfg.BodyContains != "" && !bytes.Contains(content, []byte(c.cfg.BodyContains)) {
		res.fail(fmt.Errorf("response body does not contain %q", c.cfg.BodyContains))
		res.Summary = fmt.Sprintf("%d · body missing %q", resp.StatusCode, Shorten(c.cfg.BodyContains, 30))
		return res
	}
	if c.cfg.LatencyWarn > 0 && res.Latency > c.cfg.LatencyWarn {
		res.warn(fmt.Sprintf("request took %s (threshold %s)",
			FormatDuration(res.Latency), FormatDuration(c.cfg.LatencyWarn)))
	}
	return res
}

func (c *HTTPChecker) statusAccepted(code int) bool {
	if len(c.cfg.ExpectStatus) == 0 {
		return code >= 200 && code < 400
	}
	return slices.Contains(c.cfg.ExpectStatus, code)
}

func (c *HTTPChecker) expectDescription() string {
	if len(c.cfg.ExpectStatus) == 0 {
		return "2xx/3xx"
	}
	parts := make([]string, len(c.cfg.ExpectStatus))
	for i, s := range c.cfg.ExpectStatus {
		parts[i] = strconv.Itoa(s)
	}
	return strings.Join(parts, "|")
}

// phases converts raw timestamps into the classic curl-style breakdown:
//
//	dns → connect → tls → server (time to first byte) → transfer
func (t httpTimings) phases(end time.Time) []Phase {
	var out []Phase
	add := func(name string, from, to time.Time) {
		if !from.IsZero() && !to.IsZero() && to.After(from) {
			out = append(out, Phase{Name: name, Duration: to.Sub(from)})
		}
	}
	add("dns", t.dnsStart, t.dnsDone)
	add("connect", t.connectStart, t.connectDone)
	add("tls", t.tlsStart, t.tlsDone)
	add("server", t.wroteRequest, t.firstByte)
	add("transfer", t.firstByte, end)
	return out
}

// simplifyURLError strips the noisy `Get "https://…":` prefix that
// *url.Error adds, since the URL is already shown next to the result.
func simplifyURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, context.DeadlineExceeded) {
			return errors.New("timeout: no complete response before deadline")
		}
		return ue.Err
	}
	return err
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
