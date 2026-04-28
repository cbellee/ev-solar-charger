package controller

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/inverter"
	"github.com/cbellee/ev-solar-charger/internal/observability"
	"github.com/cbellee/ev-solar-charger/internal/storage"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// State represents the controller's current operating state.
type State string

const (
	StateIdle              State = "idle"
	StateMonitoring        State = "monitoring"
	StateCharging          State = "charging"
	StateStoppedLowSurplus State = "stopped_low_surplus"
	StateWakePending       State = "wake_pending"
	StateError             State = "error"
)

// Mode represents the operating mode.
type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeManual Mode = "manual"
)

// StateSnapshot is a point-in-time snapshot for the web UI.
type StateSnapshot struct {
	State              State     `json:"state"`
	Mode               Mode      `json:"mode"`
	TestMode           bool      `json:"testMode"`
	PVWatts            float64   `json:"pvWatts"`
	GridWatts          float64   `json:"gridWatts"`
	LoadWatts          float64   `json:"loadWatts"`
	SurplusWatts       float64   `json:"surplusWatts"`
	TargetAmps         int       `json:"targetAmps"`
	ActualAmps         int       `json:"actualAmps"`
	BatteryPct         float64   `json:"batteryPct"`
	CarPluggedIn       bool      `json:"carPluggedIn"`
	CarOnline          bool      `json:"carOnline"`
	ChargingState      string    `json:"chargingState"`
	ConsecutiveLow     int       `json:"consecutiveLow"`
	ConsecutiveSurplus int       `json:"consecutiveSurplus"`
	LastUpdate         time.Time `json:"lastUpdate"`
	LastError          string    `json:"lastError"`
}

// Controller manages the solar-to-EV charging logic.
type Controller struct {
	inverter inverter.InverterReader
	vehicle  tesla.VehicleController
	store    storage.Store
	cfg      config.Config
	logger   *slog.Logger
	metrics  *observability.Metrics

	mu                 sync.RWMutex
	state              State
	mode               Mode
	snapshot           StateSnapshot
	consecutiveLow     int
	consecutiveSurplus int
	lastChargeAmps     int
	lastKnownPluggedIn bool
	lastOnlineAt       time.Time

	accumulator     []accSample
	currentMinute   time.Time
	activeSessionID int64
	sessionEnergy   float64
	sessionPeakAmps int
	sessionAmpTotal int64
	sessionSamples  int

	lastTeslaPoll          time.Time
	lastWakeAttempt        time.Time
	lastChargeStartAttempt time.Time
	lastCommandFailure     time.Time
	cachedChargeState      tesla.ChargeState
	hasCachedState         bool

	OnUpdate func(StateSnapshot)
}

type accSample struct {
	pvWatts      float64
	gridWatts    float64
	loadWatts    float64
	surplusWatts float64
	chargeAmps   int
	batteryPct   float64
	state        State
}

var tracer = otel.Tracer("controller")

// New creates a new Controller.
func New(inv inverter.InverterReader, veh tesla.VehicleController, store storage.Store,
	cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) *Controller {
	return &Controller{
		inverter: inv,
		vehicle:  veh,
		store:    store,
		cfg:      cfg,
		logger:   logger,
		metrics:  metrics,
		state:    StateIdle,
		mode:     ModeAuto,
		snapshot: StateSnapshot{
			State:    StateIdle,
			Mode:     ModeAuto,
			TestMode: cfg.TeslaTestMode,
		},
	}
}

// Run starts the control loop, blocking until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.InfoContext(ctx, "controller stopped")
			c.mu.RLock()
			isCharging := c.state == StateCharging
			c.mu.RUnlock()
			if isCharging {
				_ = c.vehicle.StopCharging(context.Background())
			}
			c.flushMinuteAverage(ctx)
			return
		case <-ticker.C:
			c.Tick(ctx)
		}
	}
}

