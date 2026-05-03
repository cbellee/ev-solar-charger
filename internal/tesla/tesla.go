package tesla

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Sentinel errors for known vehicle states.
var (
	ErrCarOffline         = errors.New("tesla: vehicle is offline")
	ErrVehicleUnavailable = errors.New("tesla: vehicle client unavailable")
	ErrNotPluggedIn       = errors.New("tesla: vehicle is not plugged in")
	ErrNotCharging        = errors.New("tesla: vehicle is not charging")
	ErrCommandsDisabled   = errors.New("tesla: commands disabled in test mode")
)

// ChargeState holds the vehicle's current charging information.
type ChargeState struct {
	State          string
	AmpsActual     int
	BatteryPct     float64
	PluggedIn      bool
	IsOnline       bool
	ChargeLimit    int // current charge_limit_soc setting (percent)
	ChargeLimitMin int // vehicle-reported minimum allowed limit
	ChargeLimitMax int // vehicle-reported maximum allowed limit
}

// APIUsage tracks Tesla Fleet API call counts for the current billing month.
type APIUsage struct {
	DataCalls     int64     `json:"dataCalls"`
	CommandCalls  int64     `json:"commandCalls"`
	WakeCalls     int64     `json:"wakeCalls"`
	StreamSignals int64     `json:"streamSignals"`
	MonthStarted  time.Time `json:"monthStarted"`
	EstimatedCost float64   `json:"estimatedCost"`
}

// APIUsageTracker provides thread-safe tracking of Tesla API usage per billing month.
type APIUsageTracker struct {
	mu            sync.RWMutex
	dataCalls     int64
	commandCalls  int64
	wakeCalls     int64
	streamSignals int64
	monthStart    time.Time
}

// NewAPIUsageTracker creates a new tracker initialised to the current month.
func NewAPIUsageTracker() *APIUsageTracker {
	now := time.Now()
	return &APIUsageTracker{
		monthStart: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()),
	}
}

func (t *APIUsageTracker) resetIfNewMonth() {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	if monthStart.After(t.monthStart) {
		t.dataCalls = 0
		t.commandCalls = 0
		t.wakeCalls = 0
		t.streamSignals = 0
		t.monthStart = monthStart
	}
}

// RecordData increments the data call counter.
func (t *APIUsageTracker) RecordData() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resetIfNewMonth()
	t.dataCalls++
}

// RecordCommand increments the command call counter.
func (t *APIUsageTracker) RecordCommand() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resetIfNewMonth()
	t.commandCalls++
}

// RecordWake increments the wake call counter.
func (t *APIUsageTracker) RecordWake() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resetIfNewMonth()
	t.wakeCalls++
}

// Snapshot returns the current API usage.
func (t *APIUsageTracker) Snapshot() APIUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()

	dataCost := float64(t.dataCalls) / 500.0
	commandCost := float64(t.commandCalls) / 1000.0
	wakeCost := float64(t.wakeCalls) / 50.0
	streamCost := float64(t.streamSignals) / 150000.0
	total := dataCost + commandCost + wakeCost + streamCost

	return APIUsage{
		DataCalls:     t.dataCalls,
		CommandCalls:  t.commandCalls,
		WakeCalls:     t.wakeCalls,
		StreamSignals: t.streamSignals,
		MonthStarted:  t.monthStart,
		EstimatedCost: total,
	}
}

// SetCounts seeds the tracker with previously persisted counters. The
// monthStart of the supplied data must match the current calendar month
// for the counters to be applied; older snapshots are ignored so a new
// month always starts from zero.
func (t *APIUsageTracker) SetCounts(dataCalls, commandCalls, wakeCalls, streamSignals int64, monthStart time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	if monthStart.IsZero() || monthStart.Before(currentMonth) {
		return
	}
	t.dataCalls = dataCalls
	t.commandCalls = commandCalls
	t.wakeCalls = wakeCalls
	t.streamSignals = streamSignals
	t.monthStart = currentMonth
}

// VehicleController controls EV charging.
type VehicleController interface {
	GetChargeState(ctx context.Context) (ChargeState, error)
	SetChargingAmps(ctx context.Context, amps int) error
	SetChargeLimit(ctx context.Context, percent int) error
	StartCharging(ctx context.Context) error
	StopCharging(ctx context.Context) error
	WakeUp(ctx context.Context) error
	SetRefreshToken(ctx context.Context, refreshToken string) error
	GetAPIUsage() APIUsage
}
