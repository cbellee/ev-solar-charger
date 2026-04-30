package tesla

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var regionBaseURLs = map[string]string{
	"na": "https://fleet-api.prd.na.vn.cloud.tesla.com",
	"eu": "https://fleet-api.prd.eu.vn.cloud.tesla.com",
	"cn": "https://fleet-api.prd.cn.vn.cloud.tesla.com",
}

const authURL = "https://fleet-auth.prd.vn.cloud.tesla.com"

// TeslaClient implements VehicleController using the Tesla Fleet API.
type TeslaClient struct {
	httpClient   *http.Client
	cmdClient    *http.Client
	baseURL      string
	cmdBaseURL   string
	authURL      string
	vin          string
	clientID     string
	clientSecret string
	privateKey   *ecdsa.PrivateKey
	logger       *slog.Logger
	metrics      *observability.Metrics
	minAmps      int
	maxAmps      int
	usage        *APIUsageTracker

	mu           sync.Mutex
	accessToken  string
	refreshToken string
	tokenExpiry  time.Time
}

// New creates a new TeslaClient.
func New(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) (*TeslaClient, error) {
	baseURL, ok := regionBaseURLs[cfg.TeslaRegion]
	if !ok {
		return nil, fmt.Errorf("tesla: unknown region %q", cfg.TeslaRegion)
	}

	var privateKey *ecdsa.PrivateKey
	keyData, err := os.ReadFile(cfg.TeslaPrivateKeyPath)
	if err != nil {
		logger.Warn("tesla: could not read private key, command signing disabled", "error", err)
	} else {
		block, _ := pem.Decode(keyData)
		if block != nil {
			key, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("tesla: parse private key: %w", err)
			}
			privateKey = key
		}
	}

	c := &TeslaClient{
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		baseURL:      baseURL,
		authURL:      authURL,
		vin:          cfg.TeslaVIN,
		clientID:     cfg.TeslaClientID,
		clientSecret: cfg.TeslaClientSecret,
		privateKey:   privateKey,
		logger:       logger,
		metrics:      metrics,
		minAmps:      cfg.MinChargeAmps,
		maxAmps:      cfg.MaxChargeAmps,
		refreshToken: cfg.TeslaRefreshToken,
		usage:        NewAPIUsageTracker(),
	}

	// Tesla Vehicle Command Protocol: routes commands (charge_start,
	// charge_stop, set_charging_amps, wake_up) through tesla-http-proxy when
	// TESLA_COMMAND_BASE is set. The proxy signs payloads with the fleet
	// private key and forwards to Fleet API. Data endpoints stay on the
	// regional Fleet API base URL.
	c.cmdBaseURL = baseURL
	c.cmdClient = c.httpClient
	if cfg.TeslaCommandBase != "" {
		c.cmdBaseURL = strings.TrimRight(cfg.TeslaCommandBase, "/")
		cmdHTTP, err := buildCommandHTTPClient(cfg.TeslaProxyCAFile)
		if err != nil {
			return nil, fmt.Errorf("tesla: build command client: %w", err)
		}
		c.cmdClient = cmdHTTP
		logger.Info("tesla: vehicle command protocol enabled", "base", c.cmdBaseURL)
	}

	if err := c.refreshAccessToken(context.Background()); err != nil {
		return nil, fmt.Errorf("tesla: initial token refresh: %w", err)
	}

	return c, nil
}

// buildCommandHTTPClient creates an HTTP client that trusts the proxy's
// (typically self-signed) TLS cert. The proxy refuses to start without TLS,
// so plain http:// is not supported.
func buildCommandHTTPClient(caFile string) (*http.Client, error) {
	tr := &http.Transport{}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read ca file %q: %w", caFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates parsed from %q", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}, nil
}

func (c *TeslaClient) refreshAccessToken(ctx context.Context) error {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"refresh_token": {c.refreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.authURL+"/oauth2/v3/token",
		strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("tesla: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("tesla: token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tesla: token refresh failed: status %d: %s", resp.StatusCode, body)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("tesla: decode token response: %w", err)
	}

	c.mu.Lock()
	c.accessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		c.refreshToken = tokenResp.RefreshToken
	}
	c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	c.mu.Unlock()

	if c.logger != nil {
		c.logger.Info("tesla: token refreshed", "expires_in", tokenResp.ExpiresIn)
	}
	return nil
}

// routeFor selects the HTTP client and base URL for a given API path.
// Command paths (signed by tesla-http-proxy) and wake_up go through the
// command client when configured; everything else uses the regional Fleet
// API directly.
func (c *TeslaClient) routeFor(path string) (*http.Client, string) {
	if c.cmdBaseURL != "" && c.cmdBaseURL != c.baseURL && isCommandPath(path) {
		return c.cmdClient, c.cmdBaseURL
	}
	return c.httpClient, c.baseURL
}

func isCommandPath(path string) bool {
	return strings.Contains(path, "/command/") || strings.HasSuffix(path, "/wake_up")
}