// Tick performs one iteration of the control loop.
func (c *Controller) Tick(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "controller.Tick")
	defer span.End()
	start := time.Now()
	defer func() {
		if c.metrics != nil {
			c.metrics.PollDuration.Record(ctx, time.Since(start).Seconds())
		}
	}()

	// Step 1: Read solar data.
	power, err := c.inverter.GetPowerData(ctx)
	if err != nil {
		c.logger.ErrorContext(ctx, "inverter read failed", "error", err)
		c.transitionTo(ctx, StateError, err.Error())
		return
	}

	if c.cfg.TeslaTestMode {
		c.tickTestMode(ctx, power)
		return
	}

	// Auto-clear StateError after the failure backoff has elapsed so the
	// controller re-evaluates from a clean slate. Without this, the error
	// state is sticky and hides the cause from the user.
	c.maybeClearError(ctx)

	// Step 2: Read vehicle state (cost-aware polling).
	// Tesla Fleet API charges $0.002 per vehicle_data call. We only poll when
	// the controller state warrants it, using cached state for intermediate ticks.
	chargeState := c.getCachedChargeState()
	if c.shouldPollTesla(power.SurplusWatts) {
		fresh, err := c.vehicle.GetChargeState(ctx)
		if err != nil {
			if err == tesla.ErrCarOffline {
				fresh = tesla.ChargeState{IsOnline: false}
			} else {
				c.logger.ErrorContext(ctx, "tesla read failed", "error", err)
				c.transitionTo(ctx, StateError, err.Error())
				return
			}
		}
		chargeState = fresh
		c.setCachedChargeState(fresh)
	}
	c.updateKnownPlugState(chargeState)
	c.recordSessionSample(chargeState)

	// Step 3: Check preconditions.
	if !chargeState.PluggedIn && chargeState.IsOnline {
		c.transitionTo(ctx, StateIdle, "car not plugged in")
		c.resetCounters()
		c.updateSnapshot(power, chargeState, 0)
		c.accumulateSample(power, chargeState, 0, ctx)
		if c.OnUpdate != nil {
			c.OnUpdate(c.GetStateSnapshot())
		}
		return
	}
	if !chargeState.IsOnline && !c.shouldAttemptWake() {
		c.transitionTo(ctx, StateIdle, "car offline and plug state unknown")
		c.resetCounters()
		c.updateSnapshot(power, chargeState, 0)
		c.accumulateSample(power, chargeState, 0, ctx)
		if c.OnUpdate != nil {
			c.OnUpdate(c.GetStateSnapshot())
		}
		return
	}

	// Step 4: Skip auto-control in manual mode.
	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()
	if mode == ModeManual {
		c.updateSnapshot(power, chargeState, 0)
		c.accumulateSample(power, chargeState, 0, ctx)
		if c.OnUpdate != nil {
			c.OnUpdate(c.GetStateSnapshot())
		}
		return
	}

	// Step 5: Calculate available amps.
	availableAmps := c.calculateAvailableAmps(power.SurplusWatts, chargeState)

	// Step 6: State machine decision.
	if availableAmps >= c.cfg.MinChargeAmps {
		c.mu.Lock()
		c.consecutiveLow = 0
		c.consecutiveSurplus++
		surplus := c.consecutiveSurplus
		c.mu.Unlock()

		if !chargeState.IsOnline {
			if surplus >= c.cfg.WakeThresholdPolls {
				c.mu.RLock()
				lastWake := c.lastWakeAttempt
				c.mu.RUnlock()
				if !lastWake.IsZero() && time.Since(lastWake) < c.cfg.WakeRetryInterval {
					// Wake already attempted recently; stay in WakePending and
					// wait for the car to come online without spamming the API.
					c.transitionTo(ctx, StateWakePending, "waiting for car to wake")
				} else {
					c.transitionTo(ctx, StateWakePending, "waking car")
					if err := c.vehicle.WakeUp(ctx); err != nil {
						c.logger.WarnContext(ctx, "wake failed", "error", err)
						c.transitionTo(ctx, StateError, err.Error())
					}
					c.mu.Lock()
					c.lastWakeAttempt = time.Now()
					c.mu.Unlock()
				}
			} else {
				c.transitionTo(ctx, StateMonitoring, "surplus detected, waiting to wake")
			}
			c.updateSnapshot(power, chargeState, availableAmps)
			c.accumulateSample(power, chargeState, availableAmps, ctx)
			if c.OnUpdate != nil {
				c.OnUpdate(c.GetStateSnapshot())
			}
			return
		}

		if chargeState.State != "Charging" {
			if !isActionableNonChargingState(chargeState.State) {
				c.transitionTo(ctx, StateMonitoring, fmt.Sprintf("charge state %q is not actionable", chargeState.State))
			} else if !c.canAttemptChargeStart() {
				c.transitionTo(ctx, StateMonitoring, "waiting for charge-start cooldown")
			} else {
				c.recordChargeStartAttempt()
				if err := c.vehicle.SetChargingAmps(ctx, availableAmps); err != nil {
					c.logger.WarnContext(ctx, "set amps before start failed", "error", err)
					c.recordCommandFailure()
					c.transitionTo(ctx, StateError, err.Error())
				} else if err := c.vehicle.StartCharging(ctx); err != nil {
					c.logger.WarnContext(ctx, "start charging failed", "error", err)
					c.recordCommandFailure()
				} else {
					c.transitionTo(ctx, StateCharging, fmt.Sprintf("started at %dA", availableAmps))
					c.mu.Lock()
					c.lastChargeAmps = availableAmps
					c.mu.Unlock()
				}
			}
		} else {
			c.mu.RLock()
			lastAmps := c.lastChargeAmps
			c.mu.RUnlock()
			ampsDiff := availableAmps - lastAmps
			if ampsDiff < 0 {
				ampsDiff = -ampsDiff
			}
			threshold := c.cfg.AmpsChangeThreshold
			if threshold < 1 {
				threshold = 1
			}
			// Skip if amps are already at the target (defensive — handles
			// the threshold=1 case where ampsDiff>=threshold can be true
			// even though we just sent the same value).
			if availableAmps != lastAmps && ampsDiff >= threshold {
				if err := c.vehicle.SetChargingAmps(ctx, availableAmps); err != nil {
					c.logger.WarnContext(ctx, "set amps failed", "error", err)
					c.recordCommandFailure()
				} else {
					c.mu.Lock()
					c.lastChargeAmps = availableAmps
					c.mu.Unlock()
					c.logger.InfoContext(ctx, "adjusted amps", "amps", availableAmps)
				}
			}
			c.transitionTo(ctx, StateCharging, fmt.Sprintf("charging at %dA", availableAmps))
		}
	} else {
		c.mu.Lock()
		c.consecutiveSurplus = 0
		c.consecutiveLow++
		lowCount := c.consecutiveLow
		c.mu.Unlock()

		if chargeState.State == "Charging" && lowCount >= c.cfg.DeadbandPolls {
			if err := c.vehicle.StopCharging(ctx); err != nil {
				c.logger.WarnContext(ctx, "stop charging failed", "error", err)
				c.recordCommandFailure()
			} else {
				c.transitionTo(ctx, StateStoppedLowSurplus, "insufficient surplus")
				c.mu.Lock()
				c.lastChargeAmps = 0
				c.mu.Unlock()
			}
		} else if chargeState.State == "Charging" {
			c.transitionTo(ctx, StateCharging, fmt.Sprintf("low surplus tick %d/%d", lowCount, c.cfg.DeadbandPolls))
		} else {
			c.transitionTo(ctx, StateMonitoring, "waiting for surplus")
		}
	}

	// Step 7: Update snapshot + accumulate.
	c.updateSnapshot(power, chargeState, availableAmps)
	c.accumulateSample(power, chargeState, availableAmps, ctx)

	// Step 8: Notify SSE subscribers.
	if c.OnUpdate != nil {
		c.OnUpdate(c.GetStateSnapshot())
	}
}

