// Package tui implements `netpulse watch`, a live full-screen terminal
// dashboard built on Bubble Tea.
//
// Bubble Tea follows the Elm architecture: all state lives in a Model, the
// Update function turns messages (key presses, window resizes, new check
// results, clock ticks) into a new Model, and View renders the Model to a
// string. Nothing mutates state outside Update, so there are no locks here:
// results from the concurrent scheduler arrive as messages on a channel.
package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/output"
	"github.com/agarwala-ayush/netpulse/internal/runner"
)

const historyLen = 40

type resultMsg checks.Result

type tickMsg time.Time

type cell struct {
	latest  checks.Result
	seen    bool
	history []float64
}

type row struct {
	name, host, group string
	cells             map[checks.Kind]*cell
}

// Model is the Bubble Tea model.
type Model struct {
	title    string
	rows     []*row
	index    map[string]int
	kinds    []checks.Kind
	results  <-chan checks.Result
	rerun    func()
	cursor   int
	offset   int
	width    int
	height   int
	started  time.Time
	interval time.Duration
	help     bool
}

// New builds a model for the given jobs. results delivers check results;
// rerun is invoked when the user presses "r".
func New(title string, jobs []runner.Job, results <-chan checks.Result, rerun func()) Model {
	m := Model{
		title: title, results: results, rerun: rerun,
		index: map[string]int{}, started: time.Now(), width: 100, height: 30,
	}
	present := map[checks.Kind]bool{}
	for _, j := range jobs {
		i, ok := m.index[j.Target]
		if !ok {
			i = len(m.rows)
			m.index[j.Target] = i
			m.rows = append(m.rows, &row{name: j.Target, host: j.Host, group: j.Group, cells: map[checks.Kind]*cell{}})
		}
		m.rows[i].cells[j.Checker.Kind()] = &cell{}
		present[j.Checker.Kind()] = true
		if m.interval == 0 || j.Interval < m.interval {
			m.interval = j.Interval
		}
	}
	for _, k := range checks.AllKinds {
		if present[k] {
			m.kinds = append(m.kinds, k)
		}
	}
	return m
}

func (m Model) waitForResult() tea.Cmd {
	return func() tea.Msg {
		r, ok := <-m.results
		if !ok {
			return nil
		}
		return resultMsg(r)
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return tea.Batch(m.waitForResult(), tick()) }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case resultMsg:
		r := checks.Result(msg)
		if i, ok := m.index[r.Target]; ok {
			c := m.rows[i].cells[r.Kind]
			if c == nil {
				c = &cell{}
				m.rows[i].cells[r.Kind] = c
			}
			c.latest, c.seen = r, true
			c.history = append(c.history, float64(r.Latency)/float64(time.Millisecond))
			if len(c.history) > historyLen {
				c.history = c.history[len(c.history)-historyLen:]
			}
		}
		return m, m.waitForResult()

	case tickMsg:
		return m, tick()

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.rows)-1 {
				m.cursor++
			}
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			m.cursor = len(m.rows) - 1
		case "r":
			if m.rerun != nil {
				m.rerun()
			}
		case "?":
			m.help = !m.help
		}
		m.clampScroll()
	}
	return m, nil
}

// visibleRows is how many table rows fit between the header and the
// detail pane.
func (m Model) visibleRows() int {
	return max(3, m.height-6-m.detailHeight())
}

func (m Model) detailHeight() int { return len(m.kinds) + 4 }

func (m *Model) clampScroll() {
	n := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+n {
		m.offset = m.cursor - n + 1
	}
	m.offset = max(0, min(m.offset, max(0, len(m.rows)-n)))
}

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	b.WriteString(m.table())
	b.WriteString("\n")
	b.WriteString(m.detail())
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) counts() (ok, warn, fail, pending int) {
	for _, r := range m.rows {
		for _, c := range r.cells {
			switch {
			case !c.seen:
				pending++
			case c.latest.Status == checks.StatusOK:
				ok++
			case c.latest.Status == checks.StatusWarn:
				warn++
			default:
				fail++
			}
		}
	}
	return
}

func (m Model) header() string {
	ok, warn, fail, pending := m.counts()
	logo := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0b1020")).
		Background(output.ColorAccent).Padding(0, 1).Render("◉ " + m.title)
	stats := fmt.Sprintf("%s  %s  %s",
		output.StatusStyle(checks.StatusOK).Render(fmt.Sprintf("✔ %d", ok)),
		output.StatusStyle(checks.StatusWarn).Render(fmt.Sprintf("▲ %d", warn)),
		output.StatusStyle(checks.StatusFail).Render(fmt.Sprintf("✖ %d", fail)))
	if pending > 0 {
		stats += output.Muted.Render(fmt.Sprintf("  … %d pending", pending))
	}
	meta := output.Muted.Render(fmt.Sprintf("%d targets · every %s · up %s",
		len(m.rows), m.interval, time.Since(m.started).Round(time.Second)))
	return " " + logo + "  " + stats + "   " + meta
}

