// Package output renders one-shot check results for humans (a coloured
// table), machines (JSON) and CI systems (Markdown, e.g. for
// $GITHUB_STEP_SUMMARY), and maps results to a process exit code.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/checks"
)

// Format is an output format name.
type Format string

// Supported formats.
const (
	FormatTable    Format = "table"
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
)

// ParseFormat validates a format name.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(s)); f {
	case FormatTable, FormatJSON, FormatMarkdown:
		return f, nil
	case "md":
		return FormatMarkdown, nil
	}
	return "", fmt.Errorf("unknown output format %q (valid: table, json, markdown)", s)
}

// Summary counts results by status.
type Summary struct {
	Total int `json:"total"`
	OK    int `json:"ok"`
	Warn  int `json:"warn"`
	Fail  int `json:"fail"`
}

// Summarize counts results by status.
func Summarize(results []checks.Result) Summary {
	s := Summary{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case checks.StatusOK:
			s.OK++
		case checks.StatusWarn:
			s.Warn++
		case checks.StatusFail, checks.StatusUnknown:
			s.Fail++
		}
	}
	return s
}

// Exit codes follow the Nagios/Icinga plugin convention, so netpulse can be
// dropped into existing monitoring and scripts can branch on severity:
//
//	0 OK · 1 WARNING · 2 CRITICAL · 3 UNKNOWN (bad usage / config error)
const (
	ExitOK       = 0
	ExitWarn     = 1
	ExitCritical = 2
	ExitUnknown  = 3
)

// ExitCode maps results to an exit code.
func ExitCode(results []checks.Result) int {
	worst := checks.StatusOK
	for _, r := range results {
		st := r.Status
		if st == checks.StatusUnknown {
			st = checks.StatusFail
		}
		worst = checks.Worst(worst, st)
	}
	switch worst {
	case checks.StatusFail:
		return ExitCritical
	case checks.StatusWarn:
		return ExitWarn
	default:
		return ExitOK
	}
}

// Report is the JSON document produced by `netpulse check -o json`.
type Report struct {
	GeneratedAt time.Time       `json:"generated_at"`
	DurationMS  float64         `json:"duration_ms"`
	Summary     Summary         `json:"summary"`
	Results     []checks.Result `json:"results"`
}

// JSON writes results as an indented JSON report.
func JSON(w io.Writer, results []checks.Result, elapsed time.Duration) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(Report{
		GeneratedAt: time.Now().UTC(),
		DurationMS:  float64(elapsed) / float64(time.Millisecond),
		Summary:     Summarize(results),
		Results:     results,
	})
}

// Markdown writes a GitHub-flavoured Markdown table.
func Markdown(w io.Writer, results []checks.Result, elapsed time.Duration) error {
	icon := map[checks.Status]string{
		checks.StatusOK: "🟢", checks.StatusWarn: "🟡", checks.StatusFail: "🔴", checks.StatusUnknown: "⚪",
	}
	s := Summarize(results)
	var b strings.Builder
	fmt.Fprintf(&b, "### NetPulse report\n\n")
	fmt.Fprintf(&b, "**%d checks** · 🟢 %d ok · 🟡 %d warn · 🔴 %d fail · completed in %s\n\n",
		s.Total, s.OK, s.Warn, s.Fail, checks.FormatDuration(elapsed))
	b.WriteString("| Target | Check | Status | Latency | Result |\n|---|---|---|---:|---|\n")
	for _, r := range results {
		text := r.Summary
		if r.Status >= checks.StatusWarn && r.Error != "" {
			text = r.Error
		}
		fmt.Fprintf(&b, "| %s | %s | %s %s | %s | %s |\n",
			mdEscape(r.Target), r.Kind, icon[r.Status], r.Status,
			checks.FormatDuration(r.Latency), mdEscape(text))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
