// Package probe implements the DNS, HTTP and TCP checks NetPulse runs.
package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/avkad3/netpulse/internal/config"
)

// Result is the outcome of a single probe run.
type Result struct {
	Target     string    `json:"target"`
	Type       string    `json:"type"`
	Time       time.Time `json:"time"`
	Success    bool      `json:"success"`
	LatencyMs  float64   `json:"latency_ms"`
	DNSMs      float64   `json:"dns_ms,omitempty"`
	ConnectMs  float64   `json:"connect_ms,omitempty"`
	TLSMs      float64   `json:"tls_ms,omitempty"`
	TTFBMs     float64   `json:"ttfb_ms,omitempty"`
	StatusCode int       `json:"status_code,omitempty"`
	Error      string    `json:"error,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// Prober runs one kind of check against one target.
type Prober interface {
	Probe(ctx context.Context) Result
}

// New builds the prober for a configured target.
func New(t config.Target) (Prober, error) {
	switch t.Type {
	case config.TypeDNS:
		return NewDNS(t), nil
	case config.TypeHTTP:
		return NewHTTP(t), nil
	case config.TypeTCP:
		return NewTCP(t), nil
	}
	return nil, fmt.Errorf("unknown probe type %q", t.Type)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

func newResult(t config.Target, start time.Time) Result {
	return Result{Target: t.Name, Type: t.Type, Time: start.UTC()}
}

func fail(r Result, start time.Time, err error) Result {
	r.Success = false
	r.LatencyMs = ms(time.Since(start))
	r.Error = err.Error()
	return r
}
