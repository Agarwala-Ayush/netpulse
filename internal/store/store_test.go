package store

import (
	"sync"
	"testing"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/checks"
)

func TestRing(t *testing.T) {
	r := newRing[int](3)
	if r.len() != 0 || len(r.items()) != 0 {
		t.Fatal("new ring should be empty")
	}
	for i := 1; i <= 5; i++ {
		r.push(i)
	}
	got := r.items()
	if r.len() != 3 || got[0] != 3 || got[1] != 4 || got[2] != 5 {
		t.Fatalf("want [3 4 5] oldest-first, got %v", got)
	}
}

func TestSnapshot(t *testing.T) {
	s := New(10)
	s.Register([]JobInfo{
		{Target: "a", Host: "a.example", Kind: checks.KindDNS},
		{Target: "a", Host: "a.example", Kind: checks.KindHTTP},
		{Target: "b", Host: "b.example", Kind: checks.KindTCP},
	})

	snap := s.Snapshot()
	if snap.Summary.Pending != 3 || snap.Summary.Targets != 2 {
		t.Fatalf("all checks should start pending: %+v", snap.Summary)
	}

	now := time.Now()
	s.Observe(checks.Result{Target: "a", Kind: checks.KindDNS, Status: checks.StatusOK, StartedAt: now})
	s.Observe(checks.Result{Target: "a", Kind: checks.KindHTTP, Status: checks.StatusFail, StartedAt: now})
	s.Observe(checks.Result{Target: "a", Kind: checks.KindHTTP, Status: checks.StatusOK, StartedAt: now})
	s.Observe(checks.Result{Target: "b", Kind: checks.KindTCP, Status: checks.StatusWarn, StartedAt: now})

	snap = s.Snapshot()
	if snap.Summary.OK != 2 || snap.Summary.Warn != 1 || snap.Summary.Fail != 0 || snap.Summary.Pending != 0 {
		t.Fatalf("summary wrong: %+v", snap.Summary)
	}
	if snap.Targets[0].Name != "a" || snap.Targets[1].Name != "b" {
		t.Fatal("targets must keep registration order")
	}
	http := snap.Targets[0].Checks[1]
	if http.Runs != 2 || http.Availability != 50 || len(http.History) != 2 {
		t.Fatalf("http availability/history wrong: %+v", http)
	}
	if snap.Targets[1].Status != checks.StatusWarn {
		t.Fatalf("target status should be worst of its checks")
	}
}

func TestConcurrentAccess(t *testing.T) {
	// Run with -race: concurrent writers and readers must not race.
	s := New(50)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 200 {
				s.Observe(checks.Result{Target: string(rune('a' + i)), Kind: checks.KindDNS, Status: checks.StatusOK, StartedAt: time.Now()})
			}
		})
		wg.Go(func() {
			for range 50 {
				_ = s.Snapshot()
			}
		})
	}
	wg.Wait()
	if got := s.Snapshot().Summary.Checks; got != 8 {
		t.Fatalf("want 8 checks, got %d", got)
	}
}
