package output

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"

	"github.com/agarwala-ayush/netpulse/internal/checks"
)

// TableOptions tune the table renderer.
type TableOptions struct {
	Verbose bool // print phases and details for every check
}

// Table renders results as a bordered, coloured table followed by a
// one-line summary. Rows for the same target are grouped together.
func Table(w io.Writer, results []checks.Result, elapsed time.Duration, opts TableOptions) error {
	headerStyle := lipgloss.NewStyle().Foreground(ColorMuted).Bold(true).Padding(0, 1)
	cell := lipgloss.NewStyle().Padding(0, 1)

	rows := make([][]string, 0, len(results))
	statuses := make([]checks.Status, 0, len(results))
	prev := ""
	for _, r := range results {
		target := ""
		if r.Target != prev {
			target = r.Target
			prev = r.Target
		}
		text := r.Summary
		if r.Status == checks.StatusFail && r.Error != "" && !strings.Contains(r.Summary, r.Error) {
			text = r.Error
		}
		rows = append(rows, []string{
			target,
			strings.ToUpper(string(r.Kind)),
			StatusBadge(r.Status),
			checks.FormatDuration(r.Latency),
			checks.Shorten(text, 72),
		})
		statuses = append(statuses, r.Status)
	}

	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(Faint).
		BorderRow(false).
		Headers("TARGET", "CHECK", "STATUS", "LATENCY", "RESULT").
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			s := cell
			switch col {
			case 0:
				s = s.Bold(true)
			case 1:
				s = s.Foreground(ColorAccent2)
			case 3:
				s = s.Align(lipgloss.Right)
			case 4:
				if statuses[row] >= checks.StatusWarn {
					s = s.Foreground(StatusColor(statuses[row]))
				}
			}
			return s
		})

	var b strings.Builder
	b.WriteString(t.Render())
	b.WriteString("\n")
	if opts.Verbose {
		b.WriteString(verbose(results))
	}
	b.WriteString(SummaryLine(results, elapsed))
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// SummaryLine renders "✔ 13 ok  ▲ 1 warn  ✖ 1 fail  · 15 checks in 1.42s".
func SummaryLine(results []checks.Result, elapsed time.Duration) string {
	s := Summarize(results)
	parts := []string{
		StatusStyle(checks.StatusOK).Render(fmt.Sprintf("✔ %d ok", s.OK)),
		StatusStyle(checks.StatusWarn).Render(fmt.Sprintf("▲ %d warn", s.Warn)),
		StatusStyle(checks.StatusFail).Render(fmt.Sprintf("✖ %d fail", s.Fail)),
	}
	return " " + strings.Join(parts, "   ") +
		Muted.Render(fmt.Sprintf("   · %d checks in %s", s.Total, checks.FormatDuration(elapsed)))
}

func verbose(results []checks.Result) string {
	var b strings.Builder
	key := Muted.Width(24)
	for _, r := range results {
		fmt.Fprintf(&b, "\n %s %s %s\n", StatusStyle(r.Status).Render(StatusIcon(r.Status)),
			Bold.Render(r.Target), Accent.Render(strings.ToUpper(string(r.Kind))))
		if r.Error != "" {
			fmt.Fprintf(&b, "   %s%s\n", key.Render("error"), StatusStyle(checks.StatusFail).Render(r.Error))
		}
		if len(r.Phases) > 0 {
			ph := make([]string, len(r.Phases))
			for i, p := range r.Phases {
				ph[i] = fmt.Sprintf("%s %s", p.Name, checks.FormatDuration(p.Duration))
			}
			fmt.Fprintf(&b, "   %s%s\n", key.Render("timing"), strings.Join(ph, Faint.Render(" → ")))
		}
		for _, d := range r.Details {
			v := d.Value
			if d.Key == "warning" {
				v = StatusStyle(checks.StatusWarn).Render(v)
			}
			fmt.Fprintf(&b, "   %s%s\n", key.Render(d.Key), v)
		}
	}
	b.WriteString("\n")
	return b.String()
}
