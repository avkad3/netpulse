// Package store persists probe results in SQLite and answers history queries.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)

	"github.com/avkad3/netpulse/internal/probe"
)

const schema = `
CREATE TABLE IF NOT EXISTS results (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	target      TEXT    NOT NULL,
	type        TEXT    NOT NULL,
	ts          INTEGER NOT NULL, -- unix milliseconds, UTC
	success     INTEGER NOT NULL,
	latency_ms  REAL    NOT NULL,
	dns_ms      REAL    NOT NULL DEFAULT 0,
	connect_ms  REAL    NOT NULL DEFAULT 0,
	tls_ms      REAL    NOT NULL DEFAULT 0,
	ttfb_ms     REAL    NOT NULL DEFAULT 0,
	status_code INTEGER NOT NULL DEFAULT 0,
	error       TEXT    NOT NULL DEFAULT '',
	detail      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_results_target_ts ON results(target, ts);
CREATE INDEX IF NOT EXISTS idx_results_ts ON results(ts);
`

// Store is a SQLite-backed result store. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open creates or opens the database at path (":memory:" works for tests).
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection avoids SQLITE_BUSY and
	// keeps an in-memory database alive across calls.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Insert saves one probe result.
func (s *Store) Insert(ctx context.Context, r probe.Result) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO results (target, type, ts, success, latency_ms, dns_ms, connect_ms,
		                     tls_ms, ttfb_ms, status_code, error, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Target, r.Type, r.Time.UnixMilli(), r.Success, r.LatencyMs, r.DNSMs, r.ConnectMs,
		r.TLSMs, r.TTFBMs, r.StatusCode, r.Error, r.Detail)
	return err
}

// Recent returns results for a target newer than since, newest first.
func (s *Store) Recent(ctx context.Context, target string, since time.Time, limit int) ([]probe.Result, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target, type, ts, success, latency_ms, dns_ms, connect_ms, tls_ms, ttfb_ms,
		       status_code, error, detail
		FROM results WHERE target = ? AND ts >= ? ORDER BY ts DESC LIMIT ?`,
		target, since.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []probe.Result{}
	for rows.Next() {
		var r probe.Result
		var ts int64
		if err := rows.Scan(&r.Target, &r.Type, &ts, &r.Success, &r.LatencyMs, &r.DNSMs,
			&r.ConnectMs, &r.TLSMs, &r.TTFBMs, &r.StatusCode, &r.Error, &r.Detail); err != nil {
			return nil, err
		}
		r.Time = time.UnixMilli(ts).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// Latest returns the most recent result for every target that has one.
func (s *Store) Latest(ctx context.Context) (map[string]probe.Result, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.target, r.type, r.ts, r.success, r.latency_ms, r.dns_ms, r.connect_ms,
		       r.tls_ms, r.ttfb_ms, r.status_code, r.error, r.detail
		FROM results r
		JOIN (SELECT target, MAX(ts) AS ts FROM results GROUP BY target) m
		  ON r.target = m.target AND r.ts = m.ts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]probe.Result{}
	for rows.Next() {
		var r probe.Result
		var ts int64
		if err := rows.Scan(&r.Target, &r.Type, &ts, &r.Success, &r.LatencyMs, &r.DNSMs,
			&r.ConnectMs, &r.TLSMs, &r.TTFBMs, &r.StatusCode, &r.Error, &r.Detail); err != nil {
			return nil, err
		}
		r.Time = time.UnixMilli(ts).UTC()
		out[r.Target] = r
	}
	return out, rows.Err()
}

// Stats summarises a target's results over a window.
type Stats struct {
	Target    string  `json:"target"`
	Window    string  `json:"window"`
	Count     int     `json:"count"`
	Failures  int     `json:"failures"`
	UptimePct float64 `json:"uptime_pct"`
	AvgMs     float64 `json:"avg_ms"`
	MinMs     float64 `json:"min_ms"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	MaxMs     float64 `json:"max_ms"`
	JitterMs  float64 `json:"jitter_ms"` // mean absolute difference between consecutive successes
}

// Stats computes availability and latency percentiles for target since now-window.
// Latency figures only include successful probes.
func (s *Store) Stats(ctx context.Context, target string, window time.Duration) (Stats, error) {
	st := Stats{Target: target, Window: window.String()}
	rows, err := s.db.QueryContext(ctx, `
		SELECT success, latency_ms FROM results
		WHERE target = ? AND ts >= ? ORDER BY ts`,
		target, time.Now().Add(-window).UnixMilli())
	if err != nil {
		return st, err
	}
	defer rows.Close()

	var lat []float64
	for rows.Next() {
		var ok bool
		var ms float64
		if err := rows.Scan(&ok, &ms); err != nil {
			return st, err
		}
		st.Count++
		if !ok {
			st.Failures++
			continue
		}
		lat = append(lat, ms)
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	if st.Count > 0 {
		st.UptimePct = round(100 * float64(st.Count-st.Failures) / float64(st.Count))
	}
	if len(lat) == 0 {
		return st, nil
	}

	var sum, jitter float64
	for i, v := range lat {
		sum += v
		if i > 0 {
			jitter += math.Abs(v - lat[i-1])
		}
	}
	st.AvgMs = round(sum / float64(len(lat)))
	if len(lat) > 1 {
		st.JitterMs = round(jitter / float64(len(lat)-1))
	}
	sorted := append([]float64(nil), lat...)
	sort.Float64s(sorted)
	st.MinMs = round(sorted[0])
	st.MaxMs = round(sorted[len(sorted)-1])
	st.P50Ms = round(percentile(sorted, 50))
	st.P95Ms = round(percentile(sorted, 95))
	st.P99Ms = round(percentile(sorted, 99))
	return st, nil
}

// Bucket is one time slice of a target's history.
type Bucket struct {
	Start    time.Time `json:"start"`
	Count    int       `json:"count"`
	Failures int       `json:"failures"`
	AvgMs    float64   `json:"avg_ms"`
	MaxMs    float64   `json:"max_ms"`
}

// History groups a target's results into fixed-width time buckets for charts.
func (s *Store) History(ctx context.Context, target string, window, bucket time.Duration) ([]Bucket, error) {
	if bucket <= 0 {
		return nil, fmt.Errorf("bucket must be positive")
	}
	bms := bucket.Milliseconds()
	rows, err := s.db.QueryContext(ctx, `
		SELECT (ts / ?) * ? AS b,
		       COUNT(*),
		       SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END),
		       COALESCE(AVG(CASE WHEN success = 1 THEN latency_ms END), 0),
		       COALESCE(MAX(CASE WHEN success = 1 THEN latency_ms END), 0)
		FROM results WHERE target = ? AND ts >= ?
		GROUP BY b ORDER BY b`,
		bms, bms, target, time.Now().Add(-window).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bucket{}
	for rows.Next() {
		var b Bucket
		var start int64
		if err := rows.Scan(&start, &b.Count, &b.Failures, &b.AvgMs, &b.MaxMs); err != nil {
			return nil, err
		}
		b.Start = time.UnixMilli(start).UTC()
		b.AvgMs, b.MaxMs = round(b.AvgMs), round(b.MaxMs)
		out = append(out, b)
	}
	return out, rows.Err()
}

// Prune deletes results older than before and returns how many were removed.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM results WHERE ts < ?`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// percentile uses linear interpolation between closest ranks on sorted data.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(rank-float64(lo))
}

func round(v float64) float64 { return math.Round(v*100) / 100 }
