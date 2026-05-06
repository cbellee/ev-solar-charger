package tesla

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cbellee/ev-solar-charger/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func installTeslaAuthTransport(t *testing.T, handler http.Handler) {
	t.Helper()

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	targetURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	originalTransport := http.DefaultTransport
	host := strings.TrimPrefix(authURL, "https://")
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == host {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = targetURL.Scheme
			clone.URL.Host = targetURL.Host
			clone.Host = targetURL.Host
			return server.Client().Transport.RoundTrip(clone)
		}
		return originalTransport.RoundTrip(req)
	})
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})
}

func testConfig(keyPath string) config.Config {
	return config.Config{
		TeslaClientID:       "test-id",
		TeslaClientSecret:   "test-secret",
		TeslaRefreshToken:   "test-refresh",
		TeslaVIN:            "TEST_VIN",
		TeslaPrivateKeyPath: keyPath,
		TeslaRegion:         "na",
		MinChargeAmps:       5,
		MaxChargeAmps:       32,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeECPrivateKeyFile(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "fleet-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}
	return path
}

type testServer struct {
	fleet *httptest.Server
	auth  *httptest.Server
}

func newTestClient(t *testing.T, fleetHandler, authHandler http.Handler) (*TeslaClient, *testServer) {
	t.Helper()
	fleet := httptest.NewServer(fleetHandler)
	t.Cleanup(fleet.Close)
	auth := httptest.NewServer(authHandler)
	t.Cleanup(auth.Close)

	c := &TeslaClient{
		httpClient:   http.DefaultClient,
		baseURL:      fleet.URL,
		authURL:      auth.URL,
		vin:          "TEST_VIN",
		clientID:     "test-id",
		clientSecret: "test-secret",
		refreshToken: "test-refresh",
		accessToken:  "test-access",
		minAmps:      5,
		maxAmps:      32,
		usage:        NewAPIUsageTracker(),
	}
	return c, &testServer{fleet: fleet, auth: auth}
}

func defaultAuthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/v3/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	})
	return mux
}

func Test_GetChargeState_charging(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"charge_state": map[string]any{
					"charging_state":         "Charging",
					"charger_actual_current": 16,
					"battery_level":          55.0,
					"time_to_full_charge":    1.5,
					"charge_port_latch":      "Engaged",
				},
				"state": "online",
			},
		})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	cs, err := c.GetChargeState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cs.State != "Charging" {
		t.Errorf("State = %q, want %q", cs.State, "Charging")
	}
	if cs.AmpsActual != 16 {
		t.Errorf("AmpsActual = %d, want 16", cs.AmpsActual)
	}
	if cs.BatteryPct != 55.0 {
		t.Errorf("BatteryPct = %f, want 55.0", cs.BatteryPct)
	}
	if cs.TimeToLimitHours != 1.5 {
		t.Errorf("TimeToLimitHours = %f, want 1.5", cs.TimeToLimitHours)
	}
	if !cs.PluggedIn {
		t.Error("PluggedIn should be true")
	}
	if !cs.IsOnline {
		t.Error("IsOnline should be true")
	}
}

func Test_GetChargeState_disconnected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"charge_state": map[string]any{
					"charging_state":    "Disconnected",
					"charge_port_latch": "Disengaged",
				},
				"state": "online",
			},
		})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	cs, err := c.GetChargeState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cs.PluggedIn {
		t.Error("PluggedIn should be false for disconnected")
	}
}

func Test_GetChargeState_disconnectedDespiteEngagedLatch(t *testing.T) {
	// Some firmware reports latch=Engaged briefly after the cable is removed.
	// Trusting the latch alone causes the controller to keep waking a car
	// that is no longer plugged in. The disconnected charging state must
	// override the latch.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"charge_state": map[string]any{
					"charging_state":    "Disconnected",
					"charge_port_latch": "Engaged",
				},
				"state": "online",
			},
		})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	cs, err := c.GetChargeState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cs.PluggedIn {
		t.Error("PluggedIn should be false when ChargingState=Disconnected even if latch=Engaged")
	}
	if cs.State != "Disconnected" {
		t.Errorf("State = %q, want Disconnected", cs.State)
	}
}

func Test_GetChargeState_vehicleOffline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	_, err := c.GetChargeState(context.Background())
	if !errors.Is(err, ErrCarOffline) {
		t.Errorf("expected ErrCarOffline, got %v", err)
	}
}

func Test_SetChargingAmps_valid(t *testing.T) {
	var receivedAmps int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/command/set_charging_amps", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]int
		json.NewDecoder(r.Body).Decode(&body)
		receivedAmps = body["charging_amps"]
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": true}})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	if err := c.SetChargingAmps(context.Background(), 16); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAmps != 16 {
		t.Errorf("receivedAmps = %d, want 16", receivedAmps)
	}
}

