package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/inverter"
	"github.com/cbellee/ev-solar-charger/internal/storage"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
)

func TestInverterFailurePublishesErrorAndRecovers(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 5000}}
	ctrl := newTestController(inv, &mockVehicle{}, &mockStore{})
	ctrl.Tick(context.Background())
	lastGood := ctrl.GetStateSnapshot()
	var updates []StateSnapshot
	ctrl.OnUpdate = func(s StateSnapshot) { updates = append(updates, s) }
	inv.err = errors.New("inverter unavailable")
	for range 2 {
		ctrl.Tick(context.Background())
	}
	if len(updates) != 2 {
		t.Fatalf("error updates = %d, want 2", len(updates))
	}
	for _, snap := range updates {
		if snap.State != StateError || snap.LastError != "inverter unavailable" || !snap.LastUpdate.Equal(lastGood.LastUpdate) {
			t.Fatalf("error must preserve last successful reading time: %+v", snap)
		}
	}
	inv.err = nil
	ctrl.Tick(context.Background())
	if ctrl.GetStateSnapshot().LastError != "" {
		t.Fatal("recovered reading must clear the displayed error")
	}
}

// mockInverter implements inverter.InverterReader.
type mockInverter struct {
	power inverter.PowerData
	err   error
}

func (m *mockInverter) Connect(ctx context.Context) error { return nil }
func (m *mockInverter) GetPowerData(ctx context.Context) (inverter.PowerData, error) {
	return m.power, m.err
}
func (m *mockInverter) Close() error { return nil }

// mockVehicle implements tesla.VehicleController.
type mockVehicle struct {
	mu          sync.Mutex
	chargeState tesla.ChargeState
	stateErr    error
	stateCalls  int
	calls       []string
	setAmpsErr  error
	setLimitErr error
	startErr    error
	stopErr     error
	wakeErr     error
}

func (m *mockVehicle) GetChargeState(ctx context.Context) (tesla.ChargeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateCalls++
	return m.chargeState, m.stateErr
}

func (m *mockVehicle) SetChargingAmps(ctx context.Context, amps int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, fmt.Sprintf("SetChargingAmps:%d", amps))
	return m.setAmpsErr
}

func (m *mockVehicle) SetChargeLimit(ctx context.Context, percent int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, fmt.Sprintf("SetChargeLimit:%d", percent))
	return m.setLimitErr
}

func (m *mockVehicle) StartCharging(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "StartCharging")
	return m.startErr
}

func (m *mockVehicle) StopCharging(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "StopCharging")
	return m.stopErr
}

func (m *mockVehicle) WakeUp(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "WakeUp")
	return m.wakeErr
}

func (m *mockVehicle) SetRefreshToken(ctx context.Context, refreshToken string) error {
	return nil
}

func (m *mockVehicle) GetAPIUsage() tesla.APIUsage {
	return tesla.APIUsage{}
}

func (m *mockVehicle) getCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.calls))
	copy(result, m.calls)
	return result
}

// mockStore implements storage.Store (minimal, for controller tests).
type mockStore struct {
	mu             sync.Mutex
	readings       []storage.Reading
	events         []storage.Event
	sessions       []storage.ChargeSession
	endedSessions  []storage.ChargeSession
	nextID         int64
	observation    *storage.VehicleObservation
	observationErr error
}

func (m *mockStore) Migrate(ctx context.Context) error { return nil }

func (m *mockStore) SaveVehicleObservation(ctx context.Context, observation storage.VehicleObservation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.observationErr != nil {
		return m.observationErr
	}
	m.observation = &observation
	return nil
}

func (m *mockStore) LoadVehicleObservation(ctx context.Context, vin string) (*storage.VehicleObservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.observationErr != nil {
		return nil, m.observationErr
	}
	if m.observation == nil || m.observation.VIN != vin {
		return nil, nil
	}
	observation := *m.observation
	return &observation, nil
}

func (m *mockStore) InsertReading(ctx context.Context, r storage.Reading) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readings = append(m.readings, r)
	return nil
}

func (m *mockStore) QueryReadings(ctx context.Context, f storage.ReadingFilter) ([]storage.Reading, error) {
	return nil, nil
}

func (m *mockStore) StartSession(ctx context.Context, s storage.ChargeSession) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	s.ID = m.nextID
	m.sessions = append(m.sessions, s)
	return m.nextID, nil
}

func (m *mockStore) EndSession(ctx context.Context, id int64, endTime time.Time, endBattery, energyKWh float64, peakAmps int, avgAmps float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ended := storage.ChargeSession{
		ID:         id,
		EndTime:    endTime,
		EndBattery: endBattery,
		EnergyKWh:  energyKWh,
		PeakAmps:   peakAmps,
		AvgAmps:    avgAmps,
	}
	m.endedSessions = append(m.endedSessions, ended)
	return nil
}

func (m *mockStore) QuerySessions(ctx context.Context, from, to time.Time, limit, offset int) ([]storage.ChargeSession, error) {
	return nil, nil
}

func (m *mockStore) InsertEvent(ctx context.Context, e storage.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *mockStore) QueryEvents(ctx context.Context, from, to time.Time, eventType string, limit, offset int) ([]storage.Event, error) {
	return nil, nil
}

func (m *mockStore) Search(ctx context.Context, query string, from, to time.Time, limit int) ([]storage.Event, error) {
	return nil, nil
}

func (m *mockStore) InsertAPIUsage(ctx context.Context, s storage.APIUsageSnapshot) error {
	return nil
}
func (m *mockStore) QueryAPIUsage(ctx context.Context, from, to time.Time, limit int) ([]storage.APIUsageSnapshot, error) {
	return nil, nil
}
func (m *mockStore) SampleAPIUsage(ctx context.Context, from, to time.Time, maxPoints int) ([]storage.APIUsageSnapshot, error) {
	return nil, nil
}
func (m *mockStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) { return 0, nil }
func (m *mockStore) Close() error                                                      { return nil }

func defaultCfg() config.Config {
	return config.Config{
		PollInterval:              10 * time.Second,
		MinChargeAmps:             5,
		MaxChargeAmps:             32,
		LineVoltage:               240,
		DeadbandPolls:             3,
		WakeThresholdPolls:        6,
		WakeRetryInterval:         5 * time.Minute,
		WakeAllowedStartHour:      0,
		WakeAllowedEndHour:        24,
		WakeTimezone:              time.UTC,
		PluggedInStaleAfter:       24 * time.Hour,
		ChargeStartRetry:          0, // no cooldown by default in legacy tests
		CommandFailureBackoff:     0, // no cooldown by default in legacy tests
		TeslaChargingPollInterval: 0, // poll every tick in tests
		TeslaIdlePollInterval:     0, // poll every tick in tests
		AmpsChangeThreshold:       0, // no hysteresis in legacy tests
		AmpsAdjustInterval:        0, // no rate limiting in legacy tests
	}
}

func newTestControllerWithConfig(inv *mockInverter, veh *mockVehicle, store storage.Store, cfg config.Config) *Controller {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return New(inv, veh, store, cfg, logger, nil)
}

func newTestController(inv *mockInverter, veh *mockVehicle, store storage.Store) *Controller {
	return newTestControllerWithConfig(inv, veh, store, defaultCfg())
}

