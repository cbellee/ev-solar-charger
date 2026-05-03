package tesla

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/observability"
)

// ActivatingController delegates to the current vehicle controller and can
// promote an unavailable fallback into a real TeslaClient once a refresh token
// becomes available at runtime.
type ActivatingController struct {
	mu      sync.RWMutex
	active  VehicleController
	cfg     config.Config
	logger  *slog.Logger
	metrics *observability.Metrics
	factory func(config.Config, *slog.Logger, *observability.Metrics) (VehicleController, error)
}

// NewActivatingController wraps the current vehicle controller so OAuth token
// activation can replace a fallback controller without swapping controller
// state in other packages.
func NewActivatingController(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics, active VehicleController) *ActivatingController {
	return &ActivatingController{
		active:  active,
		cfg:     cfg,
		logger:  logger,
		metrics: metrics,
		factory: func(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) (VehicleController, error) {
			return New(cfg, logger, metrics)
		},
	}
}

func (c *ActivatingController) current() VehicleController {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.active
}

func (c *ActivatingController) GetChargeState(ctx context.Context) (ChargeState, error) {
	return c.current().GetChargeState(ctx)
}

func (c *ActivatingController) SetChargingAmps(ctx context.Context, amps int) error {
	return c.current().SetChargingAmps(ctx, amps)
}

func (c *ActivatingController) SetChargeLimit(ctx context.Context, percent int) error {
	return c.current().SetChargeLimit(ctx, percent)
}

func (c *ActivatingController) StartCharging(ctx context.Context) error {
	return c.current().StartCharging(ctx)
}

func (c *ActivatingController) StopCharging(ctx context.Context) error {
	return c.current().StopCharging(ctx)
}

func (c *ActivatingController) WakeUp(ctx context.Context) error {
	return c.current().WakeUp(ctx)
}

// SetRefreshToken updates the active Tesla client if one is already running,
// or creates a new TeslaClient and promotes it into service.
func (c *ActivatingController) SetRefreshToken(ctx context.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return fmt.Errorf("tesla: refresh token is required")
	}

	current := c.current()
	if activeClient, ok := current.(*TeslaClient); ok {
		return activeClient.SetRefreshToken(ctx, refreshToken)
	}

	cfg := c.cfg
	cfg.TeslaRefreshToken = refreshToken
	activeClient, err := c.factory(cfg, c.logger, c.metrics)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.active = activeClient
	c.mu.Unlock()

	return nil
}

func (c *ActivatingController) GetAPIUsage() APIUsage {
	return c.current().GetAPIUsage()
}

// RestoreAPIUsage seeds the active Tesla client's usage tracker when a real
// client is available.
func (c *ActivatingController) RestoreAPIUsage(u APIUsage) {
	if activeClient, ok := c.current().(*TeslaClient); ok {
		activeClient.RestoreAPIUsage(u)
	}
}