func Test_SetChargingAmps_clampLow(t *testing.T) {
	var receivedAmps int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/command/set_charging_amps", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]int
		json.NewDecoder(r.Body).Decode(&body)
		receivedAmps = body["charging_amps"]
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": true}})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	if err := c.SetChargingAmps(context.Background(), 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAmps != 5 {
		t.Errorf("receivedAmps = %d, want 5 (clamped to min)", receivedAmps)
	}
}

func Test_SetChargingAmps_clampHigh(t *testing.T) {
	var receivedAmps int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/command/set_charging_amps", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]int
		json.NewDecoder(r.Body).Decode(&body)
		receivedAmps = body["charging_amps"]
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": true}})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	if err := c.SetChargingAmps(context.Background(), 50); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAmps != 32 {
		t.Errorf("receivedAmps = %d, want 32 (clamped to max)", receivedAmps)
	}
}

func Test_StartCharging_success(t *testing.T) {
	var calledEndpoint string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/command/charge_start", func(w http.ResponseWriter, r *http.Request) {
		calledEndpoint = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": true}})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	if err := c.StartCharging(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(calledEndpoint, "charge_start") {
		t.Errorf("expected charge_start endpoint, got %q", calledEndpoint)
	}
}

func Test_StopCharging_success(t *testing.T) {
	var calledEndpoint string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/command/charge_stop", func(w http.ResponseWriter, r *http.Request) {
		calledEndpoint = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": true}})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	if err := c.StopCharging(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(calledEndpoint, "charge_stop") {
		t.Errorf("expected charge_stop endpoint, got %q", calledEndpoint)
	}
}

func Test_WakeUp_immediatelyOnline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/wake_up", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": true}})
	})
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"charge_state": map[string]any{
					"charging_state":    "Stopped",
					"charge_port_latch": "Engaged",
				},
				"state": "online",
			},
		})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	if err := c.WakeUp(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func Test_tokenRefresh_onExpiry(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"charge_state": map[string]any{
					"charging_state": "Stopped",
				},
				"state": "online",
			},
		})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	cs, err := c.GetChargeState(context.Background())
	if err != nil {
		t.Fatalf("expected retry after 401 to succeed, got %v", err)
	}
	if cs.State != "Stopped" {
		t.Errorf("State = %q, want %q", cs.State, "Stopped")
	}
}

func Test_tokenRefresh_failure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	authMux := http.NewServeMux()
	authMux.HandleFunc("/oauth2/v3/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	c, _ := newTestClient(t, mux, authMux)
	_, err := c.GetChargeState(context.Background())
	if err == nil {
		t.Fatal("expected error for failed token refresh")
	}
}