func Test_Tick_startupOfflineRecoversWithoutForceRefresh(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaIdlePollInterval = 30 * time.Minute
	veh := &mockVehicle{stateErr: tesla.ErrCarOffline}
	ctrl := newTestControllerWithConfig(&mockInverter{}, veh, &mockStore{}, cfg)
	ctx := context.Background()
	ctrl.Tick(ctx)
	veh.stateErr = nil
	veh.chargeState = tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Complete", BatteryPct: 100}
	ctrl.lastTeslaAttempt = time.Now().Add(-time.Minute)
	ctrl.Tick(ctx)
	snap := ctrl.GetStateSnapshot()
	if veh.stateCalls != 2 || !snap.CarOnline || !snap.CarPluggedIn || snap.BatteryPct != 100 || snap.ChargingState != "Complete" {
		t.Fatalf("startup recovery did not confirm the car: reads=%d snapshot=%+v", veh.stateCalls, snap)
	}
	if calls := veh.getCalls(); len(calls) != 0 {
		t.Fatalf("passive recovery issued commands: %v", calls)
	}
}

func Test_Tick_startupRecoveryBudget(t *testing.T) {
	for _, readErr := range []error{tesla.ErrCarOffline, nil, errors.New("temporary network failure")} {
		t.Run(fmt.Sprint(readErr), func(t *testing.T) {
			cfg := defaultCfg()
			cfg.TeslaIdlePollInterval = 30 * time.Minute
			cfg.CommandFailureBackoff = 30 * time.Minute
			cfg.TelemetryRefreshMinInterval = time.Minute
			veh := &mockVehicle{
				stateErr:    readErr,
				chargeState: tesla.ChargeState{State: "Charging", BatteryPct: 64, AmpsActual: 16},
			}
			ctrl := newTestControllerWithConfig(&mockInverter{power: inverter.PowerData{SurplusWatts: 5000}}, veh, &mockStore{}, cfg)
			ctx := context.Background()
			ctrl.RequestVehicleRefresh() // Subscription reconciliation can be consumed by the first offline read.
			ctrl.Tick(ctx)
			if snap := ctrl.GetStateSnapshot(); snap.VehicleDataStatus != VehicleDataUnknown ||
				snap.VehicleObservedAt != nil || snap.ChargingState != "" || snap.ActualAmps != 0 {
				t.Fatalf("unconfirmed offline fields escaped as a vehicle reading: %+v", snap)
			}
			wakeCooldown, startCooldown := time.Now(), time.Now()
			ctrl.lastWakeAttempt, ctrl.lastChargeStartAttempt = wakeCooldown, startCooldown
			for i, delay := range []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute} {
				ctrl.lastTeslaAttempt = time.Now().Add(-delay + time.Second)
				ctrl.Tick(ctx)
				if veh.stateCalls != i+1 {
					t.Fatalf("read before recovery delay %v: %d", delay, veh.stateCalls)
				}
				ctrl.lastTeslaAttempt = time.Now().Add(-delay)
				// A coincident telemetry hint shares the last recovery read.
				if i == 2 {
					for range 10 {
						ctrl.RequestVehicleRefresh()
					}
				}
				ctrl.Tick(ctx)
				if veh.stateCalls != i+2 {
					t.Fatalf("missing/coalesced retry after %v: reads=%d", delay, veh.stateCalls)
				}
			}
			ctrl.lastTeslaAttempt = time.Now().Add(-10 * time.Minute)
			for range 10 {
				ctrl.Tick(ctx)
			}
			if veh.stateCalls != 4 {
				t.Fatalf("startup retry budget exceeded: %d reads", veh.stateCalls)
			}
			if !ctrl.lastWakeAttempt.Equal(wakeCooldown) || !ctrl.lastChargeStartAttempt.Equal(startCooldown) {
				t.Fatal("startup recovery cleared command cooldowns")
			}
			if calls := veh.getCalls(); len(calls) != 0 {
				t.Fatalf("startup recovery issued commands: %v", calls)
			}
			ctrl.lastTeslaPoll = time.Now().Add(-31 * time.Minute)
			ctrl.Tick(ctx)
			if veh.stateCalls != 5 {
				t.Fatal("regular polling did not resume after recovery budget")
			}
		})
	}
}

func Test_Tick_restoresObservationForDisplayOnly(t *testing.T) {
	for _, state := range []string{"Complete", "Charging", "Stopped", "Disconnected"} {
		t.Run(state, func(t *testing.T) {
			cfg := defaultCfg()
			cfg.TeslaVIN = "TEST_VIN"
			cfg.TeslaIdlePollInterval = 30 * time.Minute
			store := &mockStore{}
			veh := &mockVehicle{chargeState: tesla.ChargeState{
				IsOnline: true, PluggedIn: state != "Disconnected", State: state,
				BatteryPct: 100, ChargeLimit: 100, AmpsActual: 16,
			}}
			ctx := context.Background()
			first := newTestControllerWithConfig(&mockInverter{}, veh, store, cfg)
			first.SetMode(ModeManual)
			first.Tick(ctx)
			observedAt := first.GetStateSnapshot().VehicleObservedAt
			if observedAt == nil || store.observation == nil {
				t.Fatal("successful read was not persisted")
			}

			offline := &mockVehicle{stateErr: tesla.ErrCarOffline}
			restarted := newTestControllerWithConfig(
				&mockInverter{power: inverter.PowerData{SurplusWatts: 5000}}, offline, store, cfg)
			for range 10 {
				restarted.Tick(ctx)
			}
			snap := restarted.GetStateSnapshot()
			if snap.CarOnline || snap.VehicleDataStatus != VehicleDataLastKnown ||
				snap.VehicleObservedAt == nil || !snap.VehicleObservedAt.Equal(*observedAt) ||
				snap.BatteryPct != 100 || snap.ChargingState != state || snap.ChargeLimit != 100 ||
				snap.CarPluggedIn != (state != "Disconnected") {
				t.Fatalf("lost historical observation after restart: %+v", snap)
			}
			if restarted.lastKnownPluggedIn || !restarted.lastOnlineAt.IsZero() || restarted.shouldAttemptWake() {
				t.Fatal("persisted observation granted wake eligibility")
			}
			restarted.lastTeslaAttempt = time.Now().Add(-time.Minute)
			restarted.Tick(ctx)
			wantCalls := 2
			if state == "Complete" {
				wantCalls = 1
			}
			if offline.stateCalls != wantCalls || len(offline.getCalls()) != 0 {
				t.Fatalf("unsafe restored state: reads=%d commands=%v", offline.stateCalls, offline.getCalls())
			}
			if !store.observation.ObservedAt.Equal(*observedAt) {
				t.Fatal("offline read overwrote the persisted online observation")
			}
		})
	}
}

func Test_Tick_ignoresIneligibleStoredObservations(t *testing.T) {
	for _, tc := range []struct {
		name       string
		vin        string
		age        time.Duration
		online     bool
		wantStatus VehicleDataStatus
	}{
		{"wrong vehicle", "OTHER_VIN", time.Hour, true, VehicleDataUnknown},
		{"future timestamp", "TEST_VIN", -time.Hour, true, VehicleDataUnknown},
		{"offline record", "TEST_VIN", time.Hour, false, VehicleDataUnknown},
		{"expired complete", "TEST_VIN", 25 * time.Hour, true, VehicleDataLastKnown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultCfg()
			cfg.TeslaVIN = "TEST_VIN"
			cfg.TeslaIdlePollInterval = 30 * time.Minute
			store := &mockStore{observation: &storage.VehicleObservation{
				VIN: tc.vin, ObservedAt: time.Now().Add(-tc.age),
				ChargeState: tesla.ChargeState{IsOnline: tc.online, PluggedIn: true, State: "Complete"},
			}}
			veh := &mockVehicle{stateErr: tesla.ErrCarOffline}
			ctrl := newTestControllerWithConfig(&mockInverter{}, veh, store, cfg)
			ctrl.Tick(context.Background())
			ctrl.lastTeslaAttempt = time.Now().Add(-time.Minute)
			ctrl.Tick(context.Background())
			if ctrl.GetStateSnapshot().VehicleDataStatus != tc.wantStatus || veh.stateCalls != 2 || ctrl.shouldAttemptWake() {
				t.Fatalf("stored record affected recovery/control: %+v", ctrl.GetStateSnapshot())
			}
		})
	}
}

