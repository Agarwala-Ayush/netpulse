package runner

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/checks"
)

// RunOnce executes every job once, at most `concurrency` at a time, and
// returns results in the same order as jobs. onResult, if non-nil, is called
// as each result arrives (from multiple goroutines, so it must be safe for
// concurrent use).
//
// The pattern is a counting semaphore: a buffered channel whose capacity is
// the concurrency limit. A goroutine is started per job, but each one must
// put a token into the channel before doing any network I/O, so no more than
// `concurrency` probes are ever in flight. Goroutines are cheap (a few KiB of
// stack); sockets and file descriptors are not, and the semaphore protects
// the latter.
func RunOnce(ctx context.Context, jobs []Job, concurrency int, onResult func(checks.Result)) []checks.Result {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]checks.Result, len(jobs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, job := range jobs {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = cancelled(job)
				return
			}
			// Each goroutine writes only its own index, so no mutex is needed.
			results[i] = Execute(ctx, job)
			if onResult != nil {
				onResult(results[i])
			}
		})
	}
	wg.Wait()
	return results
}

func cancelled(j Job) checks.Result {
	return checks.Result{
		Target: j.Target, Group: j.Group, Host: j.Host, Kind: j.Checker.Kind(),
		Status: checks.StatusUnknown, Summary: "cancelled", StartedAt: time.Now(),
	}
}

// Sink receives every result produced by the Scheduler. Implementations
// must be safe for concurrent use and should return quickly.
type Sink interface {
	Observe(checks.Result)
}

// SinkFunc adapts a function to the Sink interface.
type SinkFunc func(checks.Result)

// Observe implements Sink.
func (f SinkFunc) Observe(r checks.Result) { f(r) }

// Scheduler runs jobs continuously, each on its own interval.
type Scheduler struct {
	jobs        []Job
	concurrency int
	sinks       []Sink
	logger      *slog.Logger

	completed atomic.Int64 // jobs that have finished at least one run
	trigger   chan struct{}
}

// NewScheduler creates a scheduler. Call Run to start it.
func NewScheduler(jobs []Job, concurrency int, logger *slog.Logger, sinks ...Sink) *Scheduler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Scheduler{
		jobs:        jobs,
		concurrency: max(concurrency, 1),
		sinks:       sinks,
		logger:      logger,
		trigger:     make(chan struct{}),
	}
}

// Ready reports whether every job has completed at least one run. It backs
// the /readyz endpoint: until then, metrics would be incomplete.
func (s *Scheduler) Ready() bool { return s.completed.Load() >= int64(len(s.jobs)) }

// TriggerAll asks every job to run immediately (used by the TUI's "r" key).
// It never blocks.
func (s *Scheduler) TriggerAll() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled.
//
// Design: one goroutine per job, each with its own time.Ticker, sharing one
// semaphore. Compared with a central priority-queue scheduler this is far
// simpler, naturally supports a different interval per target, and scales to
// thousands of jobs since an idle goroutine blocked on a channel costs only
// its stack.
//
// Two details prevent self-inflicted problems:
//   - Start-up jitter: each job waits a random fraction of its interval
//     before its first run, so 500 targets do not all fire in the same
//     millisecond every interval (the "thundering herd").
//   - time.Ticker drops ticks for slow receivers, so a check that runs
//     longer than its interval never builds up a backlog of runs.
func (s *Scheduler) Run(ctx context.Context) {
	sem := make(chan struct{}, s.concurrency)
	broadcast := make([]chan struct{}, len(s.jobs))
	var wg sync.WaitGroup

	for i, job := range s.jobs {
		broadcast[i] = make(chan struct{}, 1)
		wg.Go(func() { s.loop(ctx, job, sem, broadcast[i]) })
	}

	// Fan the single trigger channel out to every job's own channel.
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.trigger:
				for _, ch := range broadcast {
					select {
					case ch <- struct{}{}:
					default: // already pending
					}
				}
			}
		}
	})

	s.logger.Info("scheduler started", "jobs", len(s.jobs), "concurrency", s.concurrency)
	wg.Wait()
	s.logger.Info("scheduler stopped")
}

func (s *Scheduler) loop(ctx context.Context, job Job, sem chan struct{}, trigger <-chan struct{}) {
	// Spread first runs over the first ~2s (or the interval if shorter) so
	// the dashboard fills quickly but probes still don't fire in lockstep.
	window := min(job.Interval, 2*time.Second)
	jitter := time.Duration(rand.Int64N(int64(window) + 1))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	first := true
	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()
	for {
		s.runOne(ctx, job, sem, first)
		first = false
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-trigger:
			ticker.Reset(job.Interval)
		}
	}
}

func (s *Scheduler) runOne(ctx context.Context, job Job, sem chan struct{}, first bool) {
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	res := Execute(ctx, job)
	<-sem

	if ctx.Err() != nil {
		return // shutting down; don't publish a spurious "cancelled" failure
	}
	if first {
		s.completed.Add(1)
	}
	level := slog.LevelDebug
	if res.Status >= checks.StatusWarn {
		level = slog.LevelWarn
	}
	s.logger.Log(ctx, level, "check completed",
		"target", res.Target, "check", res.Kind, "status", res.Status.String(),
		"latency", res.Latency.Round(time.Microsecond), "summary", res.Summary)

	for _, sink := range s.sinks {
		sink.Observe(res)
	}
}
