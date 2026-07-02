package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/controller"
	"github.com/cbellee/ev-solar-charger/internal/inverter"
	"github.com/cbellee/ev-solar-charger/internal/storage"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
)

type recordingStore struct {
	mu            sync.Mutex
	readings      []storage.Reading
	sessions      []storage.ChargeSession
	events        []storage.Event
	freeWindow    storage.FreeWindowSettings
	freeWindowSet bool
	saveHook      func(storage.FreeWindowSettings) error
	readingFilter storage.ReadingFilter
	readingsErr   error
	sessionsErr   error
	eventsErr     error
	searchErr     error
	freeWindowErr error
	searchQuery   string
	searchLimit   int
}

func (s *recordingStore) Migrate(ctx context.Context) error                          { return nil }
func (s *recordingStore) InsertReading(ctx context.Context, r storage.Reading) error { return nil }
func (s *recordingStore) QueryReadings(ctx context.Context, f storage.ReadingFilter) ([]storage.Reading, error) {
	s.readingFilter = f
	return s.readings, s.readingsErr
}
func (s *recordingStore) StartSession(ctx context.Context, cs storage.ChargeSession) (int64, error) {
	return 1, nil
}
func (s *recordingStore) EndSession(ctx context.Context, id int64, endTime time.Time, endBattery, energyKWh float64, peakAmps int, avgAmps float64) error {
	return nil
}
func (s *recordingStore) QuerySessions(ctx context.Context, from, to time.Time, limit, offset int) ([]storage.ChargeSession, error) {
	return s.sessions, s.sessionsErr
}
func (s *recordingStore) InsertEvent(ctx context.Context, e storage.Event) error { return nil }
func (s *recordingStore) QueryEvents(ctx context.Context, from, to time.Time, eventType string, limit, offset int) ([]storage.Event, error) {
	return s.events, s.eventsErr
}
func (s *recordingStore) Search(ctx context.Context, query string, from, to time.Time, limit int) ([]storage.Event, error) {
	s.searchQuery = query
	s.searchLimit = limit
	return s.events, s.searchErr
}
func (s *recordingStore) InsertAPIUsage(ctx context.Context, snap storage.APIUsageSnapshot) error {
	return nil
}
func (s *recordingStore) QueryAPIUsage(ctx context.Context, from, to time.Time, limit int) ([]storage.APIUsageSnapshot, error) {
	return []storage.APIUsageSnapshot{}, nil
}
func (s *recordingStore) GetFreeWindowSettings(ctx context.Context) (storage.FreeWindowSettings, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.freeWindowErr != nil {
		return storage.FreeWindowSettings{}, false, s.freeWindowErr
	}
	return s.freeWindow, s.freeWindowSet, nil
}
func (s *recordingStore) SaveFreeWindowSettings(ctx context.Context, fw storage.FreeWindowSettings) error {
	s.mu.Lock()
	saveHook := s.saveHook
	err := s.freeWindowErr
	s.mu.Unlock()

	if saveHook != nil {
		if err := saveHook(fw); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.freeWindow = fw
	s.freeWindowSet = true
	s.mu.Unlock()
	return nil
}
func (s *recordingStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}
func (s *recordingStore) Close() error { return nil }

func defaultTestCfg() config.Config {
	return config.Config{
		PollInterval:       10 * time.Second,
		MinChargeAmps:      5,
		MaxChargeAmps:      32,
		LineVoltage:        240,
		DeadbandPolls:      3,
		WakeThresholdPolls: 6,
	}
}

type nullInverter struct{}

func (n *nullInverter) Connect(ctx context.Context) error { return nil }
func (n *nullInverter) GetPowerData(ctx context.Context) (inverter.PowerData, error) {
	return inverter.PowerData{}, nil
}
func (n *nullInverter) Close() error { return nil }

type nullVehicle struct{}

func (n *nullVehicle) GetChargeState(ctx context.Context) (tesla.ChargeState, error) {
	return tesla.ChargeState{}, nil
}
func (n *nullVehicle) SetChargingAmps(ctx context.Context, amps int) error   { return nil }
func (n *nullVehicle) SetChargeLimit(ctx context.Context, percent int) error { return nil }
func (n *nullVehicle) StartCharging(ctx context.Context) error               { return nil }
func (n *nullVehicle) StopCharging(ctx context.Context) error                { return nil }
func (n *nullVehicle) WakeUp(ctx context.Context) error                      { return nil }
func (n *nullVehicle) SetRefreshToken(ctx context.Context, refreshToken string) error {
	return nil
}
func (n *nullVehicle) GetAPIUsage() tesla.APIUsage { return tesla.APIUsage{} }

func newTestCtrl(t *testing.T) *controller.Controller {
	t.Helper()
	return controller.New(
		&nullInverter{},
		&nullVehicle{},
		nil,
		defaultTestCfg(),
		nil,
		nil,
	)
}

func Test_handleState_returnsSnapshot(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleState(ctrl)

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var snap controller.StateSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
}

func Test_spaHandler_returnsHTML(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	spaHandler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("content-type = %q, want html", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "<div id=\"root\">") {
		t.Fatalf("unexpected body: %q", body)
	}
}

func Test_handleControl_start(t *testing.T) {
	ctrl := newTestCtrl(t)
	ctrl.SetMode(controller.ModeManual)
	handler := handleControl(ctrl)

	body := strings.NewReader(`{"action":"start"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func Test_handleControl_stop(t *testing.T) {
	ctrl := newTestCtrl(t)
	ctrl.SetMode(controller.ModeManual)
	handler := handleControl(ctrl)

	body := strings.NewReader(`{"action":"stop"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func Test_handleControl_setAmps(t *testing.T) {
	ctrl := newTestCtrl(t)
	ctrl.SetMode(controller.ModeManual)
	handler := handleControl(ctrl)

	body := strings.NewReader(`{"action":"setAmps","amps":16}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func Test_handleControl_invalidAction(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleControl(ctrl)

	body := strings.NewReader(`{"action":"reboot"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func Test_handleControl_badJSON(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleControl(ctrl)

	body := strings.NewReader(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/control", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func Test_handleControl_missingAmps(t *testing.T) {
	ctrl := newTestCtrl(t)
	ctrl.SetMode(controller.ModeManual)
	handler := handleControl(ctrl)

	body := strings.NewReader(`{"action":"setAmps"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func Test_handleControl_requiresManualMode(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleControl(ctrl)

	body := strings.NewReader(`{"action":"start"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func Test_handleMode_auto(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleMode(ctrl)

	body := strings.NewReader(`{"mode":"auto"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/mode", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func Test_handleMode_manual(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleMode(ctrl)

	body := strings.NewReader(`{"mode":"manual"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/mode", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func Test_handleMode_invalid(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleMode(ctrl)

	body := strings.NewReader(`{"mode":"turbo"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/mode", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func Test_handleFreeWindowGet_returnsSettings(t *testing.T) {
	ctrl := newTestCtrl(t)
	if err := ctrl.SetFreeWindowSettings(controller.FreeWindowSettings{
		Enabled:   true,
		StartHour: 11,
		EndHour:   14,
		Timezone:  "Australia/Sydney",
	}); err != nil {
		t.Fatalf("SetFreeWindowSettings error: %v", err)
	}

	handler := handleFreeWindowGet(ctrl)
	req := httptest.NewRequest(http.MethodGet, "/api/free-window", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var got controller.FreeWindowSettings
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Enabled || got.StartHour != 11 || got.EndHour != 14 || got.Timezone != "Australia/Sydney" {
		t.Fatalf("got = %+v, want enabled=true start=11 end=14 timezone=Australia/Sydney", got)
	}
}

func Test_handleFreeWindowUpdate_success(t *testing.T) {
	ctrl := newTestCtrl(t)
	store := &recordingStore{}
	handler := handleFreeWindowUpdate(ctrl, store)

	body := strings.NewReader(`{"enabled":true,"startHour":10,"endHour":13,"timezone":"UTC"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/free-window", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !store.freeWindowSet {
		t.Fatal("expected free-window settings to be persisted")
	}
	if !store.freeWindow.Enabled || store.freeWindow.StartHour != 10 || store.freeWindow.EndHour != 13 || store.freeWindow.Timezone != "UTC" {
		t.Fatalf("persisted settings = %+v, want enabled=true start=10 end=13 timezone=UTC", store.freeWindow)
	}

	got := ctrl.GetFreeWindowSettings()
	if !got.Enabled || got.StartHour != 10 || got.EndHour != 13 || got.Timezone != "UTC" {
		t.Fatalf("controller settings = %+v, want enabled=true start=10 end=13 timezone=UTC", got)
	}
}

func Test_handleFreeWindowUpdate_invalidTimezone(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleFreeWindowUpdate(ctrl, &recordingStore{})

	body := strings.NewReader(`{"enabled":true,"startHour":11,"endHour":14,"timezone":"Bad/Timezone"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/free-window", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func Test_handleFreeWindowUpdate_storeError(t *testing.T) {
	ctrl := newTestCtrl(t)
	if err := ctrl.SetFreeWindowSettings(controller.FreeWindowSettings{
		Enabled:   true,
		StartHour: 11,
		EndHour:   14,
		Timezone:  "Australia/Sydney",
	}); err != nil {
		t.Fatalf("SetFreeWindowSettings error: %v", err)
	}
	store := &recordingStore{freeWindowErr: errors.New("save failed")}
	handler := handleFreeWindowUpdate(ctrl, store)

	body := strings.NewReader(`{"enabled":true,"startHour":11,"endHour":14,"timezone":"UTC"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/free-window", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	got := ctrl.GetFreeWindowSettings()
	if !got.Enabled || got.StartHour != 11 || got.EndHour != 14 || got.Timezone != "Australia/Sydney" {
		t.Fatalf("controller settings changed on store error: %+v", got)
	}
}

func Test_handleFreeWindowUpdate_concurrentInterleavingKeepsRuntimeAndStoreConsistent(t *testing.T) {
	ctrl := newTestCtrl(t)
	if err := ctrl.SetFreeWindowSettings(controller.FreeWindowSettings{
		Enabled:   true,
		StartHour: 11,
		EndHour:   14,
		Timezone:  "Australia/Sydney",
	}); err != nil {
		t.Fatalf("SetFreeWindowSettings error: %v", err)
	}

	firstEntered := make(chan struct{})
	unblockFirst := make(chan struct{})
	var saveCalls atomic.Int32

	store := &recordingStore{}
	store.saveHook = func(fw storage.FreeWindowSettings) error {
		if saveCalls.Add(1) == 1 {
			close(firstEntered)
			<-unblockFirst
			return errors.New("first save failed")
		}
		return nil
	}

	handler := handleFreeWindowUpdate(ctrl, store)

	var wg sync.WaitGroup
	w1 := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		body := strings.NewReader(`{"enabled":true,"startHour":10,"endHour":13,"timezone":"UTC"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/free-window", body)
		req.Header.Set("Content-Type", "application/json")
		handler(w1, req)
	}()

	<-firstEntered

	w2 := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		body := strings.NewReader(`{"enabled":true,"startHour":9,"endHour":12,"timezone":"Europe/London"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/free-window", body)
		req.Header.Set("Content-Type", "application/json")
		handler(w2, req)
	}()

	close(unblockFirst)
	wg.Wait()

	if w1.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d, want 500", w1.Code)
	}
	if w2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200", w2.Code)
	}

	got := ctrl.GetFreeWindowSettings()
	if !got.Enabled || got.StartHour != 9 || got.EndHour != 12 || got.Timezone != "Europe/London" {
		t.Fatalf("controller settings = %+v, want enabled=true start=9 end=12 timezone=Europe/London", got)
	}

	store.mu.Lock()
	persisted := store.freeWindow
	persistedSet := store.freeWindowSet
	store.mu.Unlock()
	if !persistedSet {
		t.Fatal("expected persisted free-window settings to be set")
	}
	if !persisted.Enabled || persisted.StartHour != 9 || persisted.EndHour != 12 || persisted.Timezone != "Europe/London" {
		t.Fatalf("persisted settings = %+v, want enabled=true start=9 end=12 timezone=Europe/London", persisted)
	}
}

func Test_handleHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
}

func Test_handleHistory_invalidInterval(t *testing.T) {
	handler := handleHistory(&nullStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/history?interval=weekly", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func Test_handleHistory_successAndLimitCap(t *testing.T) {
	store := &recordingStore{readings: []storage.Reading{{State: "charging"}}}
	handler := handleHistory(store)

	req := httptest.NewRequest(http.MethodGet, "/api/history?interval=hour&limit=5000&offset=3", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if store.readingFilter.Interval != "hour" {
		t.Fatalf("interval = %q, want hour", store.readingFilter.Interval)
	}
	if store.readingFilter.Limit != 1000 {
		t.Fatalf("limit = %d, want 1000", store.readingFilter.Limit)
	}
	if store.readingFilter.Offset != 3 {
		t.Fatalf("offset = %d, want 3", store.readingFilter.Offset)
	}
}

func Test_handleHistory_queryError(t *testing.T) {
	store := &recordingStore{readingsErr: errors.New("boom")}
	handler := handleHistory(store)

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func Test_handleSessions_success(t *testing.T) {
	store := &recordingStore{sessions: []storage.ChargeSession{{ID: 1}}}
	handler := handleSessions(store)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func Test_handleSessions_queryError(t *testing.T) {
	store := &recordingStore{sessionsErr: errors.New("boom")}
	handler := handleSessions(store)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func Test_handleEvents_success(t *testing.T) {
	store := &recordingStore{events: []storage.Event{{ID: 1, Type: "state_change"}}}
	handler := handleEvents(store)

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func Test_handleEvents_queryError(t *testing.T) {
	store := &recordingStore{eventsErr: errors.New("boom")}
	handler := handleEvents(store)

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func Test_handleSearch_missingQ(t *testing.T) {
	handler := handleSearch(&nullStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func Test_handleSearch_success(t *testing.T) {
	store := &recordingStore{events: []storage.Event{{ID: 1, Message: "surplus detected"}}}
	handler := handleSearch(store)

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=surplus&limit=25", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if store.searchQuery != "surplus" {
		t.Fatalf("query = %q, want surplus", store.searchQuery)
	}
	if store.searchLimit != 25 {
		t.Fatalf("limit = %d, want 25", store.searchLimit)
	}
}

func Test_handleSearch_error(t *testing.T) {
	store := &recordingStore{searchErr: errors.New("boom")}
	handler := handleSearch(store)

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=surplus", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func Test_parseTimeParam_invalidUsesDefault(t *testing.T) {
	defaultTime := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	req := httptest.NewRequest(http.MethodGet, "/?from=not-a-time", nil)

	got := parseTimeParam(req, "from", defaultTime)
	if !got.Equal(defaultTime) {
		t.Fatalf("got %v, want %v", got, defaultTime)
	}
}

func Test_parseTimeParam_valid(t *testing.T) {
	defaultTime := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	validTime := "2026-04-03T10:30:00Z"
	req := httptest.NewRequest(http.MethodGet, "/?from="+validTime, nil)

	got := parseTimeParam(req, "from", defaultTime)
	want, _ := time.Parse(time.RFC3339, validTime)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func Test_parseIntParam_invalidUsesDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?limit=abc", nil)

	got := parseIntParam(req, "limit", 42)
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func Test_parseIntParam_valid(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?limit=17", nil)

	got := parseIntParam(req, "limit", 42)
	if got != 17 {
		t.Fatalf("got %d, want 17", got)
	}
}

// nullStore implements storage.Store for handler tests.
type nullStore struct{}

func (s *nullStore) Migrate(ctx context.Context) error                          { return nil }
func (s *nullStore) InsertReading(ctx context.Context, r storage.Reading) error { return nil }
func (s *nullStore) QueryReadings(ctx context.Context, f storage.ReadingFilter) ([]storage.Reading, error) {
	return []storage.Reading{}, nil
}
func (s *nullStore) StartSession(ctx context.Context, cs storage.ChargeSession) (int64, error) {
	return 1, nil
}
func (s *nullStore) EndSession(ctx context.Context, id int64, endTime time.Time, endBattery, energyKWh float64, peakAmps int, avgAmps float64) error {
	return nil
}
func (s *nullStore) QuerySessions(ctx context.Context, from, to time.Time, limit, offset int) ([]storage.ChargeSession, error) {
	return []storage.ChargeSession{}, nil
}
func (s *nullStore) InsertEvent(ctx context.Context, e storage.Event) error { return nil }
func (s *nullStore) QueryEvents(ctx context.Context, from, to time.Time, eventType string, limit, offset int) ([]storage.Event, error) {
	return []storage.Event{}, nil
}
func (s *nullStore) Search(ctx context.Context, query string, from, to time.Time, limit int) ([]storage.Event, error) {
	return []storage.Event{}, nil
}
func (s *nullStore) InsertAPIUsage(ctx context.Context, snap storage.APIUsageSnapshot) error {
	return nil
}
func (s *nullStore) QueryAPIUsage(ctx context.Context, from, to time.Time, limit int) ([]storage.APIUsageSnapshot, error) {
	return []storage.APIUsageSnapshot{}, nil
}
func (s *nullStore) GetFreeWindowSettings(ctx context.Context) (storage.FreeWindowSettings, bool, error) {
	return storage.FreeWindowSettings{}, false, nil
}
func (s *nullStore) SaveFreeWindowSettings(ctx context.Context, fw storage.FreeWindowSettings) error {
	return nil
}
func (s *nullStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) { return 0, nil }
func (s *nullStore) Close() error                                                      { return nil }

func Test_handleAPIUsage_returnsUsageData(t *testing.T) {
	ctrl := newTestCtrl(t)
	handler := handleAPIUsage(ctrl)

	req := httptest.NewRequest(http.MethodGet, "/api/usage", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiUsageResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.DataCost != 0 {
		t.Errorf("DataCost = %f, want 0", resp.DataCost)
	}
	if resp.CommandCost != 0 {
		t.Errorf("CommandCost = %f, want 0", resp.CommandCost)
	}
	if resp.WakeCost != 0 {
		t.Errorf("WakeCost = %f, want 0", resp.WakeCost)
	}
	if resp.StreamCost != 0 {
		t.Errorf("StreamCost = %f, want 0", resp.StreamCost)
	}
	if resp.MonthlyDiscount != 10.0 {
		t.Errorf("MonthlyDiscount = %f, want 10.0", resp.MonthlyDiscount)
	}
	if resp.NetCost != 0 {
		t.Errorf("NetCost = %f, want 0 (under free tier)", resp.NetCost)
	}
	if resp.MonthStarted != "" {
		t.Errorf("MonthStarted = %q, want empty string for unavailable usage", resp.MonthStarted)
	}
	if resp.DiscountedDataEligible {
		t.Error("DiscountedDataEligible = true, want false")
	}
	if resp.EstimatedCostUpperBound {
		t.Error("EstimatedCostUpperBound = true, want false")
	}
}

func Test_handleAPIUsageHistory_returnsSnapshots(t *testing.T) {
	handler := handleAPIUsageHistory(&nullStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/usage/history", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var snapshots []storage.APIUsageSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snapshots); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("got %d snapshots, want 0", len(snapshots))
	}
}
