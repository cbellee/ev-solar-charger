package tesla

import (
	"context"
	"fmt"
	"strings"
)

type unavailableController struct {
	reason string
}

// NewUnavailableController returns a vehicle controller that reports why Tesla
// state is currently unavailable while still allowing the process to stay up.
func NewUnavailableController(reason string) VehicleController {
	return unavailableController{reason: strings.TrimSpace(reason)}
}

func (c unavailableController) unavailableError() error {
	if c.reason == "" {
		return ErrVehicleUnavailable
	}
	return fmt.Errorf("%w: %s", ErrVehicleUnavailable, c.reason)
}

func (c unavailableController) GetChargeState(ctx context.Context) (ChargeState, error) {
	return ChargeState{}, c.unavailableError()
}

func (c unavailableController) SetChargingAmps(ctx context.Context, amps int) error {
	return c.unavailableError()
}

func (c unavailableController) SetChargeLimit(ctx context.Context, percent int) error {
	return c.unavailableError()
}

func (c unavailableController) StartCharging(ctx context.Context) error {
	return c.unavailableError()
}

func (c unavailableController) StopCharging(ctx context.Context) error {
	return c.unavailableError()
}

func (c unavailableController) WakeUp(ctx context.Context) error {
	return c.unavailableError()
}

func (c unavailableController) SetRefreshToken(ctx context.Context, refreshToken string) error {
	return c.unavailableError()
}

func (c unavailableController) GetAPIUsage() APIUsage {
	return APIUsage{}
}