func (c *Controller) tickTestMode(ctx context.Context, power inverter.PowerData) {
	availableAmps := c.calculateAvailableAmps(power.SurplusWatts, tesla.ChargeState{})
	chargeState := tesla.ChargeState{State: "Projected only"}

	if availableAmps >= c.cfg.MinChargeAmps {
		c.mu.Lock()
		c.consecutiveLow = 0
		c.consecutiveSurplus++
		c.mu.Unlock()
		c.transitionTo(ctx, StateMonitoring, fmt.Sprintf("test mode projected %dA", availableAmps))
	} else {
		c.resetCounters()
		c.transitionTo(ctx, StateIdle, "test mode waiting for surplus")
	}

	c.updateSnapshot(power, chargeState, availableAmps)
	c.accumulateSample(power, chargeState, availableAmps, ctx)

	if c.OnUpdate != nil {
		c.OnUpdate(c.GetStateSnapshot())
	}
}

func (c *Controller) calculateAvailableAmps(surplusWatts float64, cs tesla.ChargeState) int {
	surplusAmps := int(math.Floor(surplusWatts / float64(c.cfg.LineVoltage)))
	if cs.State == "Charging" {
		surplusAmps += cs.AmpsActual
	}
	if surplusAmps < 0 {
		surplusAmps = 0
	}
	if surplusAmps > c.cfg.MaxChargeAmps {
		surplusAmps = c.cfg.MaxChargeAmps
	}
	return surplusAmps
}