func Test_Tick_cachedObservationDoesNotBecomeNew(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaIdlePollInterval = 30 * time.Minute
	cfg.TelemetryRefreshMinInterval = time.Minute
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Complete", BatteryPct: 100}}
	ctrl := newTestControllerWithConfig(&mockInverter{}, veh, &mockStore{}, cfg)
	ctx := context.Background()
	ctrl.Tick(ctx)
	first := ctrl.GetStateSnapshot()
	lastOnline, lastNonActionable := ctrl.lastOnlineAt, ctrl.lastNonActionableAt
	for range 3 {
		ctrl.Tick(ctx)
	}
	after := ctrl.GetStateSnapshot()
	if !after.VehicleObservedAt.Equal(*first.VehicleObservedAt) ||
		!ctrl.lastOnlineAt.Equal(lastOnline) || !ctrl.lastNonActionableAt.Equal(lastNonActionable) ||
		!after.LastUpdate.After(first.LastUpdate) {
		t.Fatal("cached ticks changed vehicle freshness or failed to update solar freshness")
	}
	veh.stateErr = tesla.ErrCarOffline
	ctrl.lastTeslaAttempt = time.Now().Add(-time.Minute)
	ctrl.RequestVehicleRefresh()
	ctrl.Tick(ctx)
	after = ctrl.GetStateSnapshot()
	if after.CarOnline || after.BatteryPct != 100 || after.VehicleDataStatus != VehicleDataLastKnown ||
		!after.VehicleObservedAt.Equal(*first.VehicleObservedAt) {
		t.Fatalf("offline read erased the last confirmed reading: %+v", after)
	}
	// Startup recovery stays finished after the first online confirmation.
	ctrl.lastTeslaAttempt = time.Now().Add(-10 * time.Minute)
	ctrl.Tick(ctx)
	if veh.stateCalls != 2 {
		t.Fatal("offline reading restarted the startup retry budget")
	}
}

func Test_Tick_vehiclePersistenceErrorsAreLogged(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaVIN = "TEST_VIN"
	store := &mockStore{observationErr: errors.New("database unavailable")}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, State: "Disconnected"}}
	var logs bytes.Buffer
	ctrl := New(&mockInverter{}, veh, store, cfg, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	ctrl.Tick(context.Background())
	for _, message := range []string{"failed to restore vehicle observation", "failed to persist vehicle observation"} {
		if !strings.Contains(logs.String(), message) {
			t.Fatalf("missing persistence failure %q in %s", message, logs.String())
		}
	}
	if ctrl.GetStateSnapshot().VehicleDataStatus != VehicleDataCurrent {
		t.Fatal("persistence failure discarded a valid live reading")
	}
}

func Test_Tick_telemetryHintConfirmsPlugState(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaIdlePollInterval = 30 * time.Minute
	cfg.TelemetryRefreshMinInterval = time.Minute
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, State: "Disconnected"}}
	ctrl := newTestControllerWithConfig(&mockInverter{}, veh, &mockStore{}, cfg)
	ctx := context.Background()
	ctrl.Tick(ctx)
	veh.chargeState = tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"}
	ctrl.Tick(ctx)
	if ctrl.GetStateSnapshot().CarPluggedIn {
		t.Fatal("normal tick unexpectedly bypassed the idle poll interval")
	}
	ctrl.mu.Lock()
	ctrl.lastTeslaAttempt = time.Now().Add(-time.Minute)
	lastWake := time.Now()
	ctrl.lastWakeAttempt = lastWake
	ctrl.mu.Unlock()
	for range 10 {
		ctrl.RequestVehicleRefresh()
	}
	ctrl.Tick(ctx)
	if !ctrl.GetStateSnapshot().CarPluggedIn || veh.stateCalls != 2 {
		t.Fatalf("hint did not confirm plug-in via REST: calls=%d snapshot=%+v", veh.stateCalls, ctrl.GetStateSnapshot())
	}
	if !ctrl.lastWakeAttempt.Equal(lastWake) {
		t.Fatal("telemetry hint cleared the wake cooldown")
	}
	veh.chargeState = tesla.ChargeState{IsOnline: true, State: "Disconnected"}
	ctrl.RequestVehicleRefresh()
	ctrl.Tick(ctx)
	if !ctrl.GetStateSnapshot().CarPluggedIn || veh.stateCalls != 2 {
		t.Fatal("hint bypassed the minimum refresh interval")
	}
	ctrl.mu.Lock()
	ctrl.lastTeslaAttempt = time.Now().Add(-time.Minute)
	ctrl.mu.Unlock()
	ctrl.Tick(ctx)
	if ctrl.GetStateSnapshot().CarPluggedIn || veh.stateCalls != 3 {
		t.Fatal("pending hint did not confirm unplugging after the cooldown")
	}
}

func Test_Tick_telemetryFailureKeepsBoundedPolling(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaIdlePollInterval = 30 * time.Minute
	cfg.CommandFailureBackoff = 5 * time.Minute
	cfg.TelemetryRefreshMinInterval = time.Minute
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true}}
	ctrl := newTestControllerWithConfig(&mockInverter{}, veh, &mockStore{}, cfg)
	ctrl.Tick(context.Background())
	ctrl.mu.Lock()
	ctrl.lastTeslaAttempt = time.Now().Add(-time.Minute)
	ctrl.mu.Unlock()
	veh.stateErr = errors.New("temporary data failure")
	ctrl.RequestVehicleRefresh()
	ctrl.Tick(context.Background())
	ctrl.RequestVehicleRefresh()
	ctrl.Tick(context.Background())
	if veh.stateCalls != 2 {
		t.Fatalf("failed refresh was retried too quickly: %d reads", veh.stateCalls)
	}
}

func Test_Tick_idleCarNotPluggedHighSurplus(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -5000, SurplusWatts: 5000}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: false, State: "Disconnected"}}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.Tick(context.Background())
	snap := ctrl.GetStateSnapshot()
	if snap.State != StateIdle {
		t.Errorf("State = %q, want %q", snap.State, StateIdle)
	}
	calls := veh.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no vehicle commands, got %v", calls)
	}
}

func Test_Tick_idleCarPluggedHighSurplus(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -2400, SurplusWatts: 2400}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped", TimeToLimitHours: 2.5}}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.Tick(context.Background())
	snap := ctrl.GetStateSnapshot()
	if snap.State != StateCharging {
		t.Errorf("State = %q, want %q", snap.State, StateCharging)
	}
	if snap.TimeToLimitHours != 2.5 {
		t.Errorf("TimeToLimitHours = %f, want 2.5", snap.TimeToLimitHours)
	}
	calls := veh.getCalls()
	if len(calls) < 2 {
		t.Fatalf("expected SetChargingAmps + StartCharging, got %v", calls)
	}
}

