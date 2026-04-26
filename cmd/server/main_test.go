package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/inverter"
	"github.com/cbellee/ev-solar-charger/internal/observability"
	"github.com/cbellee/ev-solar-charger/internal/storage"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
	"github.com/cbellee/ev-solar-charger/internal/web"
)

type fakeStore struct{}

func (fakeStore) Migrate(ctx context.Context) error                          { return nil }
func (fakeStore) InsertReading(ctx context.Context, r storage.Reading) error { return nil }
func (fakeStore) QueryReadings(ctx context.Context, f storage.ReadingFilter) ([]storage.Reading, error) {
	return nil, nil
}
func (fakeStore) StartSession(ctx context.Context, s storage.ChargeSession) (int64, error) {
	return 1, nil
}
func (fakeStore) EndSession(ctx context.Context, id int64, endTime time.Time, endBattery, energyKWh float64, peakAmps int, avgAmps float64) error {
	return nil
}
func (fakeStore) QuerySessions(ctx context.Context, from, to time.Time, limit, offset int) ([]storage.ChargeSession, error) {
	return nil, nil
}
func (fakeStore) InsertEvent(ctx context.Context, e storage.Event) error { return nil }
func (fakeStore) QueryEvents(ctx context.Context, from, to time.Time, eventType string, limit, offset int) ([]storage.Event, error) {
	return nil, nil
}
func (fakeStore) Search(ctx context.Context, query string, from, to time.Time, limit int) ([]storage.Event, error) {
	return nil, nil
}
func (fakeStore) InsertAPIUsage(ctx context.Context, s storage.APIUsageSnapshot) error {
	return nil
}
func (fakeStore) QueryAPIUsage(ctx context.Context, from, to time.Time, limit int) ([]storage.APIUsageSnapshot, error) {
	return nil, nil
}
func (fakeStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) { return 0, nil }
func (fakeStore) Close() error                                                      { return nil }

type fakeInverter struct{}

func (fakeInverter) Connect(ctx context.Context) error { return nil }
func (fakeInverter) GetPowerData(ctx context.Context) (inverter.PowerData, error) {
	return inverter.PowerData{}, nil
}
func (fakeInverter) Close() error { return nil }

type fakeVehicle struct{}

func (fakeVehicle) GetChargeState(ctx context.Context) (tesla.ChargeState, error) {
	return tesla.ChargeState{}, tesla.ErrCarOffline
}
func (fakeVehicle) SetChargingAmps(ctx context.Context, amps int) error { return nil }
func (fakeVehicle) StartCharging(ctx context.Context) error             { return nil }
func (fakeVehicle) StopCharging(ctx context.Context) error              { return nil }
func (fakeVehicle) WakeUp(ctx context.Context) error                    { return nil }
func (fakeVehicle) SetRefreshToken(ctx context.Context, refreshToken string) error {
	return nil
}
func (fakeVehicle) GetAPIUsage() tesla.APIUsage { return tesla.APIUsage{} }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testConfig(port int) config.Config {
	return config.Config{
		SungrowHost:        "127.0.0.1",
		SungrowPort:        8082,
		TeslaTestMode:      true,
		PollInterval:       time.Hour,
		MinChargeAmps:      5,
		MaxChargeAmps:      32,
		LineVoltage:        240,
		DeadbandPolls:      3,
		WakeThresholdPolls: 6,
		HTTPHost:           "127.0.0.1",
		HTTPPort:           port,
		HTTPAuthUser:       "admin",
		HTTPAuthPassword:   "secret",
		LogLevel:           slog.LevelInfo,
		DBPath:             "/tmp/test.db",
		DBRetentionDays:    365,
	}
}

func testDeps(cfg config.Config) runtimeDeps {
	return runtimeDeps{
		loadConfig: func() (config.Config, error) { return cfg, nil },
		setupOTelSDK: func(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
			return func(context.Context) error { return nil }, nil
		},
		newLogger:  func(name string, level slog.Level) *slog.Logger { return testLogger() },
		newMetrics: func() (*observability.Metrics, error) { return nil, nil },
		newStore:   func(dbPath string, logger *slog.Logger) (storage.Store, error) { return fakeStore{}, nil },
		newInverter: func(host string, port int, logger *slog.Logger, metrics *observability.Metrics) inverter.InverterReader {
			return fakeInverter{}
		},
		newVehicle: func(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) (tesla.VehicleController, error) {
			return fakeVehicle{}, nil
		},
		newServer: web.NewServer,
		newHub:    web.NewHub,
	}
}

func Test_main_healthcheckBranch(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	port := listener.Addr().(*net.TCPAddr).Port
	t.Setenv("HTTP_PORT", strconv.Itoa(port))

	oldArgs := os.Args
	os.Args = []string{"solar-ev-charger", "healthcheck"}
	defer func() { os.Args = oldArgs }()

	main()
}

func Test_checkHealth_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := checkHealth(ctx, srv.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func Test_checkHealth_failureStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := checkHealth(ctx, srv.URL); err == nil {
		t.Fatal("expected error for non-200 healthcheck")
	}
}

func Test_healthcheckPort_default(t *testing.T) {
	t.Setenv("HTTP_PORT", "")

	port, err := healthcheckPort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 8080 {
		t.Fatalf("port = %d, want 8080", port)
	}
}

func Test_healthcheckPort_invalid(t *testing.T) {
	t.Setenv("HTTP_PORT", "invalid")

	if _, err := healthcheckPort(); err == nil {
		t.Fatal("expected error for invalid HTTP_PORT")
	}
}

func Test_healthcheckPort_custom(t *testing.T) {
	t.Setenv("HTTP_PORT", "9090")

	port, err := healthcheckPort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 9090 {
		t.Fatalf("port = %d, want 9090", port)
	}
}

func Test_healthcheckPort_outOfRange(t *testing.T) {
	t.Setenv("HTTP_PORT", "70000")

	if _, err := healthcheckPort(); err == nil {
		t.Fatal("expected error for out-of-range HTTP_PORT")
	}
}

func Test_checkHealth_badURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := checkHealth(ctx, "://bad-url"); err == nil {
		t.Fatal("expected error for malformed URL")
	}
}

func Test_runHealthcheck_success(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	port := listener.Addr().(*net.TCPAddr).Port
	t.Setenv("HTTP_PORT", strconv.Itoa(port))

	if err := runHealthcheck(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func Test_runHealthcheck_invalidPort(t *testing.T) {
	t.Setenv("HTTP_PORT", "70000")

	if err := runHealthcheck(); err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func Test_runWithContext_serverError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	err = runWithContext(context.Background(), testDeps(testConfig(port)))
	if err == nil {
		t.Fatal("expected listen error")
	}
	if !strings.Contains(err.Error(), "http server failed") {
		t.Fatalf("error = %v, want http server failed", err)
	}
}

func Test_runWithContext_cancelledParent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runWithContext(ctx, testDeps(testConfig(0))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func Test_runWithContext_loadConfigError(t *testing.T) {
	deps := defaultRuntimeDeps()
	deps.loadConfig = func() (config.Config, error) {
		return config.Config{}, errors.New("boom")
	}

	err := runWithContext(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to load config") {
		t.Fatalf("error = %v, want load config context", err)
	}
}

func Test_runWithContext_testModeSkipsTeslaClientCreation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := testConfig(0)
	cfg.TeslaTestMode = true
	deps := testDeps(cfg)
	called := false
	deps.newVehicle = func(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) (tesla.VehicleController, error) {
		called = true
		return nil, errors.New("should not be called")
	}

	if err := runWithContext(ctx, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("expected Tesla client creation to be skipped in test mode")
	}
}