func (c *Controller) transitionTo(ctx context.Context, newState State, reason string) {
	c.mu.Lock()
	oldState := c.state
	if oldState == newState {
		c.mu.Unlock()
		return
	}
	c.state = newState
	snap := c.snapshot
	c.mu.Unlock()

	c.logger.InfoContext(ctx, "state transition", "from", oldState, "to", newState, "reason", reason)
	if c.metrics != nil {
		c.metrics.StateChanges.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("from", string(oldState)),
				attribute.String("to", string(newState))))
	}

	if c.store != nil {
		_ = c.store.InsertEvent(ctx, storage.Event{
			Timestamp: time.Now(),
			Type:      "state_change",
			Message:   fmt.Sprintf("%s -> %s", oldState, newState),
			Details:   fmt.Sprintf(`{"reason":%q}`, reason),
		})
	}

	if newState == StateCharging && oldState != StateCharging {
		c.mu.Lock()
		c.resetSessionStatsLocked()
		c.mu.Unlock()
		if c.store != nil {
			id, err := c.store.StartSession(ctx, storage.ChargeSession{
				StartTime:    time.Now(),
				StartBattery: snap.BatteryPct,
			})
			if err != nil {
				c.logger.WarnContext(ctx, "failed to start charge session", "error", err)
			} else {
				c.mu.Lock()
				c.activeSessionID = id
				c.mu.Unlock()
			}
		}
	}

	if oldState == StateCharging && newState != StateCharging {
		energyKWh, peakAmps, avgAmps, sessionID := c.sessionSummary()
		if sessionID != 0 && c.store != nil {
			if err := c.store.EndSession(ctx, sessionID, time.Now(), snap.BatteryPct, energyKWh, peakAmps, avgAmps); err != nil {
				c.logger.WarnContext(ctx, "failed to end charge session", "error", err)
			}
			c.mu.Lock()
			c.activeSessionID = 0
			c.resetSessionStatsLocked()
			c.mu.Unlock()
		}
	}
}

func (c *Controller) updateKnownPlugState(cs tesla.ChargeState) {
	if !cs.IsOnline {
		return
	}
	c.mu.Lock()
	c.lastKnownPluggedIn = cs.PluggedIn
	c.lastOnlineAt = time.Now()
	c.mu.Unlock()
}

