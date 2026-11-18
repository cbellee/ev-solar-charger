package tesla

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"
)

const (
	fleetStatusRefreshInterval = 6 * time.Hour
	fleetStatusRetryInterval   = 5 * time.Minute
)

// FleetStatus contains cached capabilities reported by Tesla, not a guarantee
// that telemetry is configured or that the vehicle is currently connected.
type FleetStatus struct {
	FetchedAt                          time.Time `json:"fetchedAt"`
	LastAttemptAt                      time.Time `json:"lastAttemptAt"`
	LastError                          string    `json:"lastError,omitempty"`
	KeyPaired                          *bool     `json:"keyPaired,omitempty"`
	VehicleCommandProtocolRequired     *bool     `json:"vehicleCommandProtocolRequired,omitempty"`
	SafetyScreenStreamingToggleEnabled *bool     `json:"safetyScreenStreamingToggleEnabled,omitempty"`
	FirmwareVersion                    string    `json:"firmwareVersion,omitempty"`
	FleetTelemetryVersion              string    `json:"fleetTelemetryVersion,omitempty"`
	DiscountedDataEligible             bool      `json:"discountedDataEligible"`
}

// FleetStatusProvider exposes cached diagnostics without making an API request.
// Test-mode and unavailable vehicle controllers need not implement it.
type FleetStatusProvider interface {
	GetFleetStatus() (FleetStatus, error)
}

type fleetStatusEntry struct {
	VIN                                string `json:"vin"`
	VehicleCommandProtocolRequired     *bool  `json:"vehicle_command_protocol_required"`
	SafetyScreenStreamingToggleEnabled *bool  `json:"safety_screen_streaming_toggle_enabled"`
	FirmwareVersion                    string `json:"firmware_version"`
	FleetTelemetryVersion              string `json:"fleet_telemetry_version"`
	DiscountedDeviceData               *bool  `json:"discounted_device_data"`
}

// GetFleetStatus returns the last capability check, including its age and any
// refresh failure. A zero FetchedAt means no successful check has completed.
func (c *TeslaClient) GetFleetStatus() (FleetStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.fleetStatus
	status.KeyPaired = copyBool(status.KeyPaired)
	status.VehicleCommandProtocolRequired = copyBool(status.VehicleCommandProtocolRequired)
	status.SafetyScreenStreamingToggleEnabled = copyBool(status.SafetyScreenStreamingToggleEnabled)
	return status, nil
}

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (c *TeslaClient) refreshFleetStatusIfStale(ctx context.Context) {
	now := time.Now()
	c.mu.Lock()
	status := c.fleetStatus
	if (!c.fleetStatusInvalidated && !status.FetchedAt.IsZero() && status.LastError == "" && now.Sub(status.FetchedAt) < fleetStatusRefreshInterval) ||
		(!status.LastAttemptAt.IsZero() && now.Sub(status.LastAttemptAt) < fleetStatusRetryInterval) {
		c.mu.Unlock()
		return
	}
	// Reserve the attempt before I/O so concurrent callers share the check.
	c.fleetStatus.LastAttemptAt = now
	c.mu.Unlock()

	fresh, err := c.fetchFleetStatus(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		// Keep the last successful capabilities, but never mark a failed check fresh.
		c.fleetStatus.LastError = "fleet status refresh failed; see server logs"
		if c.logger != nil {
			c.logger.WarnContext(ctx, "tesla: fleet_status unavailable", "error", err)
		}
		return
	}
	fresh.FetchedAt = time.Now()
	fresh.LastAttemptAt = now
	c.fleetStatus = fresh
	c.fleetStatusInvalidated = false
}

func (c *TeslaClient) fetchFleetStatus(ctx context.Context) (FleetStatus, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/1/vehicles/fleet_status", map[string][]string{"vins": {c.vin}})
	if err != nil {
		return FleetStatus{}, err
	}
	defer resp.Body.Close()

	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return FleetStatus{}, fmt.Errorf("tesla: decode fleet_status: %w", err)
	}
	return decodeFleetStatus(envelope.Response, c.vin)
}

func decodeFleetStatus(raw json.RawMessage, vin string) (FleetStatus, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil && object != nil {
		if vehicleInfo, ok := object["vehicle_info"]; ok {
			var entries map[string]json.RawMessage
			if err := json.Unmarshal(vehicleInfo, &entries); err != nil {
				return FleetStatus{}, fmt.Errorf("tesla: decode fleet_status vehicle_info: %w", err)
			}
			entry, ok := entries[vin]
			if !ok {
				return FleetStatus{}, fmt.Errorf("tesla: fleet_status missing configured vehicle")
			}
			status, err := decodeFleetStatusEntry(entry, vin)
			if err != nil {
				return FleetStatus{}, err
			}
			var paired struct {
				Paired   []string `json:"key_paired_vins"`
				Unpaired []string `json:"unpaired_vins"`
			}
			if err := json.Unmarshal(raw, &paired); err != nil {
				return FleetStatus{}, fmt.Errorf("tesla: decode fleet_status key pairing: %w", err)
			}
			isPaired := slices.Contains(paired.Paired, vin)
			isUnpaired := slices.Contains(paired.Unpaired, vin)
			if isPaired && isUnpaired {
				return FleetStatus{}, fmt.Errorf("tesla: conflicting fleet_status key pairing")
			}
			if isPaired || isUnpaired {
				status.KeyPaired = &isPaired
			}
			return status, nil
		}
		if entry, ok := object[vin]; ok {
			return decodeFleetStatusEntry(entry, vin)
		}
		return decodeFleetStatusEntry(raw, vin)
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err == nil {
		for _, rawEntry := range entries {
			var entry fleetStatusEntry
			if err := json.Unmarshal(rawEntry, &entry); err != nil {
				return FleetStatus{}, fmt.Errorf("tesla: decode fleet_status entry: %w", err)
			}
			if entry.VIN == vin {
				return decodeFleetStatusEntry(rawEntry, vin)
			}
		}
	}
	return FleetStatus{}, fmt.Errorf("tesla: unrecognised fleet_status response")
}

func decodeFleetStatusEntry(raw json.RawMessage, vin string) (FleetStatus, error) {
	var entry fleetStatusEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return FleetStatus{}, fmt.Errorf("tesla: decode fleet_status entry: %w", err)
	}
	if entry.VIN != "" && entry.VIN != vin {
		return FleetStatus{}, fmt.Errorf("tesla: fleet_status returned a different vehicle")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return FleetStatus{}, fmt.Errorf("tesla: decode fleet_status fields: %w", err)
	}
	known := false
	for _, name := range []string{
		"vehicle_command_protocol_required", "safety_screen_streaming_toggle_enabled",
		"firmware_version", "fleet_telemetry_version", "discounted_device_data",
	} {
		if value, ok := fields[name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			known = true
			break
		}
	}
	if !known {
		return FleetStatus{}, fmt.Errorf("tesla: fleet_status has no capability fields")
	}
	return FleetStatus{
		VehicleCommandProtocolRequired:     entry.VehicleCommandProtocolRequired,
		SafetyScreenStreamingToggleEnabled: entry.SafetyScreenStreamingToggleEnabled,
		FirmwareVersion:                    entry.FirmwareVersion,
		FleetTelemetryVersion:              entry.FleetTelemetryVersion,
		DiscountedDataEligible:             entry.DiscountedDeviceData != nil && *entry.DiscountedDeviceData,
	}, nil
}
