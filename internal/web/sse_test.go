package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/controller"
)

type nonFlusherResponseWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

type streamingResponseWriter struct {
	mu     sync.Mutex
	header http.Header
	status int
	body   strings.Builder
}

func (w *nonFlusherResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *nonFlusherResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *nonFlusherResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *streamingResponseWriter) Header() http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *streamingResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *streamingResponseWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	w.status = statusCode
	w.mu.Unlock()
}

func (w *streamingResponseWriter) Flush() {}

func (w *streamingResponseWriter) BodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func Test_Hub_subscribeBroadcastReceive(t *testing.T) {
	hub := NewHub(nil)
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	snap := controller.StateSnapshot{State: controller.StateCharging}
	hub.Broadcast(snap)

	received := <-ch
	if received.State != controller.StateCharging {
		t.Errorf("State = %q, want %q", received.State, controller.StateCharging)
	}
}

func Test_Hub_multipleSubscribers(t *testing.T) {
	hub := NewHub(nil)
	ch1 := hub.Subscribe()
	ch2 := hub.Subscribe()
	ch3 := hub.Subscribe()
	defer hub.Unsubscribe(ch1)
	defer hub.Unsubscribe(ch2)
	defer hub.Unsubscribe(ch3)

	snap := controller.StateSnapshot{State: controller.StateIdle}
	hub.Broadcast(snap)

	for i, ch := range []chan controller.StateSnapshot{ch1, ch2, ch3} {
		received := <-ch
		if received.State != controller.StateIdle {
			t.Errorf("subscriber %d: State = %q, want %q", i, received.State, controller.StateIdle)
		}
	}
}

func Test_Hub_unsubscribe(t *testing.T) {
	hub := NewHub(nil)
	ch := hub.Subscribe()
	hub.Unsubscribe(ch)

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}

	// Broadcast after unsubscribe should not panic.
	hub.Broadcast(controller.StateSnapshot{})
}

func Test_Hub_slowClientDropsEvent(t *testing.T) {
	hub := NewHub(nil)
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	// Fill the channel buffer (cap=10).
	for i := 0; i < 15; i++ {
		hub.Broadcast(controller.StateSnapshot{})
	}

	// Should not deadlock. Drain what we can.
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count < 1 {
		t.Error("expected at least 1 message received")
	}
	if count > 10 {
		t.Errorf("received %d messages, expected at most 10 (buffer size)", count)
	}
}

func Test_Hub_serveHTTP_streamsEvents(t *testing.T) {
	hub := NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	w := &streamingResponseWriter{}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		hub.ServeHTTP(w, req)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		hub.mu.RLock()
		count := len(hub.clients)
		hub.mu.RUnlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for SSE subscription")
		}
		time.Sleep(10 * time.Millisecond)
	}

	hub.Broadcast(controller.StateSnapshot{State: controller.StateCharging})
	deadline = time.Now().Add(time.Second)
	for !strings.Contains(w.BodyString(), "charging") {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for SSE body, got %q", w.BodyString())
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if !strings.Contains(w.BodyString(), "data: ") {
		t.Fatalf("expected SSE data frame, got %q", w.BodyString())
	}
}

func Test_Hub_serveHTTP_streamingNotSupported(t *testing.T) {
	hub := NewHub(nil)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	w := &nonFlusherResponseWriter{}

	hub.ServeHTTP(w, req)

	if w.status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.status)
	}
	if !strings.Contains(w.body.String(), "streaming not supported") {
		t.Fatalf("unexpected body: %q", w.body.String())
	}
}
