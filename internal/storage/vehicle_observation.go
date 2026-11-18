package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SaveVehicleObservation replaces the last confirmed reading for its VIN.
func (s *SQLiteStore) SaveVehicleObservation(ctx context.Context, observation VehicleObservation) error {
	if observation.VIN == "" || observation.ObservedAt.IsZero() || !observation.ChargeState.IsOnline {
		return fmt.Errorf("storage: vehicle observation requires a VIN, timestamp, and online reading")
	}
	data, err := json.Marshal(observation.ChargeState)
	if err != nil {
		return fmt.Errorf("storage: encode vehicle observation: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO vehicle_observations (vin, observed_at, charge_state) VALUES (?, ?, ?)
		ON CONFLICT(vin) DO UPDATE SET observed_at = excluded.observed_at, charge_state = excluded.charge_state`,
		observation.VIN, observation.ObservedAt.UTC().Format(time.RFC3339Nano), string(data))
	if err != nil {
		return fmt.Errorf("storage: save vehicle observation: %w", err)
	}
	return nil
}

// LoadVehicleObservation returns nil when this VIN has no confirmed reading.
func (s *SQLiteStore) LoadVehicleObservation(ctx context.Context, vin string) (*VehicleObservation, error) {
	observation := &VehicleObservation{VIN: vin}
	var observedAt, data string
	err := s.db.QueryRowContext(ctx,
		`SELECT observed_at, charge_state FROM vehicle_observations WHERE vin = ?`, vin).
		Scan(&observedAt, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: load vehicle observation: %w", err)
	}
	observation.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return nil, fmt.Errorf("storage: parse vehicle observation timestamp: %w", err)
	}
	if err := json.Unmarshal([]byte(data), &observation.ChargeState); err != nil {
		return nil, fmt.Errorf("storage: decode vehicle observation: %w", err)
	}
	return observation, nil
}
