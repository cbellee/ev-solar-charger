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
	State            string
	AmpsActual       int
	BatteryPct       float64
	TimeToLimitHours float64
	PluggedIn        bool
	IsOnline         bool
	ChargeLimit      int // current charge_limit_soc setting (percent)
	ChargeLimitMin   int // vehicle-reported minimum allowed limit
	ChargeLimitMax   int // vehicle-reported maximum allowed limit
}

// APIUsage tracks Tesla Fleet API call counts for the current billing month.
type APIUsage struct {
	DataCalls              int64     `json:"dataCalls"`
	CommandCalls           int64     `json:"commandCalls"`
	WakeCalls              int64     `json:"wakeCalls"`
	StreamSignals          int64     `json:"streamSignals"`
	MonthStarted           time.Time `json:"monthStarted"`
	EstimatedCost          float64   `json:"estimatedCost"`
	DiscountedDataEligible bool      `json:"discountedDataEligible"`
}

// BillingMonthStart returns the start of the Tesla Fleet API billing month
// containing t. Tesla bills per calendar month in UTC.
func BillingMonthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// APIUsageTracker provides thread-safe tracking of Tesla API usage per billing month.
type APIUsageTracker struct {
	mu            sync.Mutex
	dataCalls     int64
	commandCalls  int64
	wakeCalls     int64
	streamSignals int64
	monthStart    time.Time
	now           func() time.Time
}

// NewAPIUsageTracker creates a new tracker initialised to the current month.
func NewAPIUsageTracker() *APIUsageTracker {
	return newAPIUsageTracker(time.Now)
}

func newAPIUsageTracker(now func() time.Time) *APIUsageTracker {
	return &APIUsageTracker{monthStart: BillingMonthStart(now()), now: now}
}

// resetIfNewMonth zeroes the counters once the billing month has rolled over.
// It runs on reads as well as writes so a snapshot taken after the rollover
// never reports the previous month's totals.
func (t *APIUsageTracker) resetIfNewMonth() {
	monthStart := BillingMonthStart(t.now())
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
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resetIfNewMonth()

	return APIUsage{
		DataCalls:     t.dataCalls,
		CommandCalls:  t.commandCalls,
		WakeCalls:     t.wakeCalls,
		StreamSignals: t.streamSignals,
		MonthStarted:  t.monthStart,
		EstimatedCost: EstimateCost(t.dataCalls, t.commandCalls, t.wakeCalls, t.streamSignals),
	}
}

// EstimateCost returns the Fleet API cost in USD, before the monthly credit,
// for the given call counts.
func EstimateCost(dataCalls, commandCalls, wakeCalls, streamSignals int64) float64 {
	return float64(dataCalls)/500.0 +
		float64(commandCalls)/1000.0 +
		float64(wakeCalls)/50.0 +
		float64(streamSignals)/150000.0
}

// SetCounts seeds the tracker with previously persisted counters. The
// monthStart of the supplied data must match the current billing month
// for the counters to be applied; older snapshots are ignored so a new
// month always starts from zero.
func (t *APIUsageTracker) SetCounts(dataCalls, commandCalls, wakeCalls, streamSignals int64, monthStart time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	currentMonth := BillingMonthStart(t.now())
	if monthStart.IsZero() || BillingMonthStart(monthStart).Before(currentMonth) {
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