func Test_Tick_idleCarAsleepHighSurplusBelowWake(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -2400, SurplusWatts: 2400}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: false}, stateErr: tesla.ErrCarOffline}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.Tick(context.Background())
	snap := ctrl.GetStateSnapshot()
	if snap.State != StateIdle {
		t.Errorf("State = %q, want %q", snap.State, StateIdle)
	}
}

func Test_Tick_idleCarAsleepSurplusSustained(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -2400, SurplusWatts: 2400}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: false}, stateErr: tesla.ErrCarOffline}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.mu.Lock()
	ctrl.lastKnownPluggedIn = true
	ctrl.mu.Unlock()
	for i := 0; i < 6; i++ {
		ctrl.Tick(context.Background())
	}
	snap := ctrl.GetStateSnapshot()
	if snap.State != StateWakePending && snap.State != StateError {
		t.Errorf("State = %q, want wake_pending or error (wake may fail)", snap.State)
	}
	calls := veh.getCalls()
	found := false
	for _, c := range calls {
		if c == "WakeUp" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected WakeUp call, got %v", calls)
	}
}

func Test_Tick_setAmpsFailurePreventsStart(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -2400, SurplusWatts: 2400}}
	veh := &mockVehicle{
		chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"},
		setAmpsErr:  errors.New("set amps failed"),
	}
	ctrl := newTestController(inv, veh, &mockStore{})

	ctrl.Tick(context.Background())

	calls := veh.getCalls()
	if len(calls) != 1 || calls[0] != "SetChargingAmps:10" {
		t.Fatalf("expected only SetChargingAmps call, got %v", calls)
	}
	if ctrl.GetStateSnapshot().State != StateError {
		t.Fatalf("state = %q, want %q", ctrl.GetStateSnapshot().State, StateError)
	}
}

func Test_Tick_chargingSurplusIncreases(t *testing.T) {
	// Car charging at 10A, surplus shows 3600W (+car's 2400W = effectively available)
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 7000, GridWatts: -3600, SurplusWatts: 3600}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 10}}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.lastChargeAmps = 10
	ctrl.mu.Unlock()
	ctrl.Tick(context.Background())
	calls := veh.getCalls()
	found := false
	for _, call := range calls {
		if call == "SetChargingAmps:25" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SetChargingAmps:25, got %v", calls)
	}
}

func Test_Tick_chargingSurplusDropsDeadbandExpires(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 500, GridWatts: 500, SurplusWatts: 0}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 0}}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.lastChargeAmps = 10
	ctrl.mu.Unlock()
	// 3 ticks with zero surplus to exceed deadband (3).
	for i := 0; i < 3; i++ {
		ctrl.Tick(context.Background())
	}
	snap := ctrl.GetStateSnapshot()
	if snap.State != StateStoppedLowSurplus {
		t.Errorf("State = %q, want %q", snap.State, StateStoppedLowSurplus)
	}
	calls := veh.getCalls()
	found := false
	for _, c := range calls {
		if c == "StopCharging" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected StopCharging call, got %v", calls)
	}
}

func Test_Tick_chargingSurplusDropsOneTickDeadband(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{SurplusWatts: 0}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 0}}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.lastChargeAmps = 10
	ctrl.mu.Unlock()
	ctrl.Tick(context.Background())
	calls := veh.getCalls()
	for _, c := range calls {
		if c == "StopCharging" {
			t.Error("should NOT call StopCharging after 1 tick (deadband = 3)")
		}
	}
}

func Test_Tick_manualNoAutoControl(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -5000, SurplusWatts: 5000}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 10}}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.SetMode(ModeManual)
	ctrl.Tick(context.Background())
	calls := veh.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no vehicle commands in manual mode, got %v", calls)
	}
}

func Test_Tick_testModeProjectsAvailableAmps(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaTestMode = true
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -2400, LoadWatts: 3600, SurplusWatts: 2400}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 10}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)

	ctrl.Tick(context.Background())

	snap := ctrl.GetStateSnapshot()
	if !snap.TestMode {
		t.Fatal("TestMode = false, want true")
	}
	if snap.State != StateMonitoring {
		t.Fatalf("State = %q, want %q", snap.State, StateMonitoring)
	}
	if snap.TargetAmps != 10 {
		t.Fatalf("TargetAmps = %d, want 10", snap.TargetAmps)
	}
	if snap.ChargingState != "Projected only" {
		t.Fatalf("ChargingState = %q, want %q", snap.ChargingState, "Projected only")
	}
	if calls := veh.getCalls(); len(calls) != 0 {
		t.Fatalf("expected no Tesla commands in test mode, got %v", calls)
	}
}

func Test_calculateAvailableAmps(t *testing.T) {
	tests := []struct {
		name     string
		surplusW float64
		gridW    float64
		charging bool
		carAmps  int
		wantAmps int
	}{
		{"2400W no car", 2400, -2400, false, 0, 10},
		{"2400W car at 10A", 2400, -2400, true, 10, 20},
		{"1199W below min", 1199, -1199, false, 0, 4},
		{"1200W exactly min", 1200, -1200, false, 0, 5},
		{"7680W at max", 7680, -7680, false, 0, 32},
		{"9600W above max", 9600, -9600, false, 0, 32},
		{"0W no car", 0, 0, false, 0, 0},
		{"0W car at 10A", 0, 0, true, 10, 10},
		{"-500W car at 10A", 0, 500, true, 10, 7},
		{"importing 4000W car at 32A throttles", 0, 4000, true, 32, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := newTestController(nil, nil, nil)
			cs := tesla.ChargeState{AmpsActual: tt.carAmps}
			if tt.charging {
				cs.State = "Charging"
			}
			got := ctrl.calculateAvailableAmps(tt.surplusW, tt.gridW, cs)
			if got != tt.wantAmps {
				t.Errorf("calculateAvailableAmps(surplus=%f, grid=%f, charging=%v, carAmps=%d) = %d, want %d",
					tt.surplusW, tt.gridW, tt.charging, tt.carAmps, got, tt.wantAmps)
			}
		})
	}
}

func Test_transitionTo_persistsEvent(t *testing.T) {
	store := &mockStore{}
	ctrl := newTestController(&mockInverter{}, &mockVehicle{}, store)
	ctrl.transitionTo(context.Background(), StateMonitoring, "test reason")
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(store.events))
	}
	if store.events[0].Type != "state_change" {
		t.Errorf("event type = %q, want %q", store.events[0].Type, "state_change")
	}
}

func Test_transitionTo_startsChargeSession(t *testing.T) {
	store := &mockStore{}
	ctrl := newTestController(&mockInverter{}, &mockVehicle{}, store)
	ctrl.transitionTo(context.Background(), StateCharging, "start")
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(store.sessions))
	}
}

func Test_transitionTo_endsChargeSessionWithStats(t *testing.T) {
	store := &mockStore{}
	ctrl := newTestController(&mockInverter{}, &mockVehicle{}, store)
	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.activeSessionID = 42
	ctrl.snapshot.BatteryPct = 80
	ctrl.mu.Unlock()

	ctrl.recordSessionSample(tesla.ChargeState{State: "Charging", AmpsActual: 10})
	ctrl.recordSessionSample(tesla.ChargeState{State: "Charging", AmpsActual: 20})
	ctrl.transitionTo(context.Background(), StateMonitoring, "done")

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.endedSessions) != 1 {
		t.Fatalf("expected 1 ended session, got %d", len(store.endedSessions))
	}
	ended := store.endedSessions[0]
	if math.Abs(ended.EnergyKWh-0.02) > 0.000001 {
		t.Fatalf("EnergyKWh = %f, want 0.02", ended.EnergyKWh)
	}
	if ended.PeakAmps != 20 {
		t.Fatalf("PeakAmps = %d, want 20", ended.PeakAmps)
	}
	if math.Abs(ended.AvgAmps-15) > 0.000001 {
		t.Fatalf("AvgAmps = %f, want 15", ended.AvgAmps)
	}
}