// isActionableNonChargingState reports whether a non-Charging Tesla
// charging-state value warrants an attempt to start a charge. States like
// "Disconnected" (cable not engaged) or "Complete" (battery full) cannot be
// resolved by sending more commands, so we skip them to avoid API spam.
func isActionableNonChargingState(s string) bool {
	switch s {
	case "Stopped", "NoPower", "":
		return true
	default:
		// "Disconnected", "Complete", "Starting" and any other value fall
		// through here. "Starting" is in-flight; the next poll will
		// re-evaluate. Unknown states are treated as non-actionable to fail
		// safe.
		return false
	}
}

// canAttemptChargeStart returns true if the cooldowns allow another
// SetChargingAmps + StartCharging attempt right now.
func (c *Controller) canAttemptChargeStart() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	if c.cfg.ChargeStartRetry > 0 && !c.lastChargeStartAttempt.IsZero() &&
		now.Sub(c.lastChargeStartAttempt) < c.cfg.ChargeStartRetry {
		return false
	}
	if c.cfg.CommandFailureBackoff > 0 && !c.lastCommandFailure.IsZero() &&
		now.Sub(c.lastCommandFailure) < c.cfg.CommandFailureBackoff {
		return false
	}
	return true
}

func (c *Controller) recordChargeStartAttempt() {
	c.mu.Lock()
	c.lastChargeStartAttempt = time.Now()
	c.mu.Unlock()
}

func (c *Controller) recordCommandFailure() {
	c.mu.Lock()
	c.lastCommandFailure = time.Now()
	c.mu.Unlock()
}

// maybeClearError auto-recovers from StateError once the failure backoff has
// elapsed, transitioning back to StateMonitoring so the next tick re-evaluates
// from a clean slate. The lastError string in the snapshot is preserved for
// the UI by the snapshot updater.
func (c *Controller) maybeClearError(ctx context.Context) {
	c.mu.RLock()
	state := c.state
	lastFailure := c.lastCommandFailure
	c.mu.RUnlock()
	if state != StateError {
		return
	}
	if c.cfg.CommandFailureBackoff <= 0 {
		return
	}
	if lastFailure.IsZero() || time.Since(lastFailure) >= c.cfg.CommandFailureBackoff {
		c.transitionTo(ctx, StateMonitoring, "auto-clearing error after backoff")
	}
}

func (c *Controller) shouldAttemptWake() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.lastKnownPluggedIn {
		return false
	}
	// Treat plug state as unknown if we haven't seen the car online recently.
	// Without this, a car driven away while last-seen plugged in would trigger
	// wake attempts indefinitely.
	if c.cfg.PluggedInStaleAfter > 0 && !c.lastOnlineAt.IsZero() &&
		time.Since(c.lastOnlineAt) > c.cfg.PluggedInStaleAfter {
		return false
	}
	// Don't wake the car outside the allowed daytime window.
	if !c.wakeWindowOpenLocked(time.Now()) {
		return false
	}
	return true
}

// wakeWindowOpenLocked returns true if the local hour is within the configured
// wake window. Caller must hold c.mu (read or write).
func (c *Controller) wakeWindowOpenLocked(now time.Time) bool {
	loc := c.cfg.WakeTimezone
	if loc == nil {
		loc = time.Local
	}
	hour := now.In(loc).Hour()
	return hour >= c.cfg.WakeAllowedStartHour && hour < c.cfg.WakeAllowedEndHour
}

