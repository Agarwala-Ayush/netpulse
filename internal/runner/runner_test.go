package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agarwala-ayush/netpulse/internal/checks"
	"github.com/agarwala-ayush/netpulse/internal/config"
)

// fakeChecker is a programmable checker that also tracks how many instances
// run at the same time, to verify the concurrency limit.
type fakeChecker struct {
	delay   time.Duration
	status  checks.Status
	panics  bool
	running *atomic.Int64
	peak    *atomic.Int64
	calls   atomic.Int64
}

func (f *fakeChecker) Kind() checks.Kind { return checks.KindTCP }

func (f *fakeChecker) Check(ctx context.Context) checks.Result {
	f.calls.Add(1)
	if f.running != nil {
		n := f.running.Add(1)
		defer f.running.Add(-1)
		for {
			p := f.peak.Load()
			if n <= p || f.peak.CompareAndSwap(p, n) {
				break
			}
		}
	}
	if f.panics {
		panic("boom")
	}
	select {
	case <-time.After(f.delay):
		return checks.Result{Kind: checks.KindTCP, Status: f.status, Latency: f.delay}
	case <-ctx.Done():
		return checks.Result{Kind: checks.KindTCP, Status: checks.StatusFail, Error: ctx.Err().Error()}
	}
}

func job(name string, c checks.Checker) Job {
	return Job{Target: name, Interval: time.Second, Timeout: time.Second, Checker: c}
}

func TestRunOnceRespectsConcurrencyLimit(t *testing.T) {
	var running, peak atomic.Int64
	jobs := make([]Job, 50)
	for i := range jobs {
		jobs[i] = job(fmt.Sprintf("t%02d", i), &fakeChecker{
			delay: 20 * time.Millisecond, status: checks.StatusOK, running: &running, peak: &peak,
		})
	}
	start := time.Now()
	results := RunOnce(context.Background(), jobs, 5, nil)
	elapsed := time.Since(start)

	if p := peak.Load(); p > 5 {
		t.Fatalf("peak concurrency %d exceeds limit 5", p)
	}
	if p := peak.Load(); p < 2 {
		t.Fatalf("peak concurrency %d: jobs did not run in parallel", p)
	}
	// 50 jobs × 20ms / 5 workers ≈ 200ms; sequential would be 1s.
	if elapsed > 700*time.Millisecond {
		t.Fatalf("took %s, parallelism not effective", elapsed)
	}
	for i, r := range results {
		if r.Target != jobs[i].Target {
			t.Fatalf("results out of order at %d: %s != %s", i, r.Target, jobs[i].Target)
		}
	}
}

func TestRunOnceCallback(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	jobs := []Job{job("a", &fakeChecker{status: checks.StatusOK}), job("b", &fakeChecker{status: checks.StatusWarn})}
	RunOnce(context.Background(), jobs, 2, func(r checks.Result) {
		mu.Lock()
		seen[r.Target] = true
		mu.Unlock()
	})
	if !seen["a"] || !seen["b"] {
		t.Fatalf("callback not invoked for every job: %v", seen)
	}
}

func TestExecuteTimeout(t *testing.T) {
	j := job("slow", &fakeChecker{delay: time.Second, status: checks.StatusOK})
	j.Timeout = 30 * time.Millisecond
	res := Execute(context.Background(), j)
	if res.Status != checks.StatusFail || !strings.Contains(res.Summary, "timed out") {
		t.Fatalf("want timeout failure, got %s %q", res.Status, res.Summary)
	}
	if res.Target != "slow" || res.StartedAt.IsZero() {
		t.Fatal("result not stamped with job identity")
	}
}

func TestExecuteRecoversPanic(t *testing.T) {
	res := Execute(context.Background(), job("p", &fakeChecker{panics: true}))
	if res.Status != checks.StatusFail || !strings.Contains(res.Error, "panic") {
		t.Fatalf("panic not converted into failure: %+v", res)
	}
	if res.Target != "p" || res.Kind != checks.KindTCP {
		t.Fatalf("identity lost after panic: %+v", res)
	}
}

func TestSchedulerRunsRepeatedlyAndBecomesReady(t *testing.T) {
	fc := &fakeChecker{status: checks.StatusOK}
	j := job("loop", fc)
	j.Interval = 1 * time.Second
	var observed atomic.Int64
	s := NewScheduler([]Job{j}, 1, nil, SinkFunc(func(checks.Result) { observed.Add(1) }))
	if s.Ready() {
		t.Fatal("scheduler must not be ready before the first run")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	deadline := time.Now().Add(3 * time.Second)
	for !s.Ready() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.Ready() {
		t.Fatal("scheduler never became ready")
	}
	s.TriggerAll() // forces an immediate extra run
	for observed.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if observed.Load() < 2 {
		t.Fatalf("expected at least 2 runs, got %d", observed.Load())
	}
}

func TestBuildJobs(t *testing.T) {
	cfg, err := config.FromHosts([]string{"example.com", "db.example.com:5432"}, config.QuickOptions{})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := BuildJobs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// example.com: dns, http, tls, tcp; db: dns, tcp
	if len(jobs) != 6 {
		t.Fatalf("got %d jobs, want 6", len(jobs))
	}
	if jobs[0].ID() != "example.com/dns" {
		t.Fatalf("unexpected first job %s", jobs[0].ID())
	}

	cfg.Targets[0].TLS.CAFile = "/does/not/exist.pem"
	if _, err := BuildJobs(cfg); err == nil || !strings.Contains(err.Error(), "ca_file") {
		t.Fatalf("expected ca_file error, got %v", err)
	}
}
