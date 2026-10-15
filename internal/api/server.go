// Package api exposes NetPulse over HTTP: a REST API, a WebSocket feed and
// the embedded dashboard.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/avkad3/netpulse/internal/config"
	"github.com/avkad3/netpulse/internal/hub"
	"github.com/avkad3/netpulse/internal/probe"
	"github.com/avkad3/netpulse/internal/store"
	"github.com/avkad3/netpulse/internal/weather"
)

const (
	// StatsWindow is the look-back used for live weather and headline stats.
	StatsWindow = 15 * time.Minute
	maxWindow   = 30 * 24 * time.Hour
	maxLimit    = 5000
)

// Prober is the part of the scheduler the API needs.
type Prober interface {
	Targets() []config.Target
	Has(name string) bool
	ProbeNow(ctx context.Context, name string) (probe.Result, error)
}

// Server wires the store, scheduler and hub to HTTP handlers.
type Server struct {
	store   *store.Store
	sched   Prober
	hub     *hub.Hub
	static  fs.FS
	log     *slog.Logger
	started time.Time
	weather weather.Thresholds
}

func New(st *store.Store, sched Prober, h *hub.Hub, static fs.FS, log *slog.Logger) *Server {
	return &Server{store: st, sched: sched, hub: h, static: static, log: log,
		started: time.Now(), weather: weather.Default}
}

// TargetStatus is everything the dashboard shows for one target.
type TargetStatus struct {
	Target  config.Target  `json:"target"`
	Latest  *probe.Result  `json:"latest,omitempty"`
	Stats   store.Stats    `json:"stats"`
	Weather weather.Report `json:"weather"`
}

// Snapshot is the full current picture.
type Snapshot struct {
	Overall weather.Condition `json:"overall"`
	Targets []TargetStatus    `json:"targets"`
	Time    time.Time         `json:"time"`
}

// Update is pushed to WebSocket clients after every probe.
type Update struct {
	Result  probe.Result      `json:"result"`
	Stats   store.Stats       `json:"stats"`
	Weather weather.Report    `json:"weather"`
	Overall weather.Condition `json:"overall"`
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/weather", s.weatherReport)
	mux.HandleFunc("GET /api/targets", s.targets)
	mux.HandleFunc("GET /api/targets/{name}", s.target)
	mux.HandleFunc("GET /api/targets/{name}/results", s.results)
	mux.HandleFunc("GET /api/targets/{name}/stats", s.stats)
	mux.HandleFunc("GET /api/targets/{name}/history", s.history)
	mux.HandleFunc("POST /api/targets/{name}/probe", s.probeNow)
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		s.hub.ServeWS(w, r, func() (hub.Message, error) {
			snap, err := s.snapshot(r.Context())
			return hub.Message{Type: "snapshot", Data: snap}, err
		})
	})
	if s.static != nil {
		mux.Handle("GET /", http.FileServerFS(s.static))
	}
	return logRequests(s.log, mux)
}

// HandleResult stores a probe result and pushes a live update. It is the
// scheduler's result handler.
func (s *Server) HandleResult(r probe.Result) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.Insert(ctx, r); err != nil {
		s.log.Error("store result", "target", r.Target, "err", err)
		return
	}
	if !r.Success {
		s.log.Warn("probe failed", "target", r.Target, "err", r.Error, "ms", r.LatencyMs)
	}
	if s.hub.Clients() == 0 {
		return
	}
	st, err := s.store.Stats(ctx, r.Target, StatsWindow)
	if err != nil {
		s.log.Error("stats", "target", r.Target, "err", err)
		return
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		s.log.Error("snapshot", "err", err)
		return
	}
	s.hub.Broadcast(hub.Message{Type: "update", Data: Update{
		Result: r, Stats: st, Weather: s.weather.Classify(st), Overall: snap.Overall,
	}})
}

func (s *Server) snapshot(ctx context.Context) (Snapshot, error) {
	latest, err := s.store.Latest(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Time: time.Now().UTC()}
	reports := []weather.Report{}
	for _, t := range s.sched.Targets() {
		st, err := s.store.Stats(ctx, t.Name, StatsWindow)
		if err != nil {
			return Snapshot{}, err
		}
		ts := TargetStatus{Target: t, Stats: st, Weather: s.weather.Classify(st)}
		if r, ok := latest[t.Name]; ok {
			ts.Latest = &r
		}
		reports = append(reports, ts.Weather)
		snap.Targets = append(snap.Targets, ts)
	}
	snap.Overall = weather.Overall(reports)
	return snap, nil
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"uptime_s":   int(time.Since(s.started).Seconds()),
		"targets":    len(s.sched.Targets()),
		"ws_clients": s.hub.Clients(),
	})
}

func (s *Server) weatherReport(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	reports := make([]weather.Report, len(snap.Targets))
	for i, t := range snap.Targets {
		reports[i] = t.Weather
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"overall": snap.Overall, "window": StatsWindow.String(), "targets": reports,
	})
}

func (s *Server) targets(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) target(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	snap, err := s.snapshot(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, t := range snap.Targets {
		if t.Target.Name == name {
			writeJSON(w, http.StatusOK, t)
			return
		}
	}
}

func (s *Server) results(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	window, ok := duration(w, r, "window", time.Hour, time.Second, maxWindow)
	if !ok {
		return
	}
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			writeError(w, http.StatusBadRequest, "limit must be 1-5000")
			return
		}
		limit = n
	}
	res, err := s.store.Recent(r.Context(), name, time.Now().Add(-window), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	window, ok := duration(w, r, "window", time.Hour, time.Minute, maxWindow)
	if !ok {
		return
	}
	st, err := s.store.Stats(r.Context(), name, window)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": st, "weather": s.weather.Classify(st)})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	window, ok := duration(w, r, "window", time.Hour, time.Minute, maxWindow)
	if !ok {
		return
	}
	bucket, ok := duration(w, r, "bucket", max(window/60, time.Minute), 10*time.Second, window)
	if !ok {
		return
	}
	if window/bucket > 2000 {
		writeError(w, http.StatusBadRequest, "too many buckets: increase bucket")
		return
	}
	h, err := s.store.History(r.Context(), name, window, bucket)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"target": name, "window": window.String(), "bucket": bucket.String(), "buckets": h,
	})
}

func (s *Server) probeNow(w http.ResponseWriter, r *http.Request) {
	name, ok := s.name(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	res, err := s.sched.ProbeNow(ctx, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// name extracts {name} and 404s if the target is unknown.
func (s *Server) name(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if !s.sched.Has(name) {
		writeError(w, http.StatusNotFound, "unknown target "+strconv.Quote(name))
		return "", false
	}
	return name, true
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusServiceUnavailable, "request cancelled or timed out")
		return
	}
	s.log.Error("api error", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func duration(w http.ResponseWriter, r *http.Request, key string, def, lo, hi time.Duration) (time.Duration, bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, true
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		writeError(w, http.StatusBadRequest, key+" must be a duration between "+lo.String()+" and "+hi.String())
		return 0, false
	}
	return d, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController (used by the WebSocket upgrade) reach
// the underlying writer's Hijack.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/ws" {
			log.Debug("http", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "dur", time.Since(start))
		}
	})
}
