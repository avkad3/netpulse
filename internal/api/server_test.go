package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/avkad3/netpulse/internal/config"
	"github.com/avkad3/netpulse/internal/hub"
	"github.com/avkad3/netpulse/internal/probe"
	"github.com/avkad3/netpulse/internal/store"
)

type fakeSched struct {
	targets []config.Target
	srv     *Server
}

func (f *fakeSched) Targets() []config.Target { return f.targets }
func (f *fakeSched) Has(n string) bool {
	for _, t := range f.targets {
		if t.Name == n {
			return true
		}
	}
	return false
}
func (f *fakeSched) ProbeNow(_ context.Context, n string) (probe.Result, error) {
	r := probe.Result{Target: n, Type: "tcp", Time: time.Now(), Success: true, LatencyMs: 12}
	f.srv.HandleResult(r)
	return r, nil
}

func setup(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fs := &fakeSched{targets: []config.Target{
		{Name: "a", Type: "tcp", Address: "x:1"},
		{Name: "b", Type: "dns", Host: "example.com"},
	}}
	srv := New(st, fs, hub.New(log), fstest.MapFS{"index.html": {Data: []byte("<h1>hi</h1>")}}, log)
	fs.srv = srv
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv
}

func getJSON(t *testing.T, url string, want int, v any) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("GET %s: status %d (want %d): %s", url, res.StatusCode, want, b)
	}
	if v != nil {
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTargetsSnapshotAndWeather(t *testing.T) {
	ts, srv := setup(t)
	for i := range 5 {
		srv.HandleResult(probe.Result{Target: "a", Type: "tcp", Time: time.Now(),
			Success: true, LatencyMs: float64(10 + i)})
	}

	var snap Snapshot
	getJSON(t, ts.URL+"/api/targets", 200, &snap)
	if len(snap.Targets) != 2 {
		t.Fatalf("targets = %d", len(snap.Targets))
	}
	a := snap.Targets[0]
	if a.Latest == nil || a.Stats.Count != 5 || a.Weather.Condition != "sunny" {
		t.Fatalf("target a = %+v", a)
	}
	if snap.Targets[1].Weather.Condition != "unknown" {
		t.Fatalf("target b should be unknown, got %s", snap.Targets[1].Weather.Condition)
	}
	if snap.Overall != "sunny" {
		t.Fatalf("overall = %s", snap.Overall)
	}

	var w map[string]any
	getJSON(t, ts.URL+"/api/weather", 200, &w)
	if w["overall"] != "sunny" {
		t.Fatalf("weather = %v", w)
	}
}

func TestResultsStatsHistoryEndpoints(t *testing.T) {
	ts, srv := setup(t)
	srv.HandleResult(probe.Result{Target: "a", Type: "tcp", Time: time.Now(), Success: true, LatencyMs: 20})
	srv.HandleResult(probe.Result{Target: "a", Type: "tcp", Time: time.Now(), Success: false, Error: "refused"})

	var results []probe.Result
	getJSON(t, ts.URL+"/api/targets/a/results?window=1h&limit=10", 200, &results)
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}

	var stats struct {
		Stats store.Stats `json:"stats"`
	}
	getJSON(t, ts.URL+"/api/targets/a/stats?window=1h", 200, &stats)
	if stats.Stats.UptimePct != 50 {
		t.Fatalf("uptime = %v", stats.Stats.UptimePct)
	}

	var hist struct {
		Buckets []store.Bucket `json:"buckets"`
	}
	getJSON(t, ts.URL+"/api/targets/a/history?window=1h&bucket=1m", 200, &hist)
	if len(hist.Buckets) == 0 {
		t.Fatal("no history buckets")
	}
}

func TestValidationAndNotFound(t *testing.T) {
	ts, _ := setup(t)
	getJSON(t, ts.URL+"/api/targets/nope", 404, nil)
	getJSON(t, ts.URL+"/api/targets/a/results?window=banana", 400, nil)
	getJSON(t, ts.URL+"/api/targets/a/results?limit=0", 400, nil)
	getJSON(t, ts.URL+"/api/targets/a/history?window=720h&bucket=10s", 400, nil)
	getJSON(t, ts.URL+"/api/health", 200, nil)

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "hi") {
		t.Fatalf("dashboard not served: %s", b)
	}
}

func TestWebSocketSnapshotThenUpdate(t *testing.T) {
	ts, _ := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	read := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	if m := read(); m["type"] != "snapshot" {
		t.Fatalf("first message = %v", m["type"])
	}

	// Trigger a probe through the API; the client should receive an update.
	res, err := http.Post(ts.URL+"/api/targets/a/probe", "", nil)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("probe now: %v %v", res, err)
	}
	res.Body.Close()
	m := read()
	if m["type"] != "update" {
		t.Fatalf("second message = %v", m["type"])
	}
	data := m["data"].(map[string]any)
	if data["result"].(map[string]any)["target"] != "a" {
		t.Fatalf("update for wrong target: %v", data)
	}
}
