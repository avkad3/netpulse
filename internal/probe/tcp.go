package probe

import (
	"context"
	"net"
	"time"

	"github.com/avkad3/netpulse/internal/config"
)

// TCP measures connection-establishment latency to host:port. It is a
// privilege-free stand-in for ICMP ping: one TCP handshake is one round trip.
type TCP struct {
	target config.Target
}

func NewTCP(t config.Target) *TCP { return &TCP{target: t} }

func (p *TCP) Probe(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, p.target.Timeout.Duration)
	defer cancel()

	start := time.Now()
	r := newResult(p.target, start)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", p.target.Address)
	if err != nil {
		return fail(r, start, err)
	}
	r.LatencyMs = ms(time.Since(start))
	r.ConnectMs = r.LatencyMs
	r.Detail = conn.RemoteAddr().String()
	_ = conn.Close()
	r.Success = true
	return r
}
