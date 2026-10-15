package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/avkad3/netpulse/internal/config"
	"github.com/avkad3/netpulse/internal/probe"
)

type fakeProber struct {
	name    string
	delay   time.Duration
	running atomic.Int32
	maxSeen atomic.Int32
}

func (f *fakeProber) Probe(ctx context.Context) probe.Result {
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		old := f.maxSeen.Load()
		if n <= old || f.maxSeen.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
	}
	return probe.Result{Target: f.name, Success: true, Time: time.Now()}
}

func newTest(t *testing.T, n, workers int, interval, delay time.Duration,
	handle Handler) (*Scheduler, []*fakeProber) {
	t.Helper()
	var targets []config.Target
	for i := range n {
		targets = append(targets, config.Target{
			Name: string(rune('a' + i)), Type: config.TypeTCP, Address: "x:1",
			Interval: config.Duration{Duration: interval},
			Timeout:  config.Duration{Duration: interval},
		})
	}
	s, err := New(targets, workers, handle)
	if err != nil {
		t.Fatal(err)
	}
	s.maxDelay = time.Millisecond
	fakes := make([]*fakeProber, n)
	for i, tg := range targets {
		fakes[i] = &fakeProber{name: tg.Name, delay: delay}
		s.entries[tg.Name].prober = fakes[i]
	}
	return s, fakes
}

func TestRunsEveryTargetRepeatedly(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	s, _ := newTest(t, 3, 2, 20*time.Millisecond, 0, func(r probe.Result) {
		mu.Lock()
		counts[r.Target]++
		mu.Unlock()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"a", "b", "c"} {
		if counts[name] < 3 {
			t.Errorf("target %s probed %d times, want >= 3", name, counts[name])
		}
	}
}

func TestWorkerPoolBoundsConcurrency(t *testing.T) {
	s, fakes := newTest(t, 6, 2, 10*time.Millisecond, 30*time.Millisecond, nil)
	var shared atomic.Int32
	var peak atomic.Int32
	for _, f := range fakes {
		s.entries[f.name].prober = proberFunc(func(ctx context.Context) probe.Result {
			n := shared.Add(1)
			defer shared.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			return probe.Result{}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrency %d exceeds 2 workers", p)
	}
}

func TestSlowTargetDoesNotPileUp(t *testing.T) {
	// Interval 5ms but each probe takes 50ms: overlapping runs must be skipped.
	s, fakes := newTest(t, 1, 4, 5*time.Millisecond, 50*time.Millisecond, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if m := fakes[0].maxSeen.Load(); m != 1 {
		t.Fatalf("same target ran %d probes concurrently", m)
	}
}

func TestProbeNow(t *testing.T) {
	got := make(chan probe.Result, 10)
	s, _ := newTest(t, 1, 1, time.Hour, 0, func(r probe.Result) { got <- r })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	r, err := s.ProbeNow(ctx, "a")
	if err != nil || r.Target != "a" {
		t.Fatalf("ProbeNow = %+v, %v", r, err)
	}
	if _, err := s.ProbeNow(ctx, "missing"); err == nil {
		t.Fatal("expected error for unknown target")
	}
}

type proberFunc func(context.Context) probe.Result

func (f proberFunc) Probe(ctx context.Context) probe.Result { return f(ctx) }
