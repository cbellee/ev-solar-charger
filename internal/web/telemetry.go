package web

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
)

type telemetryChargeStateUpdater interface {
	GetCachedChargeState() tesla.ChargeState
	UpdateTelemetryChargeState(tesla.ChargeState, time.Time)
}

type fleetTelemetryChargeStateRequest struct {
	VIN              string   `json:"vin"`
	ObservedAt       string   `json:"observedAt"`
	ChargingState    *string  `json:"chargingState"`
	ActualAmps       *int     `json:"actualAmps"`
	BatteryPct       *float64 `json:"batteryPct"`
	TimeToLimitHours *float64 `json:"timeToLimitHours"`
	PluggedIn        *bool    `json:"pluggedIn"`
	Online           *bool    `json:"online"`
	ChargeLimit      *int     `json:"chargeLimit"`
	ChargeLimitMin   *int     `json:"chargeLimitMin"`
	ChargeLimitMax   *int     `json:"chargeLimitMax"`
}

func handleFleetTelemetryChargeState(ctrl telemetryChargeStateUpdater, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(cfg.FleetTelemetrySharedSecret) == "" {
			http.NotFound(w, r)
			return
		}
		if !fleetTelemetryAuthorized(r, cfg.FleetTelemetrySharedSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req fleetTelemetryChargeStateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.VIN) == "" {
			http.Error(w, "vin is required", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(cfg.TeslaVIN) != "" && req.VIN != cfg.TeslaVIN {
			http.Error(w, "vin mismatch", http.StatusBadRequest)
			return
		}

		observedAt, err := parseTelemetryObservedAt(req.ObservedAt)
		if err != nil {
			http.Error(w, "invalid observedAt", http.StatusBadRequest)
			return
		}

		merged := mergeTelemetryChargeState(ctrl.GetCachedChargeState(), req)
		ctrl.UpdateTelemetryChargeState(merged, observedAt)
		w.WriteHeader(http.StatusNoContent)
	}
}

func parseTelemetryObservedAt(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Now(), nil
	}
	return time.Parse(time.RFC3339, value)
}

func mergeTelemetryChargeState(base tesla.ChargeState, req fleetTelemetryChargeStateRequest) tesla.ChargeState {
	merged := base
	if req.ChargingState != nil {
		merged.State = *req.ChargingState
	}
	if req.ActualAmps != nil {
		merged.AmpsActual = *req.ActualAmps
	}
	if req.BatteryPct != nil {
		merged.BatteryPct = *req.BatteryPct
	}
	if req.TimeToLimitHours != nil {
		merged.TimeToLimitHours = *req.TimeToLimitHours
	}
	if req.PluggedIn != nil {
		merged.PluggedIn = *req.PluggedIn
	}
	if req.Online != nil {
		merged.IsOnline = *req.Online
	}
	if req.ChargeLimit != nil {
		merged.ChargeLimit = *req.ChargeLimit
	}
	if req.ChargeLimitMin != nil {
		merged.ChargeLimitMin = *req.ChargeLimitMin
	}
	if req.ChargeLimitMax != nil {
		merged.ChargeLimitMax = *req.ChargeLimitMax
	}
	return merged
}

func fleetTelemetryAuthorized(r *http.Request, secret string) bool {
	provided := strings.TrimSpace(r.Header.Get("X-Fleet-Telemetry-Secret"))
	if provided == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			provided = strings.TrimSpace(auth[7:])
		}
	}
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 1
}