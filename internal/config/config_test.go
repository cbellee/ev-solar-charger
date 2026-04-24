package config

import (
	"log/slog"
	"testing"
	"time"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SUNGROW_HOST", "192.168.1.100")
	t.Setenv("TESLA_CLIENT_ID", "test-client-id")
	t.Setenv("TESLA_CLIENT_SECRET", "test-client-secret")
	t.Setenv("TESLA_REFRESH_TOKEN", "test-refresh-token")
	t.Setenv("TESLA_VIN", "5YJ3E1EA0RF000000")
	t.Setenv("HTTP_AUTH_PASSWORD", "test-password")
}

func Test_Load_allRequiredVarsSet(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SungrowHost != "192.168.1.100" {
		t.Errorf("SungrowHost = %q, want %q", cfg.SungrowHost, "192.168.1.100")
	}
	if cfg.TeslaVIN != "5YJ3E1EA0RF000000" {
		t.Errorf("TeslaVIN = %q, want %q", cfg.TeslaVIN, "5YJ3E1EA0RF000000")
	}
}

func Test_Load_missingSungrowHost(t *testing.T) {
	t.Setenv("TESLA_CLIENT_ID", "id")
	t.Setenv("TESLA_CLIENT_SECRET", "secret")
	t.Setenv("TESLA_REFRESH_TOKEN", "token")
	t.Setenv("TESLA_VIN", "vin")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing SUNGROW_HOST")
	}
}

func Test_Load_missingTeslaVIN(t *testing.T) {
	t.Setenv("SUNGROW_HOST", "192.168.1.100")
	t.Setenv("TESLA_CLIENT_ID", "id")
	t.Setenv("TESLA_CLIENT_SECRET", "secret")
	t.Setenv("TESLA_REFRESH_TOKEN", "token")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing TESLA_VIN")
	}
}

func Test_Load_invalidPollInterval(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("POLL_INTERVAL_SECONDS", "abc")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid POLL_INTERVAL_SECONDS")
	}
}

func Test_Load_minChargeAmpsZero(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MIN_CHARGE_AMPS", "0")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for MIN_CHARGE_AMPS=0")
	}
}

func Test_Load_maxLessThanMin(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MIN_CHARGE_AMPS", "5")
	t.Setenv("MAX_CHARGE_AMPS", "3")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for MAX_CHARGE_AMPS < MIN_CHARGE_AMPS")
	}
}

func Test_Load_defaultsApplied(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SungrowPort != 8082 {
		t.Errorf("SungrowPort = %d, want 8082", cfg.SungrowPort)
	}
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s", cfg.PollInterval)
	}
	if cfg.MinChargeAmps != 5 {
		t.Errorf("MinChargeAmps = %d, want 5", cfg.MinChargeAmps)
	}
	if cfg.MaxChargeAmps != 32 {
		t.Errorf("MaxChargeAmps = %d, want 32", cfg.MaxChargeAmps)
	}
	if cfg.LineVoltage != 240 {
		t.Errorf("LineVoltage = %d, want 240", cfg.LineVoltage)
	}
	if cfg.HTTPPort != 8080 {
		t.Errorf("HTTPPort = %d, want 8080", cfg.HTTPPort)
	}
	if cfg.HTTPHost != "127.0.0.1" {
		t.Errorf("HTTPHost = %q, want %q", cfg.HTTPHost, "127.0.0.1")
	}
	if cfg.HTTPAuthUser != "admin" {
		t.Errorf("HTTPAuthUser = %q, want %q", cfg.HTTPAuthUser, "admin")
	}
	if cfg.DBRetentionDays != 365 {
		t.Errorf("DBRetentionDays = %d, want 365", cfg.DBRetentionDays)
	}
	if cfg.TeslaRegion != "na" {
		t.Errorf("TeslaRegion = %q, want %q", cfg.TeslaRegion, "na")
	}
	if cfg.TeslaTestMode {
		t.Fatal("TeslaTestMode = true, want false")
	}
	if cfg.TeslaChargingPollInterval != 60*time.Second {
		t.Errorf("TeslaChargingPollInterval = %v, want 60s", cfg.TeslaChargingPollInterval)
	}
	if cfg.TeslaIdlePollInterval != 300*time.Second {
		t.Errorf("TeslaIdlePollInterval = %v, want 300s", cfg.TeslaIdlePollInterval)
	}
	if cfg.AmpsChangeThreshold != 2 {
		t.Errorf("AmpsChangeThreshold = %d, want 2", cfg.AmpsChangeThreshold)
	}
}

