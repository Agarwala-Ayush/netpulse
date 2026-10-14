// Package store keeps the latest result and a short history for every check
// in memory. It backs the web dashboard and the JSON API.
//
// Prometheus is the long-term store; this package deliberately holds only a
// bounded window (a ring buffer per check) so netpulse stays a single
// stateless binary with predictable memory use.
package store

import (
	"sort"
	"sync"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/checks"
)

// Point is one historical sample, compact enough to ship to the browser.
type Point struct {
	Time      time.Time     `json:"t"`
	LatencyMS float64       `json:"ms"`
	Status    checks.Status `json:"s"`
}

type entry struct {
	target, group, host string
	kind                checks.Kind
	order               int
	latest              *checks.Result
	history             *ring[Point]
	runs, ok            int // lifetime counters for availability
}

// Store is safe for concurrent use. Writers (one per running check) take the
// write lock briefly; readers (HTTP handlers) take the read lock, so many
// dashboard viewers never block each other.
type Store struct {
	mu          sync.RWMutex
	historySize int
	entries     map[string]*entry
	started     time.Time
}

// New creates a store that keeps historySize points per check.
func New(historySize int) *Store {
	return &Store{historySize: max(historySize, 1), entries: map[string]*entry{}, started: time.Now()}
}

// JobInfo describes a check before it has produced any results.
type JobInfo struct {
	Target, Group, Host string
	Kind                checks.Kind
}

// Register declares the checks that will report, so the dashboard can show
// them as "pending" immediately and in configuration order. Any existing
// data is discarded (used on configuration reload).
func (s *Store) Register(jobs []JobInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[string]*entry, len(jobs))
	for i, j := range jobs {
		s.entries[j.Target+"/"+string(j.Kind)] = &entry{
			target: j.Target, group: j.Group, host: j.Host, kind: j.Kind,
			order: i, history: newRing[Point](s.historySize),
		}
	}
}

// Observe records a result. It implements runner.Sink.
func (s *Store) Observe(r checks.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[r.ID()]
	if !ok {
		e = &entry{
			target: r.Target, group: r.Group, host: r.Host, kind: r.Kind,
			order: len(s.entries), history: newRing[Point](s.historySize),
		}
		s.entries[r.ID()] = e
	}
	rc := r
	e.latest = &rc
	e.runs++
	if r.Status != checks.StatusFail {
		e.ok++
	}
	e.history.push(Point{Time: r.StartedAt, LatencyMS: float64(r.Latency) / float64(time.Millisecond), Status: r.Status})
}

// Summary aggregates the current state of every check.
type Summary struct {
	Targets int `json:"targets"`
	Checks  int `json:"checks"`
	OK      int `json:"ok"`
	Warn    int `json:"warn"`
	Fail    int `json:"fail"`
	Pending int `json:"pending"`
}

// CheckView is the dashboard's view of one check.
type CheckView struct {
	Kind         checks.Kind    `json:"kind"`
	Status       checks.Status  `json:"status"`
	Latest       *checks.Result `json:"latest,omitempty"`
	History      []Point        `json:"history"`
	Availability float64        `json:"availability"` // 0-100, since start/reload
	Runs         int            `json:"runs"`
}

// TargetView groups the checks of one target.
type TargetView struct {
	Name   string        `json:"name"`
	Group  string        `json:"group,omitempty"`
	Host   string        `json:"host"`
	Status checks.Status `json:"status"` // worst status among its checks
	Checks []CheckView   `json:"checks"`
}

// Snapshot is a consistent, point-in-time copy of the store.
type Snapshot struct {
	GeneratedAt time.Time    `json:"generated_at"`
	StartedAt   time.Time    `json:"started_at"`
	Summary     Summary      `json:"summary"`
	Targets     []TargetView `json:"targets"`
}

// Snapshot copies the current state. The copy shares no memory with the
// store, so callers can serialise it without holding any lock.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].order < list[j].order })

	snap := Snapshot{GeneratedAt: time.Now(), StartedAt: s.started}
	index := map[string]int{}
	for _, e := range list {
		ti, ok := index[e.target]
		if !ok {
			ti = len(snap.Targets)
			index[e.target] = ti
			snap.Targets = append(snap.Targets, TargetView{Name: e.target, Group: e.group, Host: e.host})
		}
		cv := CheckView{Kind: e.kind, History: e.history.items(), Runs: e.runs}
		if e.latest != nil {
			latest := *e.latest
			cv.Latest = &latest
			cv.Status = latest.Status
		}
		if e.runs > 0 {
			cv.Availability = 100 * float64(e.ok) / float64(e.runs)
		}
		t := &snap.Targets[ti]
		t.Checks = append(t.Checks, cv)
		t.Status = checks.Worst(t.Status, cv.Status)

		snap.Summary.Checks++
		switch cv.Status {
		case checks.StatusOK:
			snap.Summary.OK++
		case checks.StatusWarn:
			snap.Summary.Warn++
		case checks.StatusFail:
			snap.Summary.Fail++
		default:
			snap.Summary.Pending++
		}
	}
	snap.Summary.Targets = len(snap.Targets)
	return snap
}