func Test_doRequest_serverError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/command/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())
	path := "/api/1/vehicles/TEST_VIN/command/test"
	_, err := c.doRequest(context.Background(), http.MethodPost, path, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func Test_New_invalidRegion(t *testing.T) {
	cfg := testConfig(filepath.Join(t.TempDir(), "missing.pem"))
	cfg.TeslaRegion = "mars"

	_, err := New(cfg, testLogger(), nil)
	if err == nil {
		t.Fatal("expected error for invalid Tesla region")
	}
}

func Test_New_missingPrivateKey_allowsStartup(t *testing.T) {
	installTeslaAuthTransport(t, defaultAuthHandler())
	cfg := testConfig(filepath.Join(t.TempDir(), "missing.pem"))

	client, err := New(cfg, testLogger(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.privateKey != nil {
		t.Fatal("expected privateKey to remain nil when key file is missing")
	}
	if client.accessToken != "new-access" {
		t.Fatalf("accessToken = %q, want %q", client.accessToken, "new-access")
	}
	if client.refreshToken != "new-refresh" {
		t.Fatalf("refreshToken = %q, want %q", client.refreshToken, "new-refresh")
	}
	if client.baseURL != regionBaseURLs["na"] {
		t.Fatalf("baseURL = %q, want %q", client.baseURL, regionBaseURLs["na"])
	}
}

func Test_New_validPrivateKey_loadsSigningKey(t *testing.T) {
	installTeslaAuthTransport(t, defaultAuthHandler())
	cfg := testConfig(writeECPrivateKeyFile(t))

	client, err := New(cfg, testLogger(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.privateKey == nil {
		t.Fatal("expected privateKey to be loaded")
	}
}

func Test_New_invalidPrivateKey_returnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-key.pem")
	key := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("invalid")})
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("failed to write invalid key: %v", err)
	}

	_, err := New(testConfig(path), testLogger(), nil)
	if err == nil {
		t.Fatal("expected error for invalid EC private key")
	}
	if !strings.Contains(err.Error(), "parse private key") {
		t.Fatalf("expected parse private key error, got %v", err)
	}
}

func Test_New_initialTokenRefreshFailure(t *testing.T) {
	authMux := http.NewServeMux()
	authMux.HandleFunc("/oauth2/v3/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	installTeslaAuthTransport(t, authMux)

	_, err := New(testConfig(filepath.Join(t.TempDir(), "missing.pem")), testLogger(), nil)
	if err == nil {
		t.Fatal("expected initial token refresh error")
	}
	if !strings.Contains(err.Error(), "initial token refresh") {
		t.Fatalf("expected initial token refresh error, got %v", err)
	}
}

func Test_APIUsageTracker_recordsAllTypes(t *testing.T) {
	tracker := NewAPIUsageTracker()
	tracker.RecordData()
	tracker.RecordData()
	tracker.RecordCommand()
	tracker.RecordCommand()
	tracker.RecordCommand()
	tracker.RecordWake()

	snap := tracker.Snapshot()
	if snap.DataCalls != 2 {
		t.Errorf("DataCalls = %d, want 2", snap.DataCalls)
	}
	if snap.CommandCalls != 3 {
		t.Errorf("CommandCalls = %d, want 3", snap.CommandCalls)
	}
	if snap.WakeCalls != 1 {
		t.Errorf("WakeCalls = %d, want 1", snap.WakeCalls)
	}
	if snap.StreamSignals != 0 {
		t.Errorf("StreamSignals = %d, want 0", snap.StreamSignals)
	}
}

func Test_APIUsageTracker_estimatedCost(t *testing.T) {
	tracker := NewAPIUsageTracker()
	// 500 data calls = $1.00, 1000 commands = $1.00, 50 wakes = $1.00
	for range 500 {
		tracker.RecordData()
	}
	for range 1000 {
		tracker.RecordCommand()
	}
	for range 50 {
		tracker.RecordWake()
	}

	snap := tracker.Snapshot()
	wantCost := 3.0
	if snap.EstimatedCost < wantCost-0.001 || snap.EstimatedCost > wantCost+0.001 {
		t.Errorf("EstimatedCost = %f, want %f", snap.EstimatedCost, wantCost)
	}
}

func Test_APIUsageTracker_monthReset(t *testing.T) {
	tracker := NewAPIUsageTracker()
	tracker.RecordData()
	tracker.RecordCommand()

	// Manually set month to last month to trigger reset.
	tracker.mu.Lock()
	tracker.monthStart = tracker.monthStart.AddDate(0, -1, 0)
	tracker.mu.Unlock()

	tracker.RecordData()
	snap := tracker.Snapshot()
	if snap.DataCalls != 1 {
		t.Errorf("DataCalls after month reset = %d, want 1", snap.DataCalls)
	}
	if snap.CommandCalls != 0 {
		t.Errorf("CommandCalls after month reset = %d, want 0", snap.CommandCalls)
	}
}

func Test_GetAPIUsage_returnsTrackerSnapshot(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"charge_state": map[string]any{
					"charging_state": "Stopped",
				},
				"state": "online",
			},
		})
	})
	mux.HandleFunc("/api/1/vehicles/TEST_VIN/command/set_charging_amps", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": true}})
	})
	c, _ := newTestClient(t, mux, defaultAuthHandler())

	c.GetChargeState(context.Background())
	c.GetChargeState(context.Background())
	c.SetChargingAmps(context.Background(), 10)

	usage := c.GetAPIUsage()
	if usage.DataCalls != 2 {
		t.Errorf("DataCalls = %d, want 2", usage.DataCalls)
	}
	if usage.CommandCalls != 1 {
		t.Errorf("CommandCalls = %d, want 1", usage.CommandCalls)
	}
}

func Test_SetRefreshToken_updatesAndRefreshesAccessToken(t *testing.T) {
	mux := http.NewServeMux()
	authMux := http.NewServeMux()
	var seenRefreshToken string
	authMux.HandleFunc("/oauth2/v3/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		seenRefreshToken = r.Form.Get("refresh_token")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "rotated-access",
			"refresh_token": "rotated-refresh",
			"expires_in":    3600,
		})
	})
	c, _ := newTestClient(t, mux, authMux)

	if err := c.SetRefreshToken(context.Background(), "incoming-refresh"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenRefreshToken != "incoming-refresh" {
		t.Fatalf("seen refresh token = %q, want %q", seenRefreshToken, "incoming-refresh")
	}
	if c.accessToken != "rotated-access" {
		t.Fatalf("accessToken = %q, want %q", c.accessToken, "rotated-access")
	}
	if c.refreshToken != "rotated-refresh" {
		t.Fatalf("refreshToken = %q, want %q", c.refreshToken, "rotated-refresh")
	}
}

func Test_SetRefreshToken_emptyFails(t *testing.T) {
	mux := http.NewServeMux()
	c, _ := newTestClient(t, mux, defaultAuthHandler())

	err := c.SetRefreshToken(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty refresh token")
	}
}
