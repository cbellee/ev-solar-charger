package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type mockSecretStore struct {
	name  string
	value string
	err   error
}

func (m *mockSecretStore) SetSecret(_ context.Context, name, value string) error {
	m.name = name
	m.value = value
	return m.err
}

func testConfig(tokenEndpoint string) Config {
	return Config{
		ClientID:               "client-id",
		ClientSecret:           "client-secret",
		RedirectURI:            "http://localhost:7071/oauth/callback",
		Region:                 "na",
		Scope:                  defaultTeslaScope,
		PublicKeyPEM:           "-----BEGIN PUBLIC KEY-----\nabc123\n-----END PUBLIC KEY-----",
		StateSigningKey:        "state-signing-key",
		KeyVaultURI:            "https://example.vault.azure.net/",
		RefreshTokenSecretName: "tesla-refresh-token",
		AuthorizeEndpoint:      "https://auth.example.test/oauth2/v3/authorize",
		TokenEndpoint:          tokenEndpoint,
		SecureCookies:          false,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPublicKeyHandler(t *testing.T) {
	srv := NewServer(testConfig("https://token.example.test"), &mockSecretStore{}, testLogger())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/.well-known/appspecific/com.tesla.3p.public-key.pem", nil)

	srv.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/x-pem-file" {
		t.Fatalf("content-type = %q, want %q", got, "application/x-pem-file")
	}
	if !strings.Contains(recorder.Body.String(), "BEGIN PUBLIC KEY") {
		t.Fatalf("expected PEM payload, got %q", recorder.Body.String())
	}
}

func TestOAuthStartRedirectsAndSetsStateCookie(t *testing.T) {
	srv := NewServer(testConfig("https://token.example.test"), &mockSecretStore{}, testLogger())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/oauth/start", nil)

	srv.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	location := recorder.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	if parsed.Host != "auth.example.test" {
		t.Fatalf("redirect host = %q, want %q", parsed.Host, "auth.example.test")
	}
	query := parsed.Query()
	if query.Get("client_id") != "client-id" {
		t.Fatalf("client_id = %q, want %q", query.Get("client_id"), "client-id")
	}
	if query.Get("redirect_uri") != "http://localhost:7071/oauth/callback" {
		t.Fatalf("redirect_uri = %q", query.Get("redirect_uri"))
	}
	if query.Get("state") == "" {
		t.Fatal("expected state query parameter to be set")
	}
	if len(recorder.Result().Cookies()) == 0 {
		t.Fatal("expected state cookie to be set")
	}
}

func TestOAuthCallbackRejectsInvalidState(t *testing.T) {
	srv := NewServer(testConfig("https://token.example.test"), &mockSecretStore{}, testLogger())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-123&state=expected-state", nil)
	request.AddCookie(&http.Cookie{Name: stateCookieName, Value: signStateValue("different-state", "state-signing-key")})

	srv.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "Invalid OAuth state") {
		t.Fatalf("unexpected body %q", recorder.Body.String())
	}
}

func TestOAuthCallbackExchangesCodeAndPersistsRefreshToken(t *testing.T) {
	var tokenRequest url.Values
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		tokenRequest, err = url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-123","refresh_token":"refresh-456","expires_in":300}`))
	}))
	defer tokenServer.Close()

	cfg := testConfig(tokenServer.URL)
	store := &mockSecretStore{}
	srv := NewServer(cfg, store, testLogger())
	srv.httpClient = tokenServer.Client()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-123&state=expected-state", nil)
	request.AddCookie(&http.Cookie{Name: stateCookieName, Value: signStateValue("expected-state", cfg.StateSigningKey)})

	srv.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if store.name != cfg.RefreshTokenSecretName {
		t.Fatalf("secret name = %q, want %q", store.name, cfg.RefreshTokenSecretName)
	}
	if store.value != "refresh-456" {
		t.Fatalf("refresh token = %q, want %q", store.value, "refresh-456")
	}
	if tokenRequest.Get("grant_type") != "authorization_code" {
		t.Fatalf("grant_type = %q, want authorization_code", tokenRequest.Get("grant_type"))
	}
	if tokenRequest.Get("audience") != audienceByRegion["na"] {
		t.Fatalf("audience = %q, want %q", tokenRequest.Get("audience"), audienceByRegion["na"])
	}
	if tokenRequest.Get("redirect_uri") != cfg.RedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", tokenRequest.Get("redirect_uri"), cfg.RedirectURI)
	}
	if !strings.Contains(recorder.Body.String(), cfg.RefreshTokenSecretName) {
		t.Fatalf("expected success body to mention secret name, got %q", recorder.Body.String())
	}
}
