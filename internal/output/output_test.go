package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/checks"
)

func results(statuses ...checks.Status) []checks.Result {
	out := make([]checks.Result, len(statuses))
	for i, s := range statuses {
		out[i] = checks.Result{Target: "t", Kind: checks.KindHTTP, Status: s, Summary: "sum | pipe", Latency: time.Millisecond}
	}
	return out
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		in   []checks.Status
		want int
	}{
		{[]checks.Status{checks.StatusOK, checks.StatusOK}, ExitOK},
		{[]checks.Status{checks.StatusOK, checks.StatusWarn}, ExitWarn},
		{[]checks.Status{checks.StatusWarn, checks.StatusFail}, ExitCritical},
		{[]checks.Status{checks.StatusUnknown}, ExitCritical}, // cancelled counts as failure
		{nil, ExitOK},
	}
	for _, c := range cases {
		if got := ExitCode(results(c.in...)); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, results(checks.StatusOK, checks.StatusFail), time.Second); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Summary Summary
		Results []map[string]any
	}
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Total != 2 || rep.Summary.Fail != 1 || len(rep.Results) != 2 {
		t.Fatalf("unexpected report %+v", rep)
	}
}

func TestMarkdownEscapesPipes(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, results(checks.StatusOK), time.Second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `sum \| pipe`) {
		t.Fatalf("pipe not escaped:\n%s", buf.String())
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	rs := results(checks.StatusOK, checks.StatusWarn)
	rs[1].Details = []checks.Detail{{Key: "warning", Value: "slow"}}
	if err := Table(&buf, rs, time.Second, TableOptions{Verbose: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"TARGET", "HTTP", "1 ok", "1 warn", "slow"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestParseFormat(t *testing.T) {
	if f, err := ParseFormat("MD"); err != nil || f != FormatMarkdown {
		t.Fatalf("got %v %v", f, err)
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("expected error")
	}
}
