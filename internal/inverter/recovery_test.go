package inverter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"nhooyr.io/websocket"
)

// fakeWiNet is a TLS WiNet-S stand-in that counts logins and pings and lets a
// test decide, per session, which result_code register reads return.
type fakeWiNet struct {
	server *httptest.Server
	logins atomic.Int32
	pings  atomic.Int32
	// failCode returns the register result_code for a session (0 = success).
	failCode func(f *fakeWiNet, login int32) int
	// pingCode overrides the ping result code when non-zero.
	pingCode atomic.Int32
	// dropSocket closes the WebSocket after the next request when set.
	dropSocket atomic.Bool
}

// supersededOnly mirrors the live module: a newer login invalidates older
// sessions, whose register reads then return result_code 120.
func supersededOnly(f *fakeWiNet, login int32) int {
	if login != f.logins.Load() {
		return 120
	}
	return 0
}

func alwaysFail(code int) func(*fakeWiNet, int32) int {
	return func(*fakeWiNet, int32) int { return code }
}

func newFakeWiNet(t *testing.T, failCode func(f *fakeWiNet, login int32) int) *fakeWiNet {
	t.Helper()
	f := &fakeWiNet{failCode: failCode}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/home/overview", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() {
			if err := conn.CloseNow(); err != nil {
				t.Logf("fake socket cleanup: %v", err)
			}
		}()
		for {
			_, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var req map[string]string
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Error(err)
				return
			}
			code := 1
			data := map[string]any{"service": req["service"]}
			switch req["service"] {
			case "connect":
				data["token"] = "anonymous"
			case "login":
				data["token"] = fmt.Sprintf("session-%d", f.logins.Add(1))
			case "devicelist":
				data["list"] = []map[string]int{{"dev_type": 21, "dev_code": 9736}}
			case "ping":
				f.pings.Add(1)
				if override := f.pingCode.Load(); override != 0 {
					code = int(override)
				}
			default:
				t.Errorf("unexpected service %q", req["service"])
				return
			}
			if f.dropSocket.CompareAndSwap(true, false) {
				return
			}
			msg := "success"
			if code != 1 {
				msg = "rejected"
			}
			resp, err := json.Marshal(map[string]any{"result_code": code, "result_msg": msg, "result_data": data})
			if err != nil {
				t.Error(err)
				return
			}
			if err := conn.Write(r.Context(), websocket.MessageText, resp); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/device/getParam", func(w http.ResponseWriter, r *http.Request) {
		var login int32
		if _, err := fmt.Sscanf(r.URL.Query().Get("token"), "session-%d", &login); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if code := f.failCode(f, login); code != 0 {
			if _, err := fmt.Fprintf(w, `{"result_code":%d,"result_msg":"I18N_COMMON_INTER_ABNORMAL"}`, code); err != nil {
				t.Error(err)
			}
			return
		}
		httpGetParamHandler(map[string]string{
			"5031": "13 88 00 00 ", "5083": "F8 30 FF FF ", "5091": "0B B8 00 00 ",
		})(w, r)
	})
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeWiNet) client(t *testing.T) *SungrowClient {
	t.Helper()
	sum := sha256.Sum256(f.server.Certificate().Raw)
	client, err := NewConfigured(config.Config{
		SungrowHost: strings.TrimPrefix(f.server.URL, "https://"), SungrowPort: 443,
		SungrowTLS: true, SungrowCertSHA256: hex.EncodeToString(sum[:]),
		SungrowUsername: "test-admin", SungrowPassword: "test-password",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Logf("client cleanup: %v", err)
		}
	})
	client.readGap = 0
	return client
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func pollUntilReset(t *testing.T, ctx context.Context, client *SungrowClient) {
	t.Helper()
	for poll := 1; poll <= sessionResetAfter; poll++ {
		if _, err := client.GetPowerData(ctx); err == nil {
			t.Fatalf("poll %d unexpectedly succeeded", poll)
		}
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Reproduces the production failure: logging in elsewhere (web UI, another
// client) supersedes the app's session, after which every register read
// returned result_code 120 until the app was restarted. The keepalive still
// succeeds for a superseded session, so recovery must come from the read.
func TestSupersededSessionRecoversWithinOnePoll(t *testing.T) {
	winet := newFakeWiNet(t, supersededOnly)
	client := winet.client(t)
	ctx := testContext(t)
	if _, err := client.GetPowerData(ctx); err != nil {
		t.Fatal(err)
	}

	other := winet.client(t)
	if err := other.Connect(ctx); err != nil {
		t.Fatal(err)
	}

	power, err := client.GetPowerData(ctx)
	if err != nil {
		t.Fatalf("poll after another login: %v", err)
	}
	if power.PVWatts != 5000 {
		t.Fatalf("PVWatts = %v, want 5000", power.PVWatts)
	}
	if got := winet.logins.Load(); got != 3 {
		t.Fatalf("logins = %d, want 3", got)
	}
}

// Errors that are not session rejections still abandon the session after
// sessionResetAfter consecutive failed polls.
func TestRepeatedFailuresResetSession(t *testing.T) {
	winet := newFakeWiNet(t, func(_ *fakeWiNet, login int32) int {
		if login == 1 {
			return 999
		}
		return 0
	})
	client := winet.client(t)
	ctx := testContext(t)

	pollUntilReset(t, ctx, client)
	if got := winet.logins.Load(); got != 1 {
		t.Fatalf("logins before reset = %d, want 1", got)
	}
	if _, err := client.GetPowerData(ctx); err != nil {
		t.Fatalf("poll after reset: %v", err)
	}
	if got := winet.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2", got)
	}
}

func TestPersistentSessionRejectionBacksOffBetweenLogins(t *testing.T) {
	for _, code := range []int{120, 106} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			winet := newFakeWiNet(t, alwaysFail(code))
			client := winet.client(t)
			clock := &fakeClock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
			client.now = clock.now
			ctx := testContext(t)

			for _, step := range []struct {
				advance    time.Duration
				wantLogins int32
			}{
				{advance: 0, wantLogins: 2},                // login, rejected, immediate re-login
				{advance: 0, wantLogins: 2},                // rejected again: 30s backoff starts
				{advance: 29 * time.Second, wantLogins: 2}, // still backing off
				{advance: time.Second, wantLogins: 3},      // retry; next wait 60s
				{advance: 59 * time.Second, wantLogins: 3},
				{advance: time.Second, wantLogins: 4},
			} {
				clock.advance(step.advance)
				if _, err := client.GetPowerData(ctx); err == nil {
					t.Fatal("expected failure while sessions are rejected")
				}
				if got := winet.logins.Load(); got != step.wantLogins {
					t.Fatalf("after +%s: logins = %d, want %d", step.advance, got, step.wantLogins)
				}
			}
		})
	}
}