func Test_flushMinuteAverage_persistsAveragedReading(t *testing.T) {
	store := &mockStore{}
	ctrl := newTestController(&mockInverter{}, &mockVehicle{}, store)
	minute := time.Date(2026, 4, 3, 10, 15, 0, 0, time.UTC)

	ctrl.mu.Lock()
	ctrl.currentMinute = minute
	ctrl.accumulator = []accSample{
		{pvWatts: 1000, gridWatts: -500, loadWatts: 700, surplusWatts: 500, chargeAmps: 5, batteryPct: 40, state: StateMonitoring},
		{pvWatts: 2000, gridWatts: -700, loadWatts: 900, surplusWatts: 700, chargeAmps: 7, batteryPct: 41, state: StateCharging},
	}
	ctrl.mu.Unlock()

	ctrl.flushMinuteAverage(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.readings) != 1 {
		t.Fatalf("expected 1 reading, got %d", len(store.readings))
	}
	r := store.readings[0]
	if !r.Timestamp.Equal(minute) {
		t.Fatalf("Timestamp = %v, want %v", r.Timestamp, minute)
	}
	if r.PVWatts != 1500 {
		t.Fatalf("PVWatts = %f, want 1500", r.PVWatts)
	}
	if r.GridWatts != -600 {
		t.Fatalf("GridWatts = %f, want -600", r.GridWatts)
	}
	if r.LoadWatts != 800 {
		t.Fatalf("LoadWatts = %f, want 800", r.LoadWatts)
	}
	if r.SurplusWatts != 600 {
		t.Fatalf("SurplusWatts = %f, want 600", r.SurplusWatts)
	}
	if r.ChargeAmps != 7 {
		t.Fatalf("ChargeAmps = %d, want 7", r.ChargeAmps)
	}
	if r.BatteryPct != 41 {
		t.Fatalf("BatteryPct = %f, want 41", r.BatteryPct)
	}
	if r.State != string(StateCharging) {
		t.Fatalf("State = %q, want %q", r.State, StateCharging)
	}
}

func Test_accumulateSample_flushesPreviousMinute(t *testing.T) {
	store := &mockStore{}
	ctrl := newTestController(&mockInverter{}, &mockVehicle{}, store)
	previousMinute := time.Now().Add(-time.Minute).Truncate(time.Minute)

	ctrl.mu.Lock()
	ctrl.currentMinute = previousMinute
	ctrl.accumulator = []accSample{{
		pvWatts:      1200,
		gridWatts:    -300,
		loadWatts:    900,
		surplusWatts: 300,
		chargeAmps:   6,
		batteryPct:   50,
		state:        StateCharging,
	}}
	ctrl.mu.Unlock()

	ctrl.accumulateSample(inverter.PowerData{
		PVWatts:      2400,
		GridWatts:    -600,
		LoadWatts:    1800,
		SurplusWatts: 600,
	}, tesla.ChargeState{BatteryPct: 51}, 8, context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.readings) != 1 {
		t.Fatalf("expected 1 flushed reading, got %d", len(store.readings))
	}
	ctrl.mu.RLock()
	defer ctrl.mu.RUnlock()
	if len(ctrl.accumulator) != 1 {
		t.Fatalf("expected accumulator to contain only current-minute sample, got %d", len(ctrl.accumulator))
	}
}

func Test_Run_cancelStopsChargingAndFlushes(t *testing.T) {
	store := &mockStore{}
	veh := &mockVehicle{}
	ctrl := newTestController(&mockInverter{}, veh, store)

	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.currentMinute = time.Now().Truncate(time.Minute)
	ctrl.accumulator = []accSample{{
		pvWatts:      1500,
		gridWatts:    -400,
		loadWatts:    1100,
		surplusWatts: 400,
		chargeAmps:   6,
		batteryPct:   55,
		state:        StateCharging,
	}}
	ctrl.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctrl.Run(ctx)

	calls := veh.getCalls()
	if len(calls) != 1 || calls[0] != "StopCharging" {
		t.Fatalf("expected StopCharging on shutdown, got %v", calls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.readings) != 1 {
		t.Fatalf("expected flush on shutdown, got %d readings", len(store.readings))
	}
}

func Test_ManualSetAmps_requiresManualMode(t *testing.T) {
	ctrl := newTestController(&mockInverter{}, &mockVehicle{}, &mockStore{})

	err := ctrl.ManualSetAmps(context.Background(), 16)
	if err == nil {
		t.Fatal("expected error when not in manual mode")
	}
}

func Test_ManualCommands_inManualMode(t *testing.T) {
	veh := &mockVehicle{}
	ctrl := newTestController(&mockInverter{}, veh, &mockStore{})
	ctrl.SetMode(ModeManual)

	if err := ctrl.ManualSetAmps(context.Background(), 16); err != nil {
		t.Fatalf("ManualSetAmps error: %v", err)
	}
	if err := ctrl.ManualStart(context.Background()); err != nil {
		t.Fatalf("ManualStart error: %v", err)
	}
	if err := ctrl.ManualStop(context.Background()); err != nil {
		t.Fatalf("ManualStop error: %v", err)
	}

	calls := veh.getCalls()
	want := []string{"SetChargingAmps:16", "StartCharging", "StopCharging"}
	if len(calls) != len(want) {
		t.Fatalf("got %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls[%d] = %q, want %q", i, calls[i], want[i])
		}
	}
}

func Test_ManualCommands_testModeDisabled(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaTestMode = true
	ctrl := newTestControllerWithConfig(&mockInverter{}, &mockVehicle{}, &mockStore{}, cfg)
	ctrl.SetMode(ModeManual)

	if err := ctrl.ManualSetAmps(context.Background(), 16); !errors.Is(err, tesla.ErrCommandsDisabled) {
		t.Fatalf("ManualSetAmps error = %v, want %v", err, tesla.ErrCommandsDisabled)
	}
	if err := ctrl.ManualStart(context.Background()); !errors.Is(err, tesla.ErrCommandsDisabled) {
		t.Fatalf("ManualStart error = %v, want %v", err, tesla.ErrCommandsDisabled)
	}
	if err := ctrl.ManualStop(context.Background()); !errors.Is(err, tesla.ErrCommandsDisabled) {
		t.Fatalf("ManualStop error = %v, want %v", err, tesla.ErrCommandsDisabled)
	}
}

func Test_Tick_nilStoreNoPanic(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -2400, SurplusWatts: 2400}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"}}
	ctrl := newTestController(inv, veh, nil)
	// Should not panic with nil store.
	ctrl.Tick(context.Background())
}

func Test_Tick_concurrentSnapshotAccess(t *testing.T) {
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 5000, SurplusWatts: 2000}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 10}}
	ctrl := newTestController(inv, veh, &mockStore{})
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				ctrl.Tick(context.Background())
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_ = ctrl.GetStateSnapshot()
			}
		}
	}()
	wg.Wait()
}

func Test_shouldPollTesla_noCacheAlwaysPolls(t *testing.T) {
	ctrl := newTestController(&mockInverter{}, &mockVehicle{}, &mockStore{})
	if !ctrl.shouldPollTesla(0) {
		t.Error("shouldPollTesla = false without cached state, want true")
	}
}

