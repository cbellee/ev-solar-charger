package web

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/controller"
)

// Hub manages SSE client connections and broadcasts state updates.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan controller.StateSnapshot]struct{}
	logger  *slog.Logger
}

// NewHub creates a new SSE hub.
func NewHub(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		clients: make(map[chan controller.StateSnapshot]struct{}),
		logger:  logger,
	}
}

// Subscribe registers a new SSE client.
func (h *Hub) Subscribe() chan controller.StateSnapshot {
	ch := make(chan controller.StateSnapshot, 10)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes an SSE client.
func (h *Hub) Unsubscribe(ch chan controller.StateSnapshot) {
	h.mu.Lock()
	delete(h.clients, ch)
	close(ch)
	h.mu.Unlock()
}

// Broadcast sends a state snapshot to all connected clients.
func (h *Hub) Broadcast(snap controller.StateSnapshot) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- snap:
		default:
			// Slow client, drop event.
		}
	}
}

// ServeHTTP handles SSE connections.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.serveHTTP(w, r, 15*time.Second)
}

func (h *Hub) serveHTTP(w http.ResponseWriter, r *http.Request, heartbeat time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch := h.Subscribe()
	defer h.Unsubscribe(ch)
	write := func(frame string) bool {
		if _, err := fmt.Fprint(w, frame); err != nil {
			h.logger.WarnContext(r.Context(), "SSE write failed", "error", err)
			return false
		}
		flusher.Flush()
		return true
	}
	if !write(": connected\n\n") {
		return
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !write(": heartbeat\n\n") {
				return
			}
		case snap, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(snap)
			if err != nil {
				h.logger.ErrorContext(r.Context(), "SSE snapshot encoding failed", "error", err)
				continue
			}
			if !write(fmt.Sprintf("data: %s\n\n", data)) {
				return
			}
		}
	}
}