func TestReconnectBackoffSchedule(t *testing.T) {
	client := New("127.0.0.1", 443, nil, nil)
	clock := &fakeClock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	client.now = clock.now
	for _, want := range []time.Duration{
		0, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute,
	} {
		client.scheduleReconnectLocked()
		if got := client.nextConnect.Sub(clock.now()); got != want {
			t.Fatalf("reset %d: delay = %s, want %s", client.resets, got, want)
		}
	}
	for range 100 {
		client.scheduleReconnectLocked()
	}
	if got := client.nextConnect.Sub(clock.now()); got != reconnectBackoffMax {
		t.Fatalf("delay after many resets = %s, want %s", got, reconnectBackoffMax)
	}
}

func TestSuccessfulPollClearsFailureHistory(t *testing.T) {
	var failing atomic.Bool
	winet := newFakeWiNet(t, func(*fakeWiNet, int32) int {
		if failing.Load() {
			return 999
		}
		return 0
	})
	client := winet.client(t)
	ctx := testContext(t)

	failing.Store(true)
	for range sessionResetAfter - 1 {
		if _, err := client.GetPowerData(ctx); err == nil {
			t.Fatal("expected failure")
		}
	}
	failing.Store(false)
	if _, err := client.GetPowerData(ctx); err != nil {
		t.Fatal(err)
	}
	failing.Store(true)
	for range sessionResetAfter - 1 {
		if _, err := client.GetPowerData(ctx); err == nil {
			t.Fatal("expected failure")
		}
	}
	if got := winet.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1: intermittent failures must not reset the session", got)
	}
}

func TestKeepalive(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pingCode   int32
		drop       bool
		wantLogins int32
	}{
		{name: "healthy session is reused", wantLogins: 1},
		{name: "unsupported ping is tolerated", pingCode: 404, wantLogins: 1},
		{name: "rejected session token reconnects", pingCode: 106, wantLogins: 2},
		{name: "unauthorized ping reconnects", pingCode: 401, wantLogins: 2},
		{name: "closed socket reconnects", drop: true, wantLogins: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			winet := newFakeWiNet(t, alwaysFail(0))
			client := winet.client(t)
			ctx := testContext(t)
			if _, err := client.GetPowerData(ctx); err != nil {
				t.Fatal(err)
			}
			winet.pingCode.Store(tc.pingCode)
			winet.dropSocket.Store(tc.drop)
			if _, err := client.GetPowerData(ctx); err != nil {
				t.Fatal(err)
			}
			if got := winet.pings.Load(); got < 1 {
				t.Fatalf("pings = %d, want at least 1", got)
			}
			if got := winet.logins.Load(); got != tc.wantLogins {
				t.Fatalf("logins = %d, want %d", got, tc.wantLogins)
			}
		})
	}
}

func TestCancelledPollsDoNotResetSession(t *testing.T) {
	winet := newFakeWiNet(t, alwaysFail(0))
	client := winet.client(t)
	if _, err := client.GetPowerData(testContext(t)); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for range sessionResetAfter + 1 {
		if _, err := client.GetPowerData(cancelled); err == nil {
			t.Fatal("expected cancellation error")
		}
	}
	client.mu.Lock()
	failed, resets := client.failedPolls, client.resets
	client.mu.Unlock()
	if failed != 0 || resets != 0 {
		t.Fatalf("failedPolls = %d, resets = %d; cancellation must not count as a module fault", failed, resets)
	}
}
