package tesla

import (
	"context"
	"errors"
)

// Sentinel errors for known vehicle states.
var (
	ErrCarOffline       = errors.New("tesla: vehicle is offline")
	ErrNotPluggedIn     = errors.New("tesla: vehicle is not plugged in")
	ErrNotCharging      = errors.New("tesla: vehicle is not charging")
	ErrCommandsDisabled = errors.New("tesla: commands disabled in test mode")
)

// ChargeState holds the vehicle's current charging information.
type ChargeState struct {
	State      string
	AmpsActual int
	BatteryPct float64
	PluggedIn  bool
	IsOnline   bool
}

// VehicleController controls EV charging.
type VehicleController interface {
	GetChargeState(ctx context.Context) (ChargeState, error)
	SetChargingAmps(ctx context.Context, amps int) error
	StartCharging(ctx context.Context) error
	StopCharging(ctx context.Context) error
	WakeUp(ctx context.Context) error
}
