package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cbellee/solar-ev-charger/internal/config"
	"github.com/cbellee/solar-ev-charger/internal/inverter"
	"github.com/cbellee/solar-ev-charger/internal/storage"
	"github.com/cbellee/solar-ev-charger/internal/tesla"
)

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
	calls       []string
	setAmpsErr  error
	startErr    error
	stopErr     error
	wakeErr     error
}

func (m *mockVehicle) GetChargeState(ctx context.Context) (tesla.ChargeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chargeState, m.stateErr
}

func (m *mockVehicle) SetChargingAmps(ctx context.Context, amps int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, fmt.Sprintf("SetChargingAmps:%d", amps))
	return m.setAmpsErr
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

func (m *mockVehicle) getCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.calls))
	copy(result, m.calls)
	return result
}

// mockStore implements storage.Store (minimal, for controller tests).
type mockStore struct {
	mu            sync.Mutex
	readings      []storage.Reading
	events        []storage.Event
	sessions      []storage.ChargeSession
	endedSessions []storage.ChargeSession
	nextID        int64
}

func (m *mockStore) Migrate(ctx context.Context) error { return nil }

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

func (m *mockStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) { return 0, nil }
func (m *mockStore) Close() error                                                      { return nil }

func defaultCfg() config.Config {
	return config.Config{
		PollInterval:       10 * time.Second,
		MinChargeAmps:      5,
		MaxChargeAmps:      32,
		LineVoltage:        240,
		DeadbandPolls:      3,
		WakeThresholdPolls: 6,
	}
}

func newTestController(inv *mockInverter, veh *mockVehicle, store storage.Store) *Controller {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return New(inv, veh, store, defaultCfg(), logger, nil)
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
	veh := &mockVehicle{chargeState: tesla.ChargeState{IsOnline: true, PluggedIn: true, State: "Stopped"}}
	ctrl := newTestController(inv, veh, &mockStore{})
	ctrl.Tick(context.Background())
	snap := ctrl.GetStateSnapshot()
	if snap.State != StateCharging {
		t.Errorf("State = %q, want %q", snap.State, StateCharging)
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

func Test_calculateAvailableAmps(t *testing.T) {
	tests := []struct {
		name     string
		surplusW float64
		charging bool
		carAmps  int
		wantAmps int
	}{
		{"2400W no car", 2400, false, 0, 10},
		{"2400W car at 10A", 2400, true, 10, 20},
		{"1199W below min", 1199, false, 0, 4},
		{"1200W exactly min", 1200, false, 0, 5},
		{"7680W at max", 7680, false, 0, 32},
		{"9600W above max", 9600, false, 0, 32},
		{"0W no car", 0, false, 0, 0},
		{"0W car at 10A", 0, true, 10, 10},
		{"-500W car at 10A", 0, true, 10, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := newTestController(nil, nil, nil)
			cs := tesla.ChargeState{AmpsActual: tt.carAmps}
			if tt.charging {
				cs.State = "Charging"
			}
			got := ctrl.calculateAvailableAmps(tt.surplusW, cs)
			if got != tt.wantAmps {
				t.Errorf("calculateAvailableAmps(%f, charging=%v, carAmps=%d) = %d, want %d",
					tt.surplusW, tt.charging, tt.carAmps, got, tt.wantAmps)
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
