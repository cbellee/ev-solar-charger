package config

import (
	"fmt"
	"log/slog"
	"net/url"
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
	TeslaRedirectURI          string
	TeslaScope                string
	TeslaVIN                  string
	TeslaPrivateKeyPath       string
	TeslaPublicKeyPEMPath     string
	OAuthStateHMACKey         string
	TeslaTokenPath            string
	TeslaRegion               string
	TeslaTestMode             bool
	PollInterval              time.Duration
	MinChargeAmps             int
	MaxChargeAmps             int
	LineVoltage               int
	DeadbandPolls             int
	WakeThresholdPolls        int
	WakeRetryInterval         time.Duration
	WakeAllowedStartHour      int
	WakeAllowedEndHour        int
	WakeTimezone              *time.Location
	PluggedInStaleAfter       time.Duration
	TeslaChargingPollInterval time.Duration
	TeslaIdlePollInterval     time.Duration
	AmpsChangeThreshold       int
	HTTPHost                  string
	HTTPPort                  int
	TLSEnabled                bool
	TLSDomain                 string
	TLSCertDir                string
	TLSPort                   int
	HTTPChallengePort         int
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
	cfg.TeslaPublicKeyPEMPath = envOrDefault("TESLA_PUBLIC_KEY_PEM_PATH", "/secrets/com.tesla.3p.public-key.pem")
	cfg.OAuthStateHMACKey = envOrDefault("OAUTH_STATE_HMAC_KEY", "")
	cfg.TeslaTokenPath = envOrDefault("TESLA_TOKEN_PATH", "/data/tesla-refresh-token")
	cfg.TeslaRegion = envOrDefault("TESLA_REGION", "na")
	cfg.TeslaRedirectURI = envOrDefault("TESLA_REDIRECT_URI", "")
	cfg.TeslaScope = envOrDefault("TESLA_SCOPE", "openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds")

	// Tesla OAuth credentials are required regardless of test mode so the
	// /auth/tesla bootstrap flow can run before TESLA_TEST_MODE is disabled.
	cfg.TeslaClientID, err = requireEnv("TESLA_CLIENT_ID")
	if err != nil {
		return Config{}, err
	}

	cfg.TeslaClientSecret, err = requireEnv("TESLA_CLIENT_SECRET")
	if err != nil {
		return Config{}, err
	}

	cfg.TeslaRefreshToken = envOrDefault("TESLA_REFRESH_TOKEN", "")

	if strings.TrimSpace(cfg.TeslaRedirectURI) == "" {
		return Config{}, fmt.Errorf("config: TESLA_REDIRECT_URI is required")
	}

	redirectURL, err := url.Parse(cfg.TeslaRedirectURI)
	if err != nil {
		return Config{}, fmt.Errorf("config: parse TESLA_REDIRECT_URI: %w", err)
	}
	if redirectURL.Scheme == "" || redirectURL.Host == "" {
		return Config{}, fmt.Errorf("config: TESLA_REDIRECT_URI must be an absolute URL")
	}
	if redirectURL.Scheme != "https" && !isLocalHTTP(redirectURL) {
		return Config{}, fmt.Errorf("config: TESLA_REDIRECT_URI must use https unless it targets localhost")
	}

	if strings.TrimSpace(cfg.TeslaPublicKeyPEMPath) == "" {
		return Config{}, fmt.Errorf("config: TESLA_PUBLIC_KEY_PEM_PATH is required")
	}

	if strings.TrimSpace(cfg.OAuthStateHMACKey) == "" {
		return Config{}, fmt.Errorf("config: OAUTH_STATE_HMAC_KEY is required")
	}

	if !cfg.TeslaTestMode {
		cfg.TeslaVIN, err = requireEnv("TESLA_VIN")
		if err != nil {
			return Config{}, err
		}
	} else {
		cfg.TeslaVIN = envOrDefault("TESLA_VIN", "")
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

	wakeRetrySec, err := envInt("WAKE_RETRY_INTERVAL_SECONDS", 300)
	if err != nil {
		return Config{}, err
	}
	if wakeRetrySec < 1 {
		return Config{}, fmt.Errorf("config: WAKE_RETRY_INTERVAL_SECONDS must be >= 1, got %d", wakeRetrySec)
	}
	cfg.WakeRetryInterval = time.Duration(wakeRetrySec) * time.Second

	plugStaleSec, err := envInt("PLUGGED_IN_STALE_AFTER_SECONDS", 86400)
	if err != nil {
		return Config{}, err
	}
	if plugStaleSec < 1 {
		return Config{}, fmt.Errorf("config: PLUGGED_IN_STALE_AFTER_SECONDS must be >= 1, got %d", plugStaleSec)
	}
	cfg.PluggedInStaleAfter = time.Duration(plugStaleSec) * time.Second

	cfg.WakeAllowedStartHour, err = envInt("WAKE_ALLOWED_START_HOUR", 8)
	if err != nil {
		return Config{}, err
	}
	if cfg.WakeAllowedStartHour < 0 || cfg.WakeAllowedStartHour > 23 {
		return Config{}, fmt.Errorf("config: WAKE_ALLOWED_START_HOUR must be in [0,23], got %d", cfg.WakeAllowedStartHour)
	}

	cfg.WakeAllowedEndHour, err = envInt("WAKE_ALLOWED_END_HOUR", 19)
	if err != nil {
		return Config{}, err
	}
	if cfg.WakeAllowedEndHour < 1 || cfg.WakeAllowedEndHour > 24 {
		return Config{}, fmt.Errorf("config: WAKE_ALLOWED_END_HOUR must be in [1,24], got %d", cfg.WakeAllowedEndHour)
	}
	if cfg.WakeAllowedEndHour <= cfg.WakeAllowedStartHour {
		return Config{}, fmt.Errorf("config: WAKE_ALLOWED_END_HOUR (%d) must be greater than WAKE_ALLOWED_START_HOUR (%d)", cfg.WakeAllowedEndHour, cfg.WakeAllowedStartHour)
	}

	tzName := envOrDefault("WAKE_TIMEZONE", "Australia/Sydney")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return Config{}, fmt.Errorf("config: WAKE_TIMEZONE %q is invalid: %w", tzName, err)
	}
	cfg.WakeTimezone = loc

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

	cfg.TLSEnabled, err = envBool("TLS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg.TLSDomain = envOrDefault("TLS_DOMAIN", "")
	cfg.TLSCertDir = envOrDefault("TLS_CERT_DIR", "/data/autocert")
	cfg.TLSPort, err = envInt("TLS_PORT", 8443)
	if err != nil {
		return Config{}, err
	}
	if err := validatePort("TLS_PORT", cfg.TLSPort); err != nil {
		return Config{}, err
	}
	cfg.HTTPChallengePort, err = envInt("HTTP_CHALLENGE_PORT", 8081)
	if err != nil {
		return Config{}, err
	}
	if err := validatePort("HTTP_CHALLENGE_PORT", cfg.HTTPChallengePort); err != nil {
		return Config{}, err
	}
	if cfg.TLSEnabled {
		if strings.TrimSpace(cfg.TLSDomain) == "" {
			return Config{}, fmt.Errorf("config: TLS_DOMAIN is required when TLS_ENABLED=true")
		}
		if strings.TrimSpace(cfg.TLSCertDir) == "" {
			return Config{}, fmt.Errorf("config: TLS_CERT_DIR is required when TLS_ENABLED=true")
		}
	}

	cfg.HTTPAuthUser, err = requireEnv("HTTP_AUTH_USER")
	if err != nil {
		return Config{}, err
	}
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

func isLocalHTTP(parsedURL *url.URL) bool {
	host := strings.ToLower(parsedURL.Hostname())
	return parsedURL.Scheme == "http" && (host == "localhost" || host == "127.0.0.1")
}