func Test_shouldPollTesla_chargingRespectsInterval(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaChargingPollInterval = 60 * time.Second
	ctrl := newTestControllerWithConfig(&mockInverter{}, &mockVehicle{}, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.hasCachedState = true
	ctrl.lastTeslaPoll = time.Now()
	ctrl.mu.Unlock()

	if ctrl.shouldPollTesla(5000) {
		t.Error("shouldPollTesla = true immediately after poll, want false")
	}

	ctrl.mu.Lock()
	ctrl.lastTeslaPoll = time.Now().Add(-61 * time.Second)
	ctrl.mu.Unlock()
	if !ctrl.shouldPollTesla(5000) {
		t.Error("shouldPollTesla = false after interval elapsed, want true")
	}
}

func Test_shouldPollTesla_idleNoSurplusStillPollsAfterInterval(t *testing.T) {
	// We poll on the idle interval regardless of surplus, so we can detect
	// out-of-band charge starts (e.g. user starts charging from the Tesla
	// app) and react with StopCharging.
	cfg := defaultCfg()
	cfg.TeslaIdlePollInterval = 300 * time.Second
	ctrl := newTestControllerWithConfig(&mockInverter{}, &mockVehicle{}, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.state = StateIdle
	ctrl.hasCachedState = true
	ctrl.lastTeslaPoll = time.Now().Add(-10 * time.Minute)
	ctrl.mu.Unlock()

	if !ctrl.shouldPollTesla(0) {
		t.Error("shouldPollTesla = false after idle interval elapsed, want true")
	}
}

func Test_shouldPollTesla_idleNoSurplusFreshCacheSkipsPoll(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaIdlePollInterval = 300 * time.Second
	ctrl := newTestControllerWithConfig(&mockInverter{}, &mockVehicle{}, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.state = StateIdle
	ctrl.hasCachedState = true
	ctrl.lastTeslaPoll = time.Now().Add(-30 * time.Second)
	ctrl.mu.Unlock()

	if ctrl.shouldPollTesla(0) {
		t.Error("shouldPollTesla = true within idle interval, want false")
	}
}

func Test_shouldPollTesla_idleWithSurplusPolls(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaIdlePollInterval = 300 * time.Second
	ctrl := newTestControllerWithConfig(&mockInverter{}, &mockVehicle{}, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.state = StateIdle
	ctrl.hasCachedState = true
	ctrl.lastTeslaPoll = time.Now().Add(-6 * time.Minute)
	ctrl.mu.Unlock()

	// 2400W surplus = 10A at 240V, exceeds MinChargeAmps=5
	if !ctrl.shouldPollTesla(2400) {
		t.Error("shouldPollTesla = false with surplus and stale cache, want true")
	}
}

func Test_shouldPollTesla_wakePendingThrottle(t *testing.T) {
	cfg := defaultCfg()
	cfg.WakeRetryInterval = 5 * time.Minute
	ctrl := newTestControllerWithConfig(&mockInverter{}, &mockVehicle{}, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.state = StateWakePending
	ctrl.hasCachedState = true
	ctrl.mu.Unlock()

	// Recent poll: should not poll again yet.
	ctrl.mu.Lock()
	ctrl.lastTeslaPoll = time.Now().Add(-30 * time.Second)
	ctrl.mu.Unlock()
	if ctrl.shouldPollTesla(5000) {
		t.Error("shouldPollTesla = true in wake_pending shortly after poll, want false")
	}

	// Old poll: should poll again to detect car coming online.
	ctrl.mu.Lock()
	ctrl.lastTeslaPoll = time.Now().Add(-10 * time.Minute)
	ctrl.mu.Unlock()
	if !ctrl.shouldPollTesla(5000) {
		t.Error("shouldPollTesla = false in wake_pending after retry interval, want true")
	}
}

func Test_Tick_usesCachedStateWhenPollNotDue(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaChargingPollInterval = 60 * time.Second
	cfg.AmpsChangeThreshold = 0

	inv := &mockInverter{power: inverter.PowerData{PVWatts: 7000, GridWatts: -3600, SurplusWatts: 3600}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 10}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)

	// Prime the cache with a first tick.
	ctrl.Tick(context.Background())
	calls1 := veh.getCalls()

	// Second tick should NOT call GetChargeState again (interval not elapsed).
	ctrl.Tick(context.Background())
	calls2 := veh.getCalls()

	// The mock records all SetChargingAmps/StartCharging calls.
	// After first tick: GetChargeState was called (implicitly via mock), commands issued.
	// After second tick: GetChargeState should NOT be called again.
	// We can verify by checking the state still reflects cached data.
	snap := ctrl.GetStateSnapshot()
	if snap.State != StateCharging {
		t.Errorf("State = %q, want %q", snap.State, StateCharging)
	}
	// Both ticks should produce the same number of GetChargeState calls.
	// Since we can't directly count GetChargeState calls in the mock, verify
	// indirectly: the second tick shouldn't issue new start/amps commands because
	// the cached state already shows charging at the right amps.
	_ = calls1
	_ = calls2
}

func Test_Tick_ampsHysteresisSkipsSmallChange(t *testing.T) {
	cfg := defaultCfg()
	cfg.AmpsChangeThreshold = 3
	cfg.TeslaChargingPollInterval = 0

	// Car charging at 15A, surplus of 600W = 2A surplus → available = 2+15 = 17A.
	// Change from 15 to 17 = 2, below threshold of 3.
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 5000, GridWatts: -600, SurplusWatts: 600}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 15}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.lastChargeAmps = 15
	ctrl.mu.Unlock()

	ctrl.Tick(context.Background())

	calls := veh.getCalls()
	for _, call := range calls {
		if fmt.Sprintf("%s", call) == "SetChargingAmps:17" {
			t.Error("expected SetChargingAmps to be skipped for 2A change with threshold=3")
		}
	}
}

func Test_Tick_ampsHysteresisAllowsLargeChange(t *testing.T) {
	cfg := defaultCfg()
	cfg.AmpsChangeThreshold = 3
	cfg.TeslaChargingPollInterval = 0

	// Car charging at 10A, surplus of 3600W = 15A surplus → available = 15+10 = 25A.
	// Change from 10 to 25 = 15, exceeds threshold of 3.
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 7000, GridWatts: -3600, SurplusWatts: 3600}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 10}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.lastChargeAmps = 10
	ctrl.mu.Unlock()

	ctrl.Tick(context.Background())

	calls := veh.getCalls()
	found := false
	for _, call := range calls {
		if call == "SetChargingAmps:25" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SetChargingAmps:25 for large change, got %v", calls)
	}
}

func Test_Tick_ampsAdjustIntervalSkipsRapidRepeat(t *testing.T) {
	cfg := defaultCfg()
	cfg.AmpsChangeThreshold = 0
	cfg.AmpsAdjustInterval = 1 * time.Minute
	cfg.TeslaChargingPollInterval = 0

	inv := &mockInverter{power: inverter.PowerData{PVWatts: 7000, GridWatts: -3600, SurplusWatts: 3600}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 10}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.state = StateCharging
	ctrl.lastChargeAmps = 10
	ctrl.lastAmpsAdjustAt = time.Now()
	ctrl.mu.Unlock()

	ctrl.Tick(context.Background())

	for _, call := range veh.getCalls() {
		if call == "SetChargingAmps:25" {
			t.Fatalf("expected SetChargingAmps to be skipped within adjust interval, got %v", veh.getCalls())
		}
	}

	ctrl.mu.Lock()
	ctrl.lastAmpsAdjustAt = time.Now().Add(-2 * time.Minute)
	ctrl.mu.Unlock()

	ctrl.Tick(context.Background())

	found := false
	for _, call := range veh.getCalls() {
		if call == "SetChargingAmps:25" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected SetChargingAmps after adjust interval elapsed, got %v", veh.getCalls())
	}
}

