package output

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/agarwala-ayush/netpulse/internal/checks"
)

// Shared palette for the table output and the TUI. lipgloss automatically
// degrades colours to what the terminal supports and disables them entirely
// when output is not a TTY or NO_COLOR is set.
var (
	ColorOK      = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#4ade80"}
	ColorWarn    = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#fbbf24"}
	ColorFail    = lipgloss.AdaptiveColor{Light: "#be123c", Dark: "#fb7185"}
	ColorMuted   = lipgloss.AdaptiveColor{Light: "#64748b", Dark: "#94a3b8"}
	ColorFaint   = lipgloss.AdaptiveColor{Light: "#94a3b8", Dark: "#475569"}
	ColorAccent  = lipgloss.AdaptiveColor{Light: "#0891b2", Dark: "#22d3ee"}
	ColorAccent2 = lipgloss.AdaptiveColor{Light: "#7c3aed", Dark: "#a78bfa"}

	Bold   = lipgloss.NewStyle().Bold(true)
	Muted  = lipgloss.NewStyle().Foreground(ColorMuted)
	Faint  = lipgloss.NewStyle().Foreground(ColorFaint)
	Accent = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
)

// StatusColor returns the colour for a status.
func StatusColor(s checks.Status) lipgloss.TerminalColor {
	switch s {
	case checks.StatusOK:
		return ColorOK
	case checks.StatusWarn:
		return ColorWarn
	case checks.StatusFail:
		return ColorFail
	default:
		return ColorMuted
	}
}

// StatusStyle returns a foreground style for a status.
func StatusStyle(s checks.Status) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(StatusColor(s))
}

// StatusIcon returns a one-cell glyph for a status.
func StatusIcon(s checks.Status) string {
	switch s {
	case checks.StatusOK:
		return "✔"
	case checks.StatusWarn:
		return "▲"
	case checks.StatusFail:
		return "✖"
	default:
		return "…"
	}
}

// StatusBadge renders e.g. "✔ OK" in the status colour.
func StatusBadge(s checks.Status) string {
	label := map[checks.Status]string{
		checks.StatusOK: "OK", checks.StatusWarn: "WARN", checks.StatusFail: "FAIL",
	}[s]
	if label == "" {
		label = "PENDING"
	}
	return StatusStyle(s).Bold(true).Render(StatusIcon(s) + " " + label)
}
