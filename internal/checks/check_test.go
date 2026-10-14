package checks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorst(t *testing.T) {
	if Worst(StatusOK, StatusFail) != StatusFail || Worst(StatusWarn, StatusOK) != StatusWarn {
		t.Fatal("Worst must return the more severe status")
	}
	if Worst(StatusUnknown, StatusOK) != StatusOK {
		t.Fatal("any real status is worse than unknown")
	}
}

func TestParseKind(t *testing.T) {
	if k, err := ParseKind(" HTTP "); err != nil || k != KindHTTP {
		t.Fatalf("got %q, %v", k, err)
	}
	if _, err := ParseKind("icmp"); err == nil {
		t.Fatal("expected error")
	}
}

func TestResultJSON(t *testing.T) {
	r := Result{
		Target: "x", Kind: KindHTTP, Status: StatusWarn, Latency: 1500 * time.Microsecond,
		Phases: []Phase{{Name: "dns", Duration: 2 * time.Millisecond}},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"status":"warn"`, `"latency_ms":1.5`, `"check":"http"`, `{"name":"dns","ms":2}`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON %s missing %s", s, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "-",
		450 * time.Microsecond:  "450µs",
		3200 * time.Microsecond: "3.2ms",
		42 * time.Millisecond:   "42ms",
		1500 * time.Millisecond: "1.50s",
	}
	for d, want := range cases {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestShorten(t *testing.T) {
	if got := Shorten("héllo world", 6); got != "héllo…" {
		t.Fatalf("got %q", got)
	}
	if got := Shorten("short", 10); got != "short" {
		t.Fatalf("got %q", got)
	}
}
