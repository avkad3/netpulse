// Package hub fans live updates out to WebSocket clients.
package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	sendBuffer   = 64
	writeTimeout = 5 * time.Second
	pingInterval = 30 * time.Second
)

// Message is the envelope sent to dashboard clients.
type Message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type client struct {
	send chan []byte
}

// Hub tracks connected clients and broadcasts messages to all of them.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
	log     *slog.Logger
}

func New(log *slog.Logger) *Hub {
	return &Hub{clients: map[*client]struct{}{}, log: log}
}

// Clients returns the number of connected clients.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Broadcast sends msg to every client. Clients too slow to keep up (full
// buffer) are disconnected instead of blocking everyone else.
func (h *Hub) Broadcast(msg Message) {
	b, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("marshal broadcast", "err", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- b:
		default:
			delete(h.clients, c)
			close(c.send)
		}
	}
}

// ServeWS upgrades the request and streams messages until the client leaves.
// snapshot, if non-nil, is sent first so a new client has the full picture.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, snapshot func() (Message, error)) {
	conn, err := websocket.Accept(w, r, nil) // same-origin only by default
	if err != nil {
		return
	}
	defer conn.CloseNow()

	// We never expect client messages; CloseRead handles control frames and
	// cancels ctx when the client disconnects.
	ctx := conn.CloseRead(r.Context())

	c := &client{send: make(chan []byte, sendBuffer)}
	if snapshot != nil {
		msg, err := snapshot()
		if err != nil {
			h.log.Error("build snapshot", "err", err)
			conn.Close(websocket.StatusInternalError, "snapshot failed")
			return
		}
		b, _ := json.Marshal(msg)
		if err := write(ctx, conn, b); err != nil {
			return
		}
	}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if _, ok := h.clients[c]; ok {
			delete(h.clients, c)
			close(c.send)
		}
		h.mu.Unlock()
	}()

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-c.send:
			if !ok {
				conn.Close(websocket.StatusPolicyViolation, "client too slow")
				return
			}
			if err := write(ctx, conn, b); err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func write(ctx context.Context, conn *websocket.Conn, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}
