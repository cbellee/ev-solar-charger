package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
)

func Test_handleFleetTelemetryChargeState_updatesControllerCache(t *testing.T) {
	ctrl := newTestCtrl(t)
	ctrl.UpdateTelemetryChargeState(tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped", BatteryPct: 60}, time.Now().Add(-time.Minute))
	handler := handleFleetTelemetryChargeState(ctrl, config.Config{FleetTelemetrySharedSecret: "secret", TeslaVIN: "VIN123"})

	body, err := json.Marshal(map[string]any{
		"vin":            "VIN123",
		"observedAt":     time.Now().UTC().Format(time.RFC3339),
		"chargingState":  "Charging",
		"actualAmps":     16,
		"timeToLimitHours": 1.5,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/telemetry/tesla/charge-state", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fleet-Telemetry-Secret", "secret")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}

	cs := ctrl.GetCachedChargeState()
	if cs.State != "Charging" {
		t.Fatalf("State = %q, want %q", cs.State, "Charging")
	}
	if cs.AmpsActual != 16 {
		t.Fatalf("AmpsActual = %d, want 16", cs.AmpsActual)
	}
	if cs.TimeToLimitHours != 1.5 {
		t.Fatalf("TimeToLimitHours = %v, want 1.5", cs.TimeToLimitHours)
	}
	if !cs.IsOnline {
		t.Fatal("IsOnline = false, want true preserved from prior cache")
	}
	if !cs.PluggedIn {
		t.Fatal("PluggedIn = false, want true preserved from prior cache")
	}
	if cs.BatteryPct != 60 {
		t.Fatalf("BatteryPct = %v, want 60 preserved from prior cache", cs.BatteryPct)
	}
}

func Test_handleFleetTelemetryChargeState_rejectsWrongSecret(t *testing.T) {
	handler := handleFleetTelemetryChargeState(newTestCtrl(t), config.Config{FleetTelemetrySharedSecret: "secret", TeslaVIN: "VIN123"})
	req := httptest.NewRequest(http.MethodPost, "/telemetry/tesla/charge-state", bytes.NewReader([]byte(`{"vin":"VIN123"}`)))
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func Test_NewServer_allowsFleetTelemetryRouteWithoutEntraAuth(t *testing.T) {
	cfg := oauthTestConfig()
	cfg.FleetTelemetrySharedSecret = "secret"
	cfg.TeslaVIN = "VIN123"
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, denyAuth, cfg)

	req := httptest.NewRequest(http.MethodPost, "/telemetry/tesla/charge-state", bytes.NewReader([]byte(`{"vin":"VIN123"}`)))
	req.Header.Set("X-Fleet-Telemetry-Secret", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want empty for public telemetry route", got)
	}
}