func (m Model) nameWidth() int {
	w := 10
	for _, r := range m.rows {
		w = max(w, lipgloss.Width(r.name))
	}
	return min(w, 28) + 4
}

const colWidth = 15

func (m Model) table() string {
	nameW := m.nameWidth()
	head := output.Muted.Bold(true)
	var b strings.Builder

	b.WriteString(head.Width(nameW).Render("  TARGET"))
	for _, k := range m.kinds {
		b.WriteString(head.Width(colWidth).Render(strings.ToUpper(string(k))))
	}
	b.WriteString(head.Render("TREND"))
	b.WriteString("\n")

	end := min(len(m.rows), m.offset+m.visibleRows())
	for i := m.offset; i < end; i++ {
		r := m.rows[i]
		selected := i == m.cursor
		prefix := "  "
		nameStyle := lipgloss.NewStyle().Width(nameW)
		if selected {
			prefix = output.Accent.Render("▸ ")
			nameStyle = nameStyle.Bold(true)
		}
		b.WriteString(nameStyle.Render(prefix + checks.Shorten(r.name, nameW-3)))
		for _, k := range m.kinds {
			b.WriteString(lipgloss.NewStyle().Width(colWidth).Render(renderCell(k, r.cells[k])))
		}
		b.WriteString(output.Accent.UnsetBold().Render(sparkline(trendOf(r), 20)))
		b.WriteString("\n")
	}
	if len(m.rows) > end || m.offset > 0 {
		b.WriteString(output.Faint.Render(fmt.Sprintf("  showing %d-%d of %d", m.offset+1, end, len(m.rows))))
		b.WriteString("\n")
	}
	return b.String()
}

func renderCell(k checks.Kind, c *cell) string {
	if c == nil {
		return output.Faint.Render("·")
	}
	if !c.seen {
		return output.Muted.Render("… pending")
	}
	st := output.StatusStyle(c.latest.Status)
	val := checks.FormatDuration(c.latest.Latency)
	if k == checks.KindTLS && c.latest.TLS != nil {
		val = fmt.Sprintf("%dd left", int(math.Floor(c.latest.TLS.DaysLeft)))
	}
	if c.latest.Status == checks.StatusFail {
		val = "FAIL"
	}
	return st.Render(output.StatusIcon(c.latest.Status) + " " + val)
}

// trendOf picks the most interesting latency series for the trend column.
func trendOf(r *row) []float64 {
	for _, k := range []checks.Kind{checks.KindHTTP, checks.KindTCP, checks.KindDNS, checks.KindTLS} {
		if c := r.cells[k]; c != nil && len(c.history) > 0 {
			return c.history
		}
	}
	return nil
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders values as a strip of block characters scaled to the
// series maximum, keeping the most recent `width` points.
func sparkline(vals []float64, width int) string {
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	maxV := 0.0
	for _, v := range vals {
		maxV = max(maxV, v)
	}
	if maxV == 0 {
		return strings.Repeat(" ", width)
	}
	var b strings.Builder
	for _, v := range vals {
		idx := int(v / maxV * float64(len(sparkRunes)-1))
		b.WriteRune(sparkRunes[max(0, min(idx, len(sparkRunes)-1))])
	}
	return b.String()
}

func (m Model) detail() string {
	if len(m.rows) == 0 {
		return ""
	}
	r := m.rows[m.cursor]
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(output.ColorFaint).
		Padding(0, 1).Width(max(40, m.width-4))

	title := output.Bold.Render(r.name) + output.Muted.Render(" · "+r.host)
	if r.group != "" {
		title += output.Faint.Render("  [" + r.group + "]")
	}
	lines := []string{title}
	maxSummary := max(20, m.width-40)
	for _, k := range m.kinds {
		c := r.cells[k]
		if c == nil {
			continue
		}
		kind := output.Accent.Width(6).Render(strings.ToUpper(string(k)))
		if !c.seen {
			lines = append(lines, kind+output.Muted.Render("waiting for first run…"))
			continue
		}
		res := c.latest
		text := res.Summary
		if res.Status == checks.StatusFail && res.Error != "" {
			text = res.Error
		}
		line := kind + output.StatusStyle(res.Status).Render(output.StatusIcon(res.Status)+" ") +
			checks.Shorten(text, maxSummary)
		if len(res.Phases) > 0 {
			ph := make([]string, len(res.Phases))
			for i, p := range res.Phases {
				ph[i] = p.Name + " " + checks.FormatDuration(p.Duration)
			}
			line += output.Faint.Render("   " + strings.Join(ph, " → "))
		}
		lines = append(lines, line)
	}
	return box.Render(strings.Join(lines, "\n"))
}

func (m Model) footer() string {
	keys := []string{"↑/↓ select", "r re-run now", "q quit"}
	if m.help {
		keys = append(keys, "g/G top/bottom", "TLS column shows days until expiry", "TREND = latency history")
	} else {
		keys = append(keys, "? more")
	}
	return output.Faint.Render(" " + strings.Join(keys, " · "))
}