func (c *TeslaClient) doRequest(ctx context.Context, method, path string, body any) (*http.Response, error) {
	c.mu.Lock()
	if time.Now().After(c.tokenExpiry.Add(-60 * time.Second)) {
		c.mu.Unlock()
		if err := c.refreshAccessToken(ctx); err != nil {
			return nil, err
		}
	} else {
		c.mu.Unlock()
	}

	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("tesla: marshal request body: %w", err)
		}
	}

	httpClient, baseURL := c.routeFor(path)

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("tesla: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	c.mu.Lock()
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	c.mu.Unlock()

	if len(payload) > 0 {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tesla: %s %s: %w", method, path, err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := c.refreshAccessToken(ctx); err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("tesla: rebuild request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		c.mu.Lock()
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
		c.mu.Unlock()
		if len(payload) > 0 {
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(payload)), nil
			}
		}
		resp, err = httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("tesla: %s %s retry: %w", method, path, err)
		}
	}

	if resp.StatusCode == http.StatusRequestTimeout {
		resp.Body.Close()
		return nil, ErrCarOffline
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("tesla: %s %s: status %d: %s", method, path, resp.StatusCode, body)
	}

	return resp, nil
}

// GetChargeState retrieves the vehicle's current charge state.
func (c *TeslaClient) GetChargeState(ctx context.Context) (ChargeState, error) {
	c.usage.RecordData()
	path := fmt.Sprintf("/api/1/vehicles/%s/vehicle_data?endpoints=charge_state", c.vin)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ChargeState{}, err
	}
	defer resp.Body.Close()

	var result struct {
		Response struct {
			ChargeState struct {
				ChargingState      string  `json:"charging_state"`
				ChargeAmps         int     `json:"charger_actual_current"`
				BatteryLevel       float64 `json:"battery_level"`
				ChargePortDoorOpen bool    `json:"charge_port_door_open"`
				ChargePortLatch    string  `json:"charge_port_latch"`
			} `json:"charge_state"`
			State string `json:"state"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ChargeState{}, fmt.Errorf("tesla: decode charge state: %w", err)
	}

	cs := ChargeState{
		State:      result.Response.ChargeState.ChargingState,
		AmpsActual: result.Response.ChargeState.ChargeAmps,
		BatteryPct: result.Response.ChargeState.BatteryLevel,
		PluggedIn:  result.Response.ChargeState.ChargePortLatch == "Engaged",
		IsOnline:   result.Response.State == "online",
	}

	if c.metrics != nil {
		c.metrics.ChargeAmps.Record(ctx, int64(cs.AmpsActual))
		c.metrics.BatteryPct.Record(ctx, cs.BatteryPct)
	}

	return cs, nil
}

// SetChargingAmps sets the charging amperage, clamped to min/max.
func (c *TeslaClient) SetChargingAmps(ctx context.Context, amps int) error {
	c.usage.RecordCommand()
	if amps < c.minAmps {
		amps = c.minAmps
	}
	if amps > c.maxAmps {
		amps = c.maxAmps
	}

	path := fmt.Sprintf("/api/1/vehicles/%s/command/set_charging_amps", c.vin)
	body := map[string]int{"charging_amps": amps}
	resp, err := c.doRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if c.metrics != nil {
		c.metrics.ChargeCommands.Add(ctx, 1, metric.WithAttributes(attribute.String("command", "set_amps")))
	}
	return nil
}

// StartCharging starts the vehicle's charging session.
func (c *TeslaClient) StartCharging(ctx context.Context) error {
	c.usage.RecordCommand()
	path := fmt.Sprintf("/api/1/vehicles/%s/command/charge_start", c.vin)
	resp, err := c.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if c.metrics != nil {
		c.metrics.ChargeCommands.Add(ctx, 1, metric.WithAttributes(attribute.String("command", "start")))
	}
	return nil
}

// StopCharging stops the vehicle's charging session.
func (c *TeslaClient) StopCharging(ctx context.Context) error {
	c.usage.RecordCommand()
	path := fmt.Sprintf("/api/1/vehicles/%s/command/charge_stop", c.vin)
	resp, err := c.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if c.metrics != nil {
		c.metrics.ChargeCommands.Add(ctx, 1, metric.WithAttributes(attribute.String("command", "stop")))
	}
	return nil
}

// WakeUp wakes the vehicle, polling until online or timeout.
func (c *TeslaClient) WakeUp(ctx context.Context) error {
	c.usage.RecordWake()
	path := fmt.Sprintf("/api/1/vehicles/%s/wake_up", c.vin)
	resp, err := c.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()

	// Poll for up to 30 seconds.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		cs, err := c.GetChargeState(ctx)
		if err == nil && cs.IsOnline {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("tesla: wake_up timed out after 30s")
}

// GetAPIUsage returns the current monthly API usage snapshot.
func (c *TeslaClient) GetAPIUsage() APIUsage {
	return c.usage.Snapshot()
}

// RestoreAPIUsage seeds the in-memory usage tracker from a previously
// persisted snapshot. Snapshots older than the current calendar month are
// ignored.
func (c *TeslaClient) RestoreAPIUsage(u APIUsage) {
	c.usage.SetCounts(u.DataCalls, u.CommandCalls, u.WakeCalls, u.StreamSignals, u.MonthStarted)
}

// SetRefreshToken updates the refresh token and immediately refreshes the access token.
func (c *TeslaClient) SetRefreshToken(ctx context.Context, refreshToken string) error {
	if strings.TrimSpace(refreshToken) == "" {
		return fmt.Errorf("tesla: refresh token is required")
	}

	c.mu.Lock()
	c.refreshToken = refreshToken
	c.mu.Unlock()

	return c.refreshAccessToken(ctx)
}
