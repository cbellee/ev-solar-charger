package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	SungrowHost               string
	SungrowPort               int
	TeslaClientID             string
	TeslaClientSecret         string
	TeslaRefreshToken         string
	TeslaVIN                  string
	TeslaPrivateKeyPath       string
	TeslaRegion               string
	TeslaTestMode             bool
	PollInterval              time.Duration
	MinChargeAmps             int
	MaxChargeAmps             int
	LineVoltage               int
	DeadbandPolls             int
	WakeThresholdPolls        int
	TeslaChargingPollInterval time.Duration
	TeslaIdlePollInterval     time.Duration
	AmpsChangeThreshold       int
	HTTPHost                  string
	HTTPPort                  int
	HTTPAuthUser              string
	HTTPAuthPassword          string
	LogLevel                  slog.Level
	DBPath                    string
	DBRetentionDays           int
}

// Load reads configuration from environment variables, applies defaults,
// and validates required fields. Returns a descriptive error if configuration
// is invalid.
func Load() (Config, error) {
	var cfg Config
	var err error

	cfg.SungrowHost, err = requireEnv("SUNGROW_HOST")
	if err != nil {
		return Config{}, err
	}

	cfg.SungrowPort, err = envInt("SUNGROW_PORT", 8082)
	if err != nil {
		return Config{}, err
	}
	if err := validatePort("SUNGROW_PORT", cfg.SungrowPort); err != nil {
		return Config{}, err
	}

	cfg.TeslaTestMode, err = envBool("TESLA_TEST_MODE", false)
	if err != nil {
		return Config{}, err
	}
	cfg.TeslaPrivateKeyPath = envOrDefault("TESLA_PRIVATE_KEY_PATH", "/secrets/fleet-key.pem")
	cfg.TeslaRegion = envOrDefault("TESLA_REGION", "na")

	if !cfg.TeslaTestMode {
		cfg.TeslaClientID, err = requireEnv("TESLA_CLIENT_ID")
		if err != nil {
			return Config{}, err
		}

		cfg.TeslaClientSecret, err = requireEnv("TESLA_CLIENT_SECRET")
		if err != nil {
			return Config{}, err
		}

		cfg.TeslaRefreshToken, err = requireEnv("TESLA_REFRESH_TOKEN")
		if err != nil {
			return Config{}, err
		}

		cfg.TeslaVIN, err = requireEnv("TESLA_VIN")
		if err != nil {
			return Config{}, err
		}
	}

	pollSec, err := envInt("POLL_INTERVAL_SECONDS", 10)
	if err != nil {
		return Config{}, err
	}
	if pollSec < 1 {
		return Config{}, fmt.Errorf("config: POLL_INTERVAL_SECONDS must be >= 1, got %d", pollSec)
	}

	cfg.PollInterval = time.Duration(pollSec) * time.Second
	cfg.MinChargeAmps, err = envInt("MIN_CHARGE_AMPS", 5)
	if err != nil {
		return Config{}, err
	}

	if cfg.MinChargeAmps < 1 {
		return Config{}, fmt.Errorf("config: MIN_CHARGE_AMPS must be >= 1, got %d", cfg.MinChargeAmps)
	}

	cfg.MaxChargeAmps, err = envInt("MAX_CHARGE_AMPS", 32)
	if err != nil {
		return Config{}, err
	}

	if cfg.MaxChargeAmps < cfg.MinChargeAmps {
		return Config{}, fmt.Errorf("config: MAX_CHARGE_AMPS (%d) must be >= MIN_CHARGE_AMPS (%d)", cfg.MaxChargeAmps, cfg.MinChargeAmps)
	}

	cfg.LineVoltage, err = envInt("LINE_VOLTAGE", 240)
	if err != nil {
		return Config{}, err
	}
	if cfg.LineVoltage < 1 {
		return Config{}, fmt.Errorf("config: LINE_VOLTAGE must be >= 1, got %d", cfg.LineVoltage)
	}

	cfg.DeadbandPolls, err = envInt("DEADBAND_POLLS", 3)
	if err != nil {
		return Config{}, err
	}
	if cfg.DeadbandPolls < 1 {
		return Config{}, fmt.Errorf("config: DEADBAND_POLLS must be >= 1, got %d", cfg.DeadbandPolls)
	}

	cfg.WakeThresholdPolls, err = envInt("WAKE_THRESHOLD_POLLS", 6)
	if err != nil {
		return Config{}, err
	}
	if cfg.WakeThresholdPolls < 1 {
		return Config{}, fmt.Errorf("config: WAKE_THRESHOLD_POLLS must be >= 1, got %d", cfg.WakeThresholdPolls)
	}

	teslaChargingSec, err := envInt("TESLA_CHARGING_POLL_SECONDS", 60)
	if err != nil {
		return Config{}, err
	}
	if teslaChargingSec < 1 {
		return Config{}, fmt.Errorf("config: TESLA_CHARGING_POLL_SECONDS must be >= 1, got %d", teslaChargingSec)
	}
	cfg.TeslaChargingPollInterval = time.Duration(teslaChargingSec) * time.Second

	teslaIdleSec, err := envInt("TESLA_IDLE_POLL_SECONDS", 300)
	if err != nil {
		return Config{}, err
	}
	if teslaIdleSec < 1 {
		return Config{}, fmt.Errorf("config: TESLA_IDLE_POLL_SECONDS must be >= 1, got %d", teslaIdleSec)
	}
	cfg.TeslaIdlePollInterval = time.Duration(teslaIdleSec) * time.Second

	cfg.AmpsChangeThreshold, err = envInt("AMPS_CHANGE_THRESHOLD", 2)
	if err != nil {
		return Config{}, err
	}
	if cfg.AmpsChangeThreshold < 0 {
		return Config{}, fmt.Errorf("config: AMPS_CHANGE_THRESHOLD must be >= 0, got %d", cfg.AmpsChangeThreshold)
	}

	cfg.HTTPHost = envOrDefault("HTTP_HOST", "127.0.0.1")
	if strings.TrimSpace(cfg.HTTPHost) == "" {
		return Config{}, fmt.Errorf("config: HTTP_HOST must not be empty")
	}

	cfg.HTTPPort, err = envInt("HTTP_PORT", 8080)
	if err != nil {
		return Config{}, err
	}
	if err := validatePort("HTTP_PORT", cfg.HTTPPort); err != nil {
		return Config{}, err
	}

	cfg.HTTPAuthUser = envOrDefault("HTTP_AUTH_USER", "admin")
	cfg.HTTPAuthPassword, err = requireEnv("HTTP_AUTH_PASSWORD")
	if err != nil {
		return Config{}, err
	}

	cfg.LogLevel, err = parseLogLevel(envOrDefault("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	cfg.DBPath = envOrDefault("DB_PATH", "/data/solar-ev-charger.db")

	cfg.DBRetentionDays, err = envInt("DB_RETENTION_DAYS", 365)
	if err != nil {
		return Config{}, err
	}
	if cfg.DBRetentionDays < 1 {
		return Config{}, fmt.Errorf("config: DB_RETENTION_DAYS must be >= 1, got %d", cfg.DBRetentionDays)
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("config: %s is required", key)
	}
	return v, nil
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer: %w", key, err)
	}
	return n, nil
}

func envBool(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean: %w", key, err)
	}
	return b, nil
}

func validatePort(name string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("config: %s must be between 1 and 65535, got %d", name, port)
	}
	return nil
}

func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: invalid LOG_LEVEL %q, must be debug, info, warn, or error", s)
	}
}
