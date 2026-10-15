// Package scheduler runs every target's probe on its own interval, using a
// bounded pool of workers so a burst of slow probes can't exhaust resources.
package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/avkad3/netpulse/internal/config"
	"github.com/avkad3/netpulse/internal/probe"
)

// Handler receives every probe result. It is called from worker goroutines
// and must be safe for concurrent use.
type Handler func(probe.Result)

type job struct {
	name   string
	prober probe.Prober
	reply  chan probe.Result // non-nil for on-demand probes
}

type entry struct {
	target   config.Target
	prober   probe.Prober
	inflight atomic.Bool // a scheduled probe is queued or running
}

// Scheduler owns the probe loop.
type Scheduler struct {
	entries  map[string]*entry
	order    []string
	workers  int
	handle   Handler
	jobs     chan job
	maxDelay time.Duration // cap on the random start-up delay
}

// New builds probers for all targets.
func New(targets []config.Target, workers int, handle Handler) (*Scheduler, error) {
	s := &Scheduler{
		entries:  map[string]*entry{},
		workers:  max(workers, 1),
		handle:   handle,
		jobs:     make(chan job, len(targets)+workers),
		maxDelay: 2 * time.Second,
	}
	for _, t := range targets {
		p, err := probe.New(t)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", t.Name, err)
		}
		s.entries[t.Name] = &entry{target: t, prober: p}
		s.order = append(s.order, t.Name)
	}
	return s, nil
}

// Run starts the workers and per-target tickers and blocks until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range s.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.work(ctx)
		}()
	}
	for _, name := range s.order {
		wg.Add(1)
		go func(e *entry) {
			defer wg.Done()
			s.tick(ctx, e)
		}(s.entries[name])
	}
	wg.Wait()
}

// tick enqueues a probe every interval. If the previous probe for this
// target hasn't finished, the tick is skipped rather than piling up.
func (s *Scheduler) tick(ctx context.Context, e *entry) {
	// Spread start-up so targets don't all fire in the same instant.
	delay := time.Duration(rand.Int64N(int64(min(e.target.Interval.Duration, s.maxDelay)) + 1))
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	s.enqueue(ctx, e)

	t := time.NewTicker(e.target.Interval.Duration)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.enqueue(ctx, e)
		}
	}
}

func (s *Scheduler) enqueue(ctx context.Context, e *entry) {
	if !e.inflight.CompareAndSwap(false, true) {
		return
	}
	select {
	case s.jobs <- job{name: e.target.Name, prober: e.prober}:
	case <-ctx.Done():
		e.inflight.Store(false)
	}
}

func (s *Scheduler) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.jobs:
			r := j.prober.Probe(ctx)
			if ctx.Err() != nil {
				return // shutting down: don't record half-cancelled probes
			}
			if j.reply == nil {
				s.entries[j.name].inflight.Store(false)
			}
			if s.handle != nil {
				s.handle(r)
			}
			if j.reply != nil {
				j.reply <- r
			}
		}
	}
}

// ProbeNow runs a target's probe immediately through the worker pool and
// waits for the result. The result is also passed to the handler.
func (s *Scheduler) ProbeNow(ctx context.Context, name string) (probe.Result, error) {
	e, ok := s.entries[name]
	if !ok {
		return probe.Result{}, fmt.Errorf("unknown target %q", name)
	}
	reply := make(chan probe.Result, 1)
	select {
	case s.jobs <- job{name: name, prober: e.prober, reply: reply}:
	case <-ctx.Done():
		return probe.Result{}, ctx.Err()
	}
	select {
	case r := <-reply:
		return r, nil
	case <-ctx.Done():
		return probe.Result{}, ctx.Err()
	}
}

// Targets returns the configured targets in config order.
func (s *Scheduler) Targets() []config.Target {
	out := make([]config.Target, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.entries[n].target)
	}
	return out
}

// Has reports whether a target with this name exists.
func (s *Scheduler) Has(name string) bool { _, ok := s.entries[name]; return ok }
