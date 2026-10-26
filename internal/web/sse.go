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

// DefaultMaxSSEClients caps concurrent /events streams so a misbehaving or
// compromised client cannot exhaust goroutines and file descriptors.
const DefaultMaxSSEClients = 64

// Hub manages SSE client connections and broadcasts state updates.
type Hub struct {
	mu         sync.RWMutex
	clients    map[chan controller.StateSnapshot]struct{}
	maxClients int
	logger     *slog.Logger
}

// NewHub creates a new SSE hub.
func NewHub(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		clients:    make(map[chan controller.StateSnapshot]struct{}),
		maxClients: DefaultMaxSSEClients,
		logger:     logger,
	}
}

// Subscribe registers a new SSE client.
func (h *Hub) Subscribe() chan controller.StateSnapshot {
	ch, _ := h.subscribe(0)
	return ch
}

// subscribe registers a client unless limit (> 0) clients are already
// connected.
func (h *Hub) subscribe(limit int) (chan controller.StateSnapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if limit > 0 && len(h.clients) >= limit {
		return nil, false
	}
	ch := make(chan controller.StateSnapshot, 10)
	h.clients[ch] = struct{}{}
	return ch, true
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

	ch, ok := h.subscribe(h.maxClients)
	if !ok {
		h.logger.WarnContext(r.Context(), "SSE client limit reached", "limit", h.maxClients)
		w.Header().Set("Retry-After", "30")
		http.Error(w, "too many event streams", http.StatusServiceUnavailable)
		return
	}
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
