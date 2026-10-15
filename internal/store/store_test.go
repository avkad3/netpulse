package store

import (
	"context"
	"testing"
	"time"

	"github.com/avkad3/netpulse/internal/probe"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func add(t *testing.T, s *Store, target string, at time.Time, ok bool, ms float64) {
	t.Helper()
	r := probe.Result{Target: target, Type: "tcp", Time: at, Success: ok, LatencyMs: ms}
	if !ok {
		r.Error = "boom"
	}
	if err := s.Insert(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestStatsPercentilesAndUptime(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	for i := 1; i <= 10; i++ { // 10, 20, ..., 100 ms
		add(t, s, "a", now.Add(-time.Duration(11-i)*time.Second), true, float64(i*10))
	}
	add(t, s, "a", now, false, 5000)
	add(t, s, "a", now.Add(-2*time.Hour), true, 999) // outside window

	st, err := s.Stats(context.Background(), "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Count != 11 || st.Failures != 1 {
		t.Fatalf("count/failures = %d/%d", st.Count, st.Failures)
	}
	if st.UptimePct != 90.91 {
		t.Errorf("uptime = %v", st.UptimePct)
	}
	if st.MinMs != 10 || st.MaxMs != 100 || st.AvgMs != 55 || st.P50Ms != 55 {
		t.Errorf("latency stats wrong: %+v", st)
	}
	if st.P95Ms != 95.5 {
		t.Errorf("p95 = %v", st.P95Ms)
	}
	if st.JitterMs != 10 {
		t.Errorf("jitter = %v", st.JitterMs)
	}
}

func TestStatsEmpty(t *testing.T) {
	st, err := openTest(t).Stats(context.Background(), "nobody", time.Hour)
	if err != nil || st.Count != 0 || st.UptimePct != 0 {
		t.Fatalf("got %+v, %v", st, err)
	}
}

func TestRecentLatestHistoryPrune(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Now().Truncate(time.Minute).Add(-10 * time.Minute)
	add(t, s, "a", base, true, 10)
	add(t, s, "a", base.Add(10*time.Second), false, 0)
	add(t, s, "a", base.Add(70*time.Second), true, 30)
	add(t, s, "b", base.Add(5*time.Second), true, 7)

	recent, err := s.Recent(ctx, "a", base.Add(-time.Hour), 2)
	if err != nil || len(recent) != 2 || recent[0].LatencyMs != 30 {
		t.Fatalf("recent = %+v, %v", recent, err)
	}

	latest, err := s.Latest(ctx)
	if err != nil || len(latest) != 2 || latest["a"].LatencyMs != 30 || latest["b"].LatencyMs != 7 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}

	hist, err := s.History(ctx, "a", time.Hour, time.Minute)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	if hist[0].Count != 2 || hist[0].Failures != 1 || hist[0].AvgMs != 10 {
		t.Errorf("first bucket = %+v", hist[0])
	}

	n, err := s.Prune(ctx, base.Add(30*time.Second))
	if err != nil || n != 3 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}
