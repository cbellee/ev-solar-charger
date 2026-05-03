package tesla

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/observability"
)

func Test_ActivatingController_SetRefreshToken_promotesUnavailableController(t *testing.T) {
	fleetMux := http.NewServeMux()
	fleetMux.HandleFunc("/api/1/vehicles/TEST_VIN/vehicle_data", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"state": "online",
				"charge_state": map[string]any{
					"charging_state":         "Stopped",
					"charger_actual_current": 0,
					"battery_level":          62,
					"charge_port_latch":      "Engaged",
					"charge_limit_soc":       80,
					"charge_limit_soc_min":   50,
					"charge_limit_soc_max":   100,
				},
			},
		})
	})

	client, _ := newTestClient(t, fleetMux, defaultAuthHandler())
	activating := NewActivatingController(testConfig(""), testLogger(), nil,
		NewUnavailableController("complete /auth/tesla to activate telemetry"))
	activating.factory = func(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) (VehicleController, error) {
		if cfg.TeslaRefreshToken != "incoming-refresh" {
			t.Fatalf("factory refresh token = %q, want %q", cfg.TeslaRefreshToken, "incoming-refresh")
		}
		return client, nil
	}

	if err := activating.SetRefreshToken(context.Background(), "incoming-refresh"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, err := activating.GetChargeState(context.Background())
	if err != nil {
		t.Fatalf("GetChargeState error: %v", err)
	}
	if !state.IsOnline {
		t.Fatal("IsOnline = false, want true")
	}
	if !state.PluggedIn {
		t.Fatal("PluggedIn = false, want true")
	}
	if state.BatteryPct != 62 {
		t.Fatalf("BatteryPct = %v, want 62", state.BatteryPct)
	}

	usage := activating.GetAPIUsage()
	if usage.MonthStarted.IsZero() {
		t.Fatal("MonthStarted is zero, want current month")
	}
	monthStart := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Now().Location())
	if !usage.MonthStarted.Equal(monthStart) {
		t.Fatalf("MonthStarted = %v, want %v", usage.MonthStarted, monthStart)
	}

	// Ensure the active controller was promoted to a real Tesla client.
	if _, ok := activating.current().(*TeslaClient); !ok {
		t.Fatal("active controller was not promoted to *TeslaClient")
	}
}
