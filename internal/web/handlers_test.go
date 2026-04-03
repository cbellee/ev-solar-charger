package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cbellee/solar-ev-charger/internal/config"
	"github.com/cbellee/solar-ev-charger/internal/controller"
	"github.com/cbellee/solar-ev-charger/internal/inverter"
	"github.com/cbellee/solar-ev-charger/internal/storage"
	"github.com/cbellee/solar-ev-charger/internal/tesla"
)

type recordingStore struct {
	readings      []storage.Reading
	sessions      []storage.ChargeSession
	events        []storage.Event
	readingFilter storage.ReadingFilter
	readingsErr   error
	sessionsErr   error
	eventsErr     error
	searchErr     error
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
func (n *nullVehicle) SetChargingAmps(ctx context.Context, amps int) error { return nil }
func (n *nullVehicle) StartCharging(ctx context.Context) error             { return nil }
func (n *nullVehicle) StopCharging(ctx context.Context) error              { return nil }
func (n *nullVehicle) WakeUp(ctx context.Context) error                    { return nil }

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

func Test_handleIndex_returnsHTML(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handleIndex(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("content-type = %q, want html", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "Solar EV Charger Controller") {
		t.Fatalf("unexpected body: %q", body)
	}
}

func Test_handleControl_start(t *testing.T) {
	ctrl := newTestCtrl(t)
	ctrl.SetMode(controller.ModeManual)
	handler := handleControl(ctrl)

	body := strings.NewReader(`{"action":"start"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control", body)
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
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
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
func (s *nullStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) { return 0, nil }
func (s *nullStore) Close() error                                                      { return nil }
