package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
targets:
  - host: example.com
    dns: {}
    http: {}
    tls: {}
    tcp: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval.D() != DefaultInterval || cfg.Timeout.D() != DefaultTimeout || cfg.Concurrency != DefaultConcurrency {
		t.Fatalf("global defaults not applied: %+v", cfg)
	}
	tg := cfg.Targets[0]
	if tg.Name != "example.com" {
		t.Errorf("name should default to host, got %q", tg.Name)
	}
	if tg.HTTP.URL != "https://example.com/" || !*tg.HTTP.FollowRedirects {
		t.Errorf("http defaults wrong: %+v", tg.HTTP)
	}
	if tg.TLS.Port != 443 || tg.TLS.WarnDays != 30 || tg.TLS.CritDays != 7 {
		t.Errorf("tls defaults wrong: %+v", tg.TLS)
	}
	if tg.TCP.Count != 3 || tg.TCP.Port != 443 {
		t.Errorf("tcp defaults wrong: %+v", tg.TCP)
	}
	if strings.Join(tg.DNS.Types, ",") != "A,AAAA" {
		t.Errorf("dns types default wrong: %v", tg.DNS.Types)
	}
	if got := strings.Join(tg.Kinds(), ","); got != "dns,http,tls,tcp" {
		t.Errorf("Kinds() = %s", got)
	}
}

func TestTargetOverrides(t *testing.T) {
	cfg, err := Parse([]byte(`
interval: 60s
timeout: 20s
targets:
  - name: fast
    host: example.com
    interval: 5s
    tcp: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	tg := cfg.Targets[0]
	if tg.Interval.D() != 5*time.Second {
		t.Errorf("interval override ignored: %s", tg.Interval.D())
	}
	// The inherited 20s timeout must be clamped to the 5s interval.
	if tg.Timeout.D() != 5*time.Second {
		t.Errorf("timeout should be clamped to interval, got %s", tg.Timeout.D())
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	_, err := Parse([]byte(`
concurrency: -1
targets:
  - name: bad name!
    host: https://example.com
    http: { url: "ftp://x", expect_status: [999] }
    tls: { warn_days: 3, crit_days: 10 }
    tcp: { port: 70000 }
  - name: dup
    host: a.example
  - name: dup
    host: b.example
    dns: { types: [BOGUS] }
`))
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{
		"concurrency", "name may only contain", "bare hostname", "http.url", "999",
		"warn_days", "tcp.port", "no checks configured", "duplicate target name", "BOGUS",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error output missing %q:\n%v", want, err)
		}
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	_, err := Parse([]byte(`
targets:
  - host: example.com
    http: { expect_staus: [200] }
`))
	if err == nil || !strings.Contains(err.Error(), "expect_staus") {
		t.Fatalf("typo should be rejected, got %v", err)
	}
}

func TestEnvExpansion(t *testing.T) {
	t.Setenv("NP_TEST_TOKEN", "s3cret")
	cfg, err := Parse([]byte(`
targets:
  - host: example.com
    http:
      headers: { Authorization: "Bearer ${NP_TEST_TOKEN}" }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Targets[0].HTTP.Headers["Authorization"]; got != "Bearer s3cret" {
		t.Fatalf("got %q", got)
	}
}

func TestInvalidDuration(t *testing.T) {
	if _, err := Parse([]byte("interval: soon\ntargets: [{host: x, tcp: {}}]")); err == nil {
		t.Fatal("expected duration error")
	}
}

func TestLoadExampleConfig(t *testing.T) {
	// The shipped example must always be valid.
	cfg, err := Load(filepath.Join("..", "..", "configs", "netpulse.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) < 5 {
		t.Fatalf("example has only %d targets", len(cfg.Targets))
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); !os.IsNotExist(unwrapAll(err)) {
		t.Fatalf("want not-exist error, got %v", err)
	}
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

func TestFromHosts(t *testing.T) {
	cfg, err := FromHosts([]string{
		"example.com",
		"https://api.example.com:8443/health",
		"http://plain.example.com",
		"db.example.com:5432",
		"example.com",
	}, QuickOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Target{}
	for _, tg := range cfg.Targets {
		byName[tg.Name] = tg
	}

	if tg := byName["example.com"]; tg.DNS == nil || tg.HTTP == nil || tg.TLS == nil || tg.TCP == nil {
		t.Errorf("bare host should enable all checks: %v", tg.Kinds())
	}
	if tg := byName["api.example.com"]; tg.HTTP.URL != "https://api.example.com:8443/health" || tg.TLS.Port != 8443 || tg.TCP.Port != 8443 {
		t.Errorf("URL host wrong: %+v %+v", tg.HTTP, tg.TLS)
	}
	if tg := byName["plain.example.com"]; tg.TLS != nil || tg.TCP.Port != 80 {
		t.Errorf("http:// should skip TLS and use port 80: %v", tg.Kinds())
	}
	if tg := byName["db.example.com"]; tg.HTTP != nil || tg.TLS != nil || tg.TCP.Port != 5432 {
		t.Errorf("host:port should be TCP (and DNS) only: %v", tg.Kinds())
	}
	if _, ok := byName["example.com-2"]; !ok {
		t.Errorf("duplicate hosts should get unique names: %v", byName)
	}
}

func TestFromHostsCheckFilter(t *testing.T) {
	cfg, err := FromHosts([]string{"example.com"}, QuickOptions{Checks: []string{"tls"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Targets[0].Kinds(), ","); got != "tls" {
		t.Fatalf("got %s", got)
	}
	if _, err := FromHosts([]string{"ftp://x"}, QuickOptions{}); err == nil {
		t.Fatal("expected error for ftp scheme")
	}
}
