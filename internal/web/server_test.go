package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cbellee/ev-solar-charger/internal/config"
)

func oauthTestConfig() config.Config {
	return config.Config{
		TeslaTestMode:         false,
		TeslaClientID:         "client-id",
		TeslaClientSecret:     "client-secret",
		TeslaRedirectURI:      "https://tesla.bellee.net/auth/tesla/callback",
		TeslaScope:            "openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds",
		TeslaPublicKeyPEMPath: "/not/real.pem",
		OAuthStateHMACKey:     "state-signing-key",
		TeslaRegion:           "na",
		TeslaTokenPath:        "/tmp/tesla-refresh-token",
	}
}

// denyAuth always rejects with 401. Used to verify protected routes are
// gated by the Authenticator the server is constructed with.
var denyAuth = AuthenticatorFunc(func(_ http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
})

func Test_NewServer_requiresAuthForProtectedRoutes(t *testing.T) {
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, denyAuth, oauthTestConfig())

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if got := w.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("expected WWW-Authenticate header")
	}
}

func Test_NewServer_allowsHealthzWithoutAuth(t *testing.T) {
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, denyAuth, oauthTestConfig())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func Test_NewServer_allowsProtectedRoutesWithAuth(t *testing.T) {
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, NoopAuthenticator{}, oauthTestConfig())

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func Test_NewServer_allowsOAuthStartWithoutAuth(t *testing.T) {
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, denyAuth, oauthTestConfig())

	req := httptest.NewRequest(http.MethodGet, "/auth/tesla", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusFound)
	}
}

func Test_NewServer_allowsPublicKeyEndpointWithoutAuth(t *testing.T) {
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, denyAuth, oauthTestConfig())

	req := httptest.NewRequest(http.MethodGet, "/.well-known/appspecific/com.tesla.3p.public-key.pem", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}
