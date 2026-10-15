package hub

import (
	"io"
	"log/slog"
	"testing"
)

func TestBroadcastDropsSlowClients(t *testing.T) {
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	fast := &client{send: make(chan []byte, sendBuffer)}
	slow := &client{send: make(chan []byte, 1)}
	h.clients[fast] = struct{}{}
	h.clients[slow] = struct{}{}

	h.Broadcast(Message{Type: "x"})
	h.Broadcast(Message{Type: "y"}) // slow's buffer of 1 is now full -> dropped

	if h.Clients() != 1 {
		t.Fatalf("clients = %d, want 1", h.Clients())
	}
	if _, ok := h.clients[fast]; !ok {
		t.Fatal("fast client was dropped")
	}
	if len(fast.send) != 2 {
		t.Fatalf("fast client got %d messages", len(fast.send))
	}
	<-slow.send
	if _, open := <-slow.send; open {
		t.Fatal("slow client's channel should be closed")
	}
}