func Test_Load_logLevelDebug(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("LOG_LEVEL", "debug")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
}

func Test_Load_logLevelInvalid(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("LOG_LEVEL", "invalid")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid LOG_LEVEL")
	}
}

func Test_Load_dbPathDefault(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DBPath != "/data/solar-ev-charger.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/data/solar-ev-charger.db")
	}
}

func Test_Load_dbRetentionDaysCustom(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_RETENTION_DAYS", "90")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DBRetentionDays != 90 {
		t.Errorf("DBRetentionDays = %d, want 90", cfg.DBRetentionDays)
	}
}

func Test_Load_missingHTTPAuthPassword(t *testing.T) {
	t.Setenv("SUNGROW_HOST", "192.168.1.100")
	t.Setenv("TESLA_CLIENT_ID", "test-client-id")
	t.Setenv("TESLA_CLIENT_SECRET", "test-client-secret")
	t.Setenv("TESLA_REFRESH_TOKEN", "test-refresh-token")
	t.Setenv("TESLA_VIN", "5YJ3E1EA0RF000000")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing HTTP_AUTH_PASSWORD")
	}
}

func Test_Load_invalidHTTPPort(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("HTTP_PORT", "70000")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid HTTP_PORT")
	}
}

func Test_Load_testModeSkipsTeslaCredentials(t *testing.T) {
	t.Setenv("SUNGROW_HOST", "192.168.1.100")
	t.Setenv("HTTP_AUTH_PASSWORD", "test-password")
	t.Setenv("TESLA_TEST_MODE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.TeslaTestMode {
		t.Fatal("TeslaTestMode = false, want true")
	}
	if cfg.TeslaVIN != "" {
		t.Fatalf("TeslaVIN = %q, want empty in test mode", cfg.TeslaVIN)
	}
}

func Test_Load_invalidTeslaTestMode(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TESLA_TEST_MODE", "maybe")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid TESLA_TEST_MODE")
	}
}

func Test_Load_teslaChargingPollSecondsCustom(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TESLA_CHARGING_POLL_SECONDS", "30")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TeslaChargingPollInterval != 30*time.Second {
		t.Errorf("TeslaChargingPollInterval = %v, want 30s", cfg.TeslaChargingPollInterval)
	}
}

func Test_Load_teslaChargingPollSecondsZero(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TESLA_CHARGING_POLL_SECONDS", "0")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for TESLA_CHARGING_POLL_SECONDS=0")
	}
}

func Test_Load_teslaIdlePollSecondsCustom(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TESLA_IDLE_POLL_SECONDS", "120")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TeslaIdlePollInterval != 120*time.Second {
		t.Errorf("TeslaIdlePollInterval = %v, want 120s", cfg.TeslaIdlePollInterval)
	}
}

func Test_Load_ampsChangeThresholdCustom(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AMPS_CHANGE_THRESHOLD", "5")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AmpsChangeThreshold != 5 {
		t.Errorf("AmpsChangeThreshold = %d, want 5", cfg.AmpsChangeThreshold)
	}
}

func Test_Load_ampsChangeThresholdNegative(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AMPS_CHANGE_THRESHOLD", "-1")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for AMPS_CHANGE_THRESHOLD=-1")
	}
}
