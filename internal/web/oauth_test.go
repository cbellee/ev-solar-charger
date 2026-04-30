package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
)

type oauthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f oauthRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type oauthTestVehicle struct {
	refreshToken string
	err          error
}

func (m *oauthTestVehicle) GetChargeState(ctx context.Context) (tesla.ChargeState, error) {
	return tesla.ChargeState{}, nil
}
func (m *oauthTestVehicle) SetChargingAmps(ctx context.Context, amps int) error { return nil }
func (m *oauthTestVehicle) SetChargeLimit(ctx context.Context, percent int) error { return nil }
func (m *oauthTestVehicle) StartCharging(ctx context.Context) error             { return nil }
func (m *oauthTestVehicle) StopCharging(ctx context.Context) error              { return nil }
func (m *oauthTestVehicle) WakeUp(ctx context.Context) error                    { return nil }
func (m *oauthTestVehicle) SetRefreshToken(ctx context.Context, refreshToken string) error {
	m.refreshToken = refreshToken
	return m.err
}
func (m *oauthTestVehicle) GetAPIUsage() tesla.APIUsage { return tesla.APIUsage{} }

func oauthConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		TeslaClientID:         "client-id",
		TeslaClientSecret:     "client-secret",
		TeslaRedirectURI:      "https://tesla.bellee.net/auth/tesla/callback",
		TeslaScope:            "openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds",
		TeslaPublicKeyPEMPath: filepath.Join(t.TempDir(), "com.tesla.3p.public-key.pem"),
		OAuthStateHMACKey:     "test-state-signing-key",
		TeslaRegion:           "na",
		TeslaTokenPath:        filepath.Join(t.TempDir(), "tesla-refresh-token"),
	}
}

func installTokenEndpointRedirect(t *testing.T, client *http.Client, handler http.Handler) {
	t.Helper()

	tokenServer := httptest.NewTLSServer(handler)
	t.Cleanup(tokenServer.Close)

	targetURL, err := url.Parse(tokenServer.URL)
	if err != nil {
		t.Fatalf("failed to parse test token URL: %v", err)
	}

	baseTransport := http.DefaultTransport
	targetHost := "fleet-auth.prd.vn.cloud.tesla.com"
	client.Transport = oauthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == targetHost {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = targetURL.Scheme
			clone.URL.Host = targetURL.Host
			clone.Host = targetURL.Host
			return tokenServer.Client().Transport.RoundTrip(clone)
		}
		return baseTransport.RoundTrip(req)
	})
}

func TestOAuthHandlePublicKey_servesConfiguredFile(t *testing.T) {
	cfg := oauthConfig(t)
	pemData := "-----BEGIN PUBLIC KEY-----\nabc123\n-----END PUBLIC KEY-----\n"
	if err := os.WriteFile(cfg.TeslaPublicKeyPEMPath, []byte(pemData), 0o600); err != nil {
		t.Fatalf("write pem file: %v", err)
	}

	oauth := newOAuthServer(cfg, &oauthTestVehicle{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/appspecific/com.tesla.3p.public-key.pem", nil)
	w := httptest.NewRecorder()
	oauth.handlePublicKey(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Content-Type"); got != "application/x-pem-file" {
		t.Fatalf("content-type = %q, want %q", got, "application/x-pem-file")
	}
	if w.Body.String() != pemData {
		t.Fatalf("body = %q, want pem", w.Body.String())
	}
}

func TestOAuthHandleStart_redirectsToTeslaAndSetsStateCookie(t *testing.T) {
	cfg := oauthConfig(t)
	oauth := newOAuthServer(cfg, &oauthTestVehicle{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/tesla", nil)
	w := httptest.NewRecorder()
	oauth.handleOAuthStart(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusFound)
	}
	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, "https://auth.tesla.com/oauth2/v3/authorize") {
		t.Fatalf("location = %q, want Tesla authorize endpoint", location)
	}
	redirected, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	q := redirected.Query()
	if q.Get("client_id") != cfg.TeslaClientID {
		t.Fatalf("client_id = %q, want %q", q.Get("client_id"), cfg.TeslaClientID)
	}
	if q.Get("redirect_uri") != cfg.TeslaRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", q.Get("redirect_uri"), cfg.TeslaRedirectURI)
	}
	if q.Get("state") == "" {
		t.Fatal("state was not set")
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected state cookie")
	}
}

func TestOAuthHandleCallback_rejectsInvalidState(t *testing.T) {
	cfg := oauthConfig(t)
	oauth := newOAuthServer(cfg, &oauthTestVehicle{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/tesla/callback?code=abc&state=expected", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: signStateValue("wrong", cfg.OAuthStateHMACKey)})
	w := httptest.NewRecorder()
	oauth.handleOAuthCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), "Invalid OAuth state") {
		t.Fatalf("body = %q, want Invalid OAuth state", w.Body.String())
	}
}

func TestOAuthHandleCallback_exchangesAndPersistsToken(t *testing.T) {
	cfg := oauthConfig(t)
	vehicle := &oauthTestVehicle{}
	oauth := newOAuthServer(cfg, vehicle, nil)

	installTokenEndpointRedirect(t, oauth.httpClient, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Fatalf("grant_type = %q, want authorization_code", r.Form.Get("grant_type"))
		}
		if r.Form.Get("redirect_uri") != cfg.TeslaRedirectURI {
			t.Fatalf("redirect_uri = %q, want %q", r.Form.Get("redirect_uri"), cfg.TeslaRedirectURI)
		}
		if r.Form.Get("audience") != audienceForRegion(cfg.TeslaRegion) {
			t.Fatalf("audience = %q, want %q", r.Form.Get("audience"), audienceForRegion(cfg.TeslaRegion))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-123",
			"refresh_token": "refresh-456",
			"expires_in":    300,
		})
	}))

	state := "state-123"
	req := httptest.NewRequest(http.MethodGet, "/auth/tesla/callback?code=code-abc&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: signStateValue(state, cfg.OAuthStateHMACKey)})
	w := httptest.NewRecorder()
	oauth.handleOAuthCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%q", w.Code, http.StatusOK, w.Body.String())
	}
	if vehicle.refreshToken != "refresh-456" {
		t.Fatalf("vehicle refresh token = %q, want %q", vehicle.refreshToken, "refresh-456")
	}
	tokenBytes, err := os.ReadFile(cfg.TeslaTokenPath)
	if err != nil {
		t.Fatalf("read token path: %v", err)
	}
	if strings.TrimSpace(string(tokenBytes)) != "refresh-456" {
		t.Fatalf("token file = %q, want %q", strings.TrimSpace(string(tokenBytes)), "refresh-456")
	}
}