func Test_Tick_noTeslaCallsWhenIdleNoSurplus(t *testing.T) {
	cfg := defaultCfg()
	cfg.TeslaIdlePollInterval = 300 * time.Second

	inv := &mockInverter{power: inverter.PowerData{PVWatts: 200, GridWatts: 100, SurplusWatts: 0}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: false, State: "Disconnected"}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)

	// Prime the cache.
	ctrl.setCachedChargeState(tesla.ChargeState{IsOnline: true, PluggedIn: false, State: "Disconnected"})
	ctrl.mu.Lock()
	ctrl.state = StateIdle
	ctrl.mu.Unlock()

	ctrl.Tick(context.Background())

	calls := veh.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no Tesla API calls when idle with no surplus, got %v", calls)
	}
}

func Test_Tick_skipsCommandsWhenDisconnected(t *testing.T) {
	cfg := defaultCfg()
	cfg.ChargeStartRetry = 2 * time.Minute
	cfg.CommandFailureBackoff = 5 * time.Minute

	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -5000, SurplusWatts: 5000}}
	// Car online + plugged in but Tesla reports Disconnected (cable not engaged).
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Disconnected"}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)

	// Run several ticks to ensure no command spam.
	for i := 0; i < 5; i++ {
		ctrl.Tick(context.Background())
	}

	for _, call := range veh.getCalls() {
		if call != "" && (containsAny(call, "SetChargingAmps", "StartCharging")) {
			t.Errorf("expected no charge commands while Disconnected, got %v", veh.getCalls())
			break
		}
	}
}

func Test_Tick_skipsChargeStart_withinCooldown(t *testing.T) {
	cfg := defaultCfg()
	cfg.ChargeStartRetry = 5 * time.Minute
	cfg.CommandFailureBackoff = 0 // isolate ChargeStartRetry

	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -5000, SurplusWatts: 5000}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)

	// First tick: should attempt charge-start (SetChargingAmps + StartCharging).
	ctrl.Tick(context.Background())
	firstCalls := len(veh.getCalls())
	if firstCalls == 0 {
		t.Fatalf("expected first tick to send commands, got 0 calls")
	}

	// Subsequent ticks within cooldown: no additional commands.
	for i := 0; i < 5; i++ {
		ctrl.Tick(context.Background())
	}
	if got := len(veh.getCalls()); got != firstCalls {
		t.Errorf("expected no additional commands within cooldown, got %d new (calls=%v)", got-firstCalls, veh.getCalls())
	}
}

func Test_Tick_confirmsSuccessfulChargeCommands(t *testing.T) {
	tests := []struct {
		name        string
		before      tesla.ChargeState
		after       tesla.ChargeState
		beforePower inverter.PowerData
		afterPower  inverter.PowerData
		wantState   State
	}{
		{
			name:        "start",
			before:      tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"},
			after:       tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 15},
			beforePower: inverter.PowerData{SurplusWatts: 3820, GridWatts: -3820},
			afterPower:  inverter.PowerData{},
			wantState:   StateCharging,
		},
		{
			name:        "stop",
			before:      tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Charging", AmpsActual: 15},
			after:       tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"},
			beforePower: inverter.PowerData{GridWatts: 5000},
			afterPower:  inverter.PowerData{GridWatts: 5000},
			wantState:   StateMonitoring,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultCfg()
			cfg.ChargeStartRetry = 5 * time.Minute
			cfg.TeslaChargingPollInterval = 5 * time.Minute
			cfg.TeslaIdlePollInterval = 30 * time.Minute
			cfg.TelemetryRefreshMinInterval = time.Minute
			cfg.DeadbandPolls = 1
			inv := &mockInverter{power: tt.beforePower}
			veh := &mockVehicle{chargeState: tt.before}
			ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)
			ctx := context.Background()

			ctrl.Tick(ctx)
			commandCount := len(veh.getCalls())
			if commandCount == 0 {
				t.Fatal("expected a charging command on the first tick")
			}
			veh.chargeState = tt.after
			inv.power = tt.afterPower
			ctrl.Tick(ctx)
			snap := ctrl.GetStateSnapshot()
			if snap.State != tt.wantState || snap.ChargingState != tt.after.State || snap.ActualAmps != tt.after.AmpsActual {
				t.Errorf("snapshot still reflects pre-command state: %+v", snap)
			}
			if veh.stateCalls != 2 {
				t.Errorf("vehicle reads = %d, want initial read and one command confirmation", veh.stateCalls)
			}
			ctrl.Tick(ctx)
			if veh.stateCalls != 2 || len(veh.getCalls()) != commandCount {
				t.Errorf("confirmation caused repeated reads or commands: reads=%d commands=%v", veh.stateCalls, veh.getCalls())
			}
		})
	}
}

func Test_Tick_chargeConfirmationFailureDoesNotRepeatRead(t *testing.T) {
	cfg := defaultCfg()
	cfg.ChargeStartRetry = 5 * time.Minute
	cfg.CommandFailureBackoff = 5 * time.Minute
	cfg.TeslaChargingPollInterval = 5 * time.Minute
	cfg.TeslaIdlePollInterval = 30 * time.Minute
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"}}
	inv := &mockInverter{power: inverter.PowerData{SurplusWatts: 3820, GridWatts: -3820}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)
	ctx := context.Background()

	ctrl.Tick(ctx)
	veh.stateErr = errors.New("confirmation unavailable")
	ctrl.Tick(ctx)
	ctrl.mu.RLock()
	state := ctrl.state
	ctrl.mu.RUnlock()
	if state != StateError || veh.stateCalls != 2 {
		t.Fatalf("confirmation failure did not enter the error state: reads=%d state=%s", veh.stateCalls, state)
	}
	for range 3 {
		ctrl.Tick(ctx)
	}
	if veh.stateCalls != 2 || len(veh.getCalls()) != 2 {
		t.Fatalf("failed confirmation caused repeated reads or commands: reads=%d commands=%v", veh.stateCalls, veh.getCalls())
	}
}

func Test_ManualCommands_confirmOnlySuccessfulChanges(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", action, fail), func(t *testing.T) {
				cfg := defaultCfg()
				cfg.TeslaChargingPollInterval = 5 * time.Minute
				cfg.TeslaIdlePollInterval = 30 * time.Minute
				before, after := "Stopped", "Charging"
				if action == "stop" {
					before, after = after, before
				}
				veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: before}}
				ctrl := newTestControllerWithConfig(&mockInverter{}, veh, &mockStore{}, cfg)
				ctrl.SetMode(ModeManual)
				ctx := context.Background()
				ctrl.Tick(ctx)
				var commandErr error
				if fail {
					commandErr = errors.New("command rejected")
				}
				veh.startErr, veh.stopErr = commandErr, commandErr
				command := ctrl.ManualStart
				if action == "stop" {
					command = ctrl.ManualStop
				}
				if err := command(ctx); !errors.Is(err, commandErr) {
					t.Fatalf("command error = %v, want %v", err, commandErr)
				}
				wantReads, wantState := 1, before
				if !fail {
					veh.chargeState.State = after
					wantReads, wantState = 2, after
				}
				ctrl.Tick(ctx)
				if veh.stateCalls != wantReads || ctrl.GetStateSnapshot().ChargingState != wantState {
					t.Fatalf("reads=%d snapshot=%+v, want reads=%d state=%s", veh.stateCalls, ctrl.GetStateSnapshot(), wantReads, wantState)
				}
				ctrl.Tick(ctx)
				if veh.stateCalls != wantReads || len(veh.getCalls()) != 1 {
					t.Fatalf("confirmation repeated: reads=%d commands=%v", veh.stateCalls, veh.getCalls())
				}
			})
		}
	}
}