// shouldPollTesla decides whether to make a fresh Tesla API call this tick.
// Tesla Fleet API charges per vehicle_data request ($0.002) and has rate limits
// (60 req/min). We poll more frequently while charging (to track amps/battery)
// and less frequently when idle, to balance cost against solar responsiveness.
func (c *Controller) shouldPollTesla(surplusWatts float64) bool {
	c.mu.RLock()
	state := c.state
	lastPoll := c.lastTeslaPoll
	hasCache := c.hasCachedState
	c.mu.RUnlock()

	if !hasCache {
		return true
	}

	now := time.Now()
	switch state {
	case StateCharging:
		return now.Sub(lastPoll) >= c.cfg.TeslaChargingPollInterval
	case StateWakePending:
		// Re-poll periodically to detect when the car comes online after a
		// wake. Without this, the controller would stay stuck on cached state.
		return now.Sub(lastPoll) >= c.cfg.WakeRetryInterval
	case StateError:
		// Poll periodically while in error so we can recover from transient
		// failures (cable re-engaged, network blip, etc.) without acting on
		// stale cached state.
		if c.cfg.CommandFailureBackoff > 0 {
			return now.Sub(lastPoll) >= c.cfg.CommandFailureBackoff
		}
		return now.Sub(lastPoll) >= c.cfg.TeslaIdlePollInterval
	default:
		surplusAmps := int(math.Floor(surplusWatts / float64(c.cfg.LineVoltage)))
		if surplusAmps >= c.cfg.MinChargeAmps {
			return now.Sub(lastPoll) >= c.cfg.TeslaIdlePollInterval
		}
		return false
	}
}

func (c *Controller) getCachedChargeState() tesla.ChargeState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cachedChargeState
}

func (c *Controller) setCachedChargeState(cs tesla.ChargeState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedChargeState = cs
	c.hasCachedState = true
	c.lastTeslaPoll = time.Now()
}

func (c *Controller) recordSessionSample(cs tesla.ChargeState) {
	if cs.State != "Charging" || cs.AmpsActual <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeSessionID == 0 {
		return
	}

	if cs.AmpsActual > c.sessionPeakAmps {
		c.sessionPeakAmps = cs.AmpsActual
	}
	c.sessionAmpTotal += int64(cs.AmpsActual)
	c.sessionSamples++
	c.sessionEnergy += float64(cs.AmpsActual) * float64(c.cfg.LineVoltage) * c.cfg.PollInterval.Hours() / 1000
}

func (c *Controller) sessionSummary() (float64, int, float64, int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	avgAmps := 0.0
	if c.sessionSamples > 0 {
		avgAmps = float64(c.sessionAmpTotal) / float64(c.sessionSamples)
	}

	return c.sessionEnergy, c.sessionPeakAmps, avgAmps, c.activeSessionID
}

func (c *Controller) resetSessionStatsLocked() {
	c.sessionEnergy = 0
	c.sessionPeakAmps = 0
	c.sessionAmpTotal = 0
	c.sessionSamples = 0
}

func (c *Controller) resetCounters() {
	c.mu.Lock()
	c.consecutiveLow = 0
	c.consecutiveSurplus = 0
	c.mu.Unlock()
}

func (c *Controller) updateSnapshot(power inverter.PowerData, cs tesla.ChargeState, targetAmps int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = StateSnapshot{
		State:              c.state,
		Mode:               c.mode,
		TestMode:           c.cfg.TeslaTestMode,
		PVWatts:            power.PVWatts,
		GridWatts:          power.GridWatts,
		LoadWatts:          power.LoadWatts,
		SurplusWatts:       power.SurplusWatts,
		TargetAmps:         targetAmps,
		ActualAmps:         cs.AmpsActual,
		BatteryPct:         cs.BatteryPct,
		CarPluggedIn:       cs.PluggedIn,
		CarOnline:          cs.IsOnline,
		ChargingState:      cs.State,
		ConsecutiveLow:     c.consecutiveLow,
		ConsecutiveSurplus: c.consecutiveSurplus,
		LastUpdate:         time.Now(),
	}
}

