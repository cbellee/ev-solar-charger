package tesla

import "context"

type testModeController struct{}

// NewTestModeController returns a no-op vehicle controller for projected-only mode.
func NewTestModeController() VehicleController {
	return testModeController{}
}

func (testModeController) GetChargeState(ctx context.Context) (ChargeState, error) {
	return ChargeState{State: "Projected only"}, nil
}

func (testModeController) SetChargingAmps(ctx context.Context, amps int) error {
	return ErrCommandsDisabled
}

func (testModeController) StartCharging(ctx context.Context) error {
	return ErrCommandsDisabled
}

func (testModeController) StopCharging(ctx context.Context) error {
	return ErrCommandsDisabled
}

func (testModeController) WakeUp(ctx context.Context) error {
	return ErrCommandsDisabled
}

func (testModeController) SetRefreshToken(ctx context.Context, refreshToken string) error {
	return nil
}

func (testModeController) GetAPIUsage() APIUsage {
	return APIUsage{}
}
