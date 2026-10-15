package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/avkad3/netpulse/internal/config"
)

func target(typ string) config.Target {
	return config.Target{
		Name:     "t",
		Type:     typ,
		Interval: config.Duration{Duration: 5 * time.Second},
		Timeout:  config.Duration{Duration: 2 * time.Second},
	}
}

func TestHTTPProbeSuccessRecordsPhases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tg := target(config.TypeHTTP)
	tg.URL = srv.URL
	r := NewHTTP(tg).Probe(context.Background())
	if !r.Success || r.StatusCode != 200 {
		t.Fatalf("want success 200, got %+v", r)
	}
	if r.LatencyMs <= 0 || r.ConnectMs <= 0 || r.TTFBMs <= 0 {
		t.Fatalf("expected positive timings, got %+v", r)
	}
	if r.TTFBMs > r.LatencyMs {
		t.Fatalf("ttfb %.3f > total %.3f", r.TTFBMs, r.LatencyMs)
	}
}

func TestHTTPProbeServerErrorIsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tg := target(config.TypeHTTP)
	tg.URL = srv.URL
	r := NewHTTP(tg).Probe(context.Background())
	if r.Success || r.StatusCode != 503 || r.Error != "HTTP 503" {
		t.Fatalf("want failed 503, got %+v", r)
	}
}

func TestHTTPProbeTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	tg := target(config.TypeHTTP)
	tg.URL = srv.URL
	tg.Timeout.Duration = 100 * time.Millisecond
	r := NewHTTP(tg).Probe(context.Background())
	if r.Success || r.Error == "" {
		t.Fatalf("want timeout failure, got %+v", r)
	}
}

func TestTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	tg := target(config.TypeTCP)
	tg.Address = ln.Addr().String()
	if r := NewTCP(tg).Probe(context.Background()); !r.Success || r.LatencyMs <= 0 {
		t.Fatalf("want success, got %+v", r)
	}

	addr := ln.Addr().String()
	_ = ln.Close() // nothing listening any more -> connection refused
	tg.Address = addr
	if r := NewTCP(tg).Probe(context.Background()); r.Success || r.Error == "" {
		t.Fatalf("want failure on closed port, got %+v", r)
	}
}

func TestDNSProbeLocalhost(t *testing.T) {
	tg := target(config.TypeDNS)
	tg.Host = "localhost"
	r := NewDNS(tg).Probe(context.Background())
	if !r.Success || !strings.Contains(r.Detail, "127.0.0.1") && !strings.Contains(r.Detail, "::1") {
		t.Fatalf("want localhost to resolve, got %+v", r)
	}
}

func TestDNSProbeUnreachableServerFails(t *testing.T) {
	// A UDP socket that never answers: the probe must give up at its timeout.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	tg := target(config.TypeDNS)
	tg.Host = "example.com"
	tg.Server = pc.LocalAddr().String()
	tg.Timeout.Duration = 300 * time.Millisecond
	start := time.Now()
	r := NewDNS(tg).Probe(context.Background())
	if r.Success {
		t.Fatalf("want failure, got %+v", r)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("probe ignored its timeout")
	}
}

func TestNewRejectsUnknownType(t *testing.T) {
	if _, err := New(config.Target{Type: "icmp"}); err == nil {
		t.Fatal("expected error")
	}
}