func (c *Controller) accumulateSample(power inverter.PowerData, cs tesla.ChargeState, amps int, ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	thisMinute := time.Now().Truncate(time.Minute)
	if c.currentMinute.IsZero() {
		c.currentMinute = thisMinute
	}

	if thisMinute != c.currentMinute {
		c.mu.Unlock()
		c.flushMinuteAverage(ctx)
		c.mu.Lock()
		c.currentMinute = thisMinute
		c.accumulator = c.accumulator[:0]
	}

	c.accumulator = append(c.accumulator, accSample{
		pvWatts:      power.PVWatts,
		gridWatts:    power.GridWatts,
		loadWatts:    power.LoadWatts,
		surplusWatts: power.SurplusWatts,
		chargeAmps:   amps,
		batteryPct:   cs.BatteryPct,
		state:        c.state,
	})
}

func (c *Controller) flushMinuteAverage(ctx context.Context) {
	c.mu.RLock()
	acc := make([]accSample, len(c.accumulator))
	copy(acc, c.accumulator)
	minute := c.currentMinute
	c.mu.RUnlock()

	if c.store == nil || len(acc) == 0 {
		return
	}

	n := float64(len(acc))
	var sumPV, sumGrid, sumLoad, sumSurplus float64
	for _, s := range acc {
		sumPV += s.pvWatts
		sumGrid += s.gridWatts
		sumLoad += s.loadWatts
		sumSurplus += s.surplusWatts
	}

	last := acc[len(acc)-1]
	reading := storage.Reading{
		Timestamp:    minute,
		PVWatts:      sumPV / n,
		GridWatts:    sumGrid / n,
		LoadWatts:    sumLoad / n,
		SurplusWatts: sumSurplus / n,
		ChargeAmps:   last.chargeAmps,
		BatteryPct:   last.batteryPct,
		State:        string(last.state),
	}

	if err := c.store.InsertReading(ctx, reading); err != nil {
		c.logger.ErrorContext(ctx, "failed to persist reading", "error", err)
	}

	// Persist API usage snapshot for historical analysis.
	usage := c.vehicle.GetAPIUsage()
	usageSnap := storage.APIUsageSnapshot{
		Timestamp:     minute,
		DataCalls:     usage.DataCalls,
		CommandCalls:  usage.CommandCalls,
		WakeCalls:     usage.WakeCalls,
		StreamSignals: usage.StreamSignals,
		EstimatedCost: usage.EstimatedCost,
	}
	if err := c.store.InsertAPIUsage(ctx, usageSnap); err != nil {
		c.logger.ErrorContext(ctx, "failed to persist api usage", "error", err)
	}
}

// SetMode changes the operating mode.
func (c *Controller) SetMode(mode Mode) {
	c.mu.Lock()
	c.mode = mode
	c.snapshot.Mode = mode
	c.mu.Unlock()
}

// ManualSetAmps sets charging amps (manual mode only).
func (c *Controller) ManualSetAmps(ctx context.Context, amps int) error {
	if c.cfg.TeslaTestMode {
		return tesla.ErrCommandsDisabled
	}
	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()
	if mode != ModeManual {
		return fmt.Errorf("controller: manual set amps requires manual mode")
	}
	return c.vehicle.SetChargingAmps(ctx, amps)
}

// ManualStart starts charging (manual mode only).
func (c *Controller) ManualStart(ctx context.Context) error {
	if c.cfg.TeslaTestMode {
		return tesla.ErrCommandsDisabled
	}
	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()
	if mode != ModeManual {
		return fmt.Errorf("controller: manual start requires manual mode")
	}
	return c.vehicle.StartCharging(ctx)
}

// ManualStop stops charging (manual mode only).
func (c *Controller) ManualStop(ctx context.Context) error {
	if c.cfg.TeslaTestMode {
		return tesla.ErrCommandsDisabled
	}
	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()
	if mode != ModeManual {
		return fmt.Errorf("controller: manual stop requires manual mode")
	}
	return c.vehicle.StopCharging(ctx)
}

// GetStateSnapshot returns a thread-safe copy of the current state.
func (c *Controller) GetStateSnapshot() StateSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

// GetAPIUsage returns the current Tesla API usage stats.
func (c *Controller) GetAPIUsage() tesla.APIUsage {
	return c.vehicle.GetAPIUsage()
}
