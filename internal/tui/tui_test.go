package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/runner"
)

type stubChecker struct{ kind checks.Kind }

func (s stubChecker) Kind() checks.Kind                   { return s.kind }
func (s stubChecker) Check(context.Context) checks.Result { return checks.Result{} }

func TestModelUpdateAndView(t *testing.T) {
	jobs := []runner.Job{
		{Target: "alpha", Host: "alpha.example", Interval: time.Second, Checker: stubChecker{checks.KindHTTP}},
		{Target: "beta", Host: "beta.example", Interval: time.Second, Checker: stubChecker{checks.KindTCP}},
	}
	m := New("Test", jobs, make(chan checks.Result), nil)

	view := m.View()
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "pending") {
		t.Fatalf("initial view should list targets as pending:\n%s", view)
	}

	next, _ := m.Update(resultMsg(checks.Result{
		Target: "alpha", Kind: checks.KindHTTP, Status: checks.StatusOK,
		Latency: 42 * time.Millisecond, Summary: "200 OK",
	}))
	m = next.(Model)
	if !strings.Contains(m.View(), "42ms") {
		t.Fatalf("view should show latency after a result:\n%s", m.View())
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d after down, want 1", m.cursor)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if next.(Model).cursor != 1 {
		t.Fatal("cursor must not move past the last row")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q should quit")
	}
}

func TestSparkline(t *testing.T) {
	if got := sparkline([]float64{1, 2, 4, 8}, 10); got != "▁▂▄█" {
		t.Fatalf("got %q", got)
	}
	if got := sparkline(nil, 3); got != "   " {
		t.Fatalf("empty series should render blanks, got %q", got)
	}
	if got := []rune(sparkline(make([]float64, 50), 5)); len(got) != 5 {
		t.Fatalf("width not respected: %d", len(got))
	}
}
