package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/avkad3/netpulse/internal/config"
)

const maxBody = 1 << 20 // read at most 1 MiB of each response

// HTTP fetches a URL and breaks the request down into DNS, connect, TLS and
// time-to-first-byte phases. Keep-alives are disabled so every probe measures
// a fresh connection, like a first-time visitor would see.
type HTTP struct {
	target config.Target
	client *http.Client
}

func NewHTTP(t config.Target) *HTTP {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	return &HTTP{
		target: t,
		client: &http.Client{
			Transport: tr,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("stopped after %d redirects", len(via))
				}
				return nil
			},
		},
	}
}

func (p *HTTP) Probe(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, p.target.Timeout.Duration)
	defer cancel()

	start := time.Now()
	r := newResult(p.target, start)

	// Trace hooks can fire from dialer goroutines (e.g. IPv4/IPv6 racing), so
	// phase timings are guarded and copied into the result at the end.
	var (
		mu                           sync.Mutex
		dnsStart, connStart, tlsSt   time.Time
		dnsMs, connMs, tlsMs, ttfbMs float64
	)
	set := func(dst *float64, v float64) { mu.Lock(); *dst = v; mu.Unlock() }
	mark := func(dst *time.Time) { mu.Lock(); *dst = time.Now(); mu.Unlock() }
	since := func(t *time.Time) (time.Duration, bool) {
		mu.Lock()
		defer mu.Unlock()
		return time.Since(*t), !t.IsZero()
	}
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { mark(&dnsStart) },
		DNSDone: func(httptrace.DNSDoneInfo) {
			if d, ok := since(&dnsStart); ok {
				set(&dnsMs, ms(d))
			}
		},
		ConnectStart: func(string, string) { mark(&connStart) },
		ConnectDone: func(_, _ string, err error) {
			if d, ok := since(&connStart); ok && err == nil {
				set(&connMs, ms(d))
			}
		},
		TLSHandshakeStart: func() { mark(&tlsSt) },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			if d, ok := since(&tlsSt); ok {
				set(&tlsMs, ms(d))
			}
		},
		GotFirstResponseByte: func() { set(&ttfbMs, ms(time.Since(start))) },
	}
	phases := func(res Result) Result {
		mu.Lock()
		defer mu.Unlock()
		res.DNSMs, res.ConnectMs, res.TLSMs, res.TTFBMs = dnsMs, connMs, tlsMs, ttfbMs
		return res
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace),
		http.MethodGet, p.target.URL, nil)
	if err != nil {
		return fail(r, start, err)
	}
	req.Header.Set("User-Agent", "NetPulse/1.0 (+https://github.com/avkad3/netpulse)")

	resp, err := p.client.Do(req)
	if err != nil {
		return phases(fail(r, start, err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))

	r.LatencyMs = ms(time.Since(start))
	r.StatusCode = resp.StatusCode
	r.Detail = resp.Proto
	if resp.StatusCode >= 400 {
		r.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return phases(r)
	}
	r.Success = true
	return phases(r)
}
