package probe

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/avkad3/netpulse/internal/config"
)

// DNS measures how long it takes to resolve a host name, optionally against a
// specific resolver instead of the system one.
type DNS struct {
	target   config.Target
	resolver *net.Resolver
}

func NewDNS(t config.Target) *DNS {
	r := net.DefaultResolver
	if t.Server != "" {
		server := t.Server
		r = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, server)
			},
		}
	}
	return &DNS{target: t, resolver: r}
}

func (p *DNS) Probe(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, p.target.Timeout.Duration)
	defer cancel()

	start := time.Now()
	r := newResult(p.target, start)
	addrs, err := p.resolver.LookupHost(ctx, p.target.Host)
	if err != nil {
		return fail(r, start, err)
	}
	r.Success = true
	r.LatencyMs = ms(time.Since(start))
	r.DNSMs = r.LatencyMs
	if len(addrs) > 3 {
		addrs = addrs[:3]
	}
	r.Detail = strings.Join(addrs, ", ")
	return r
}