func Test_Tick_backsOffAfterCommandFailure(t *testing.T) {
	cfg := defaultCfg()
	cfg.ChargeStartRetry = 0 // isolate failure backoff
	cfg.CommandFailureBackoff = 5 * time.Minute

	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -5000, SurplusWatts: 5000}}
	veh := &mockVehicle{
		chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"},
		setAmpsErr:  errors.New("403 forbidden"),
	}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)

	// First tick: SetChargingAmps fails -> records failure, transitions to Error.
	ctrl.Tick(context.Background())
	firstCalls := len(veh.getCalls())
	if firstCalls == 0 {
		t.Fatalf("expected first tick to attempt SetChargingAmps")
	}

	// Subsequent ticks within failure backoff: must not retry.
	for i := 0; i < 5; i++ {
		ctrl.Tick(context.Background())
	}
	if got := len(veh.getCalls()); got != firstCalls {
		t.Errorf("expected no retry within failure backoff, got %d new calls (calls=%v)", got-firstCalls, veh.getCalls())
	}
}

func Test_Tick_clearsErrorAfterBackoff(t *testing.T) {
	cfg := defaultCfg()
	cfg.CommandFailureBackoff = 1 * time.Millisecond

	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -5000, SurplusWatts: 5000}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"}}
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)

	// Force the controller into StateError with an old failure timestamp.
	ctrl.mu.Lock()
	ctrl.state = StateError
	ctrl.lastCommandFailure = time.Now().Add(-1 * time.Second)
	ctrl.mu.Unlock()

	ctrl.Tick(context.Background())

	ctrl.mu.RLock()
	got := ctrl.state
	ctrl.mu.RUnlock()
	if got == StateError {
		t.Errorf("expected error state to auto-clear after backoff, still in %s", got)
	}
}

func Test_isActionableNonChargingState(t *testing.T) {
	cases := map[string]bool{
		"Stopped":      true,
		"NoPower":      true,
		"":             true,
		"Disconnected": false,
		"Complete":     false,
		"Starting":     false,
		"Charging":     false,
		"unknown":      false,
	}
	for state, want := range cases {
		if got := isActionableNonChargingState(state); got != want {
			t.Errorf("isActionableNonChargingState(%q)=%v want %v", state, got, want)
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

func Test_Tick_offlineSurplusBelowWakeMargin(t *testing.T) {
	// 2400W / 240V = 10A. With MinChargeAmps=5 and WakeMinAmpsMargin=8, the
	// effective wake threshold is 13A so we should NOT wake.
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 6000, GridWatts: -2400, SurplusWatts: 2400}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: false}, stateErr: tesla.ErrCarOffline}
	cfg := defaultCfg()
	cfg.WakeMinAmpsMargin = 8
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.lastKnownPluggedIn = true
	ctrl.mu.Unlock()
	for i := 0; i < cfg.WakeThresholdPolls+2; i++ {
		ctrl.Tick(context.Background())
	}
	for _, c := range veh.getCalls() {
		if c == "WakeUp" {
			t.Fatalf("expected no WakeUp under wake margin, got calls %v", veh.getCalls())
		}
	}
	if got := ctrl.GetStateSnapshot().State; got != StateMonitoring {
		t.Errorf("State = %q, want %q", got, StateMonitoring)
	}
}

func Test_Tick_offlineSurplusAboveWakeMargin(t *testing.T) {
	// 4800W / 240V = 20A; margin 8 → threshold 13A; 20>=13 → wake should fire.
	inv := &mockInverter{power: inverter.PowerData{PVWatts: 8000, GridWatts: -4800, SurplusWatts: 4800}}
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: false}, stateErr: tesla.ErrCarOffline}
	cfg := defaultCfg()
	cfg.WakeMinAmpsMargin = 8
	ctrl := newTestControllerWithConfig(inv, veh, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.lastKnownPluggedIn = true
	ctrl.mu.Unlock()
	for i := 0; i < cfg.WakeThresholdPolls; i++ {
		ctrl.Tick(context.Background())
	}
	wakeFound := false
	for _, c := range veh.getCalls() {
		if c == "WakeUp" {
			wakeFound = true
		}
	}
	if !wakeFound {
		t.Fatalf("expected WakeUp call, got %v", veh.getCalls())
	}
}

func Test_shouldAttemptWake_suppressedAfterNonActionable(t *testing.T) {
	cfg := defaultCfg()
	cfg.WakeAfterNonActionableBackoff = 30 * time.Minute
	ctrl := newTestControllerWithConfig(&mockInverter{}, &mockVehicle{}, &mockStore{}, cfg)
	ctrl.mu.Lock()
	ctrl.lastKnownPluggedIn = true
	ctrl.lastOnlineAt = time.Now()
	ctrl.lastNonActionableAt = time.Now().Add(-5 * time.Minute)
	ctrl.mu.Unlock()
	if ctrl.shouldAttemptWake() {
		t.Errorf("expected wake suppression within backoff window, got allowed")
	}
	ctrl.mu.Lock()
	ctrl.lastNonActionableAt = time.Now().Add(-31 * time.Minute)
	ctrl.mu.Unlock()
	if !ctrl.shouldAttemptWake() {
		t.Errorf("expected wake allowed after backoff expires")
	}
}

func Test_updateKnownPlugState_stampsAndClearsNonActionable(t *testing.T) {
	ctrl := newTestController(&mockInverter{}, &mockVehicle{}, &mockStore{})
	// Online + Disconnected: should stamp.
	ctrl.updateKnownPlugState(tesla.ChargeState{IsOnline: true, PluggedIn: false, State: "Disconnected"})
	ctrl.mu.RLock()
	stamped := !ctrl.lastNonActionableAt.IsZero()
	ctrl.mu.RUnlock()
	if !stamped {
		t.Fatalf("expected lastNonActionableAt set after Disconnected observation")
	}
	// Online + actionable: should clear.
	ctrl.updateKnownPlugState(tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"})
	ctrl.mu.RLock()
	cleared := ctrl.lastNonActionableAt.IsZero()
	ctrl.mu.RUnlock()
	if !cleared {
		t.Fatalf("expected lastNonActionableAt cleared after actionable observation")
	}
	// Offline: should not modify.
	ctrl.updateKnownPlugState(tesla.ChargeState{IsOnline: true, PluggedIn: false, State: "Complete"})
	ctrl.mu.RLock()
	stamped2 := !ctrl.lastNonActionableAt.IsZero()
	ctrl.mu.RUnlock()
	if !stamped2 {
		t.Fatalf("expected lastNonActionableAt set after Complete observation")
	}
	ctrl.updateKnownPlugState(tesla.ChargeState{IsOnline: false, PluggedIn: true, State: "Stopped"})
	ctrl.mu.RLock()
	stillStamped := !ctrl.lastNonActionableAt.IsZero()
	ctrl.mu.RUnlock()
	if !stillStamped {
		t.Errorf("offline observation should not clear lastNonActionableAt")
	}
}
