package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agarwala-ayush/netpulse/internal/output"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(append(args, "--no-color"), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// writeConfig writes a config that points only at local test servers, so
// these end-to-end tests never depend on the internet.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "netpulse.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckExitCodes(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer okSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badSrv.Close()

	healthy := writeConfig(t, "targets:\n  - name: local\n    host: 127.0.0.1\n    http: { url: "+okSrv.URL+" }\n")
	code, out, _ := runCLI(t, "check", "-c", healthy)
	if code != output.ExitOK || !strings.Contains(out, "local") {
		t.Fatalf("healthy: code %d, output:\n%s", code, out)
	}

	broken := writeConfig(t, "targets:\n  - name: broken\n    host: 127.0.0.1\n    http: { url: "+badSrv.URL+" }\n")
	code, out, _ = runCLI(t, "check", "-c", broken, "-o", "json")
	if code != output.ExitCritical {
		t.Fatalf("broken: code %d, want %d", code, output.ExitCritical)
	}
	var rep output.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out)
	}
	if rep.Summary.Fail != 1 {
		t.Fatalf("summary %+v", rep.Summary)
	}
}

func TestCheckUsageErrors(t *testing.T) {
	cases := [][]string{
		{"check", "-c", "/does/not/exist.yaml"},
		{"check", "example.com", "-o", "xml"},
		{"check", "example.com", "--checks", "icmp"},
		{"check", "-c", "x.yaml", "example.com"},
	}
	for _, args := range cases {
		code, _, stderr := runCLI(t, args...)
		if code != output.ExitUnknown {
			t.Errorf("%v: code %d, want %d (stderr %q)", args, code, output.ExitUnknown, stderr)
		}
		if !strings.Contains(stderr, "error:") {
			t.Errorf("%v: no error printed", args)
		}
	}
}

func TestInitAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netpulse.yaml")
	if code, _, stderr := runCLI(t, "init", path); code != 0 {
		t.Fatalf("init failed: %s", stderr)
	}
	if code, _, _ := runCLI(t, "init", path); code != output.ExitUnknown {
		t.Fatal("init must refuse to overwrite without --force")
	}
	if code, _, _ := runCLI(t, "init", path, "--force"); code != 0 {
		t.Fatal("init --force should overwrite")
	}
	code, out, stderr := runCLI(t, "validate", "-c", path)
	if code != 0 || !strings.Contains(out, "is valid") {
		t.Fatalf("validate failed (%d): %s %s", code, out, stderr)
	}

	bad := writeConfig(t, "targets:\n  - host: x\n    http: { expect_staus: [200] }\n")
	if code, _, _ := runCLI(t, "validate", "-c", bad); code != output.ExitUnknown {
		t.Fatalf("validate of a bad config should exit %d", output.ExitUnknown)
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := runCLI(t, "version", "--json")
	if code != 0 || !strings.Contains(out, `"go_version"`) {
		t.Fatalf("version --json: %d %s", code, out)
	}
}
