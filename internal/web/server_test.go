package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func Test_NewServer_requiresAuthForProtectedRoutes(t *testing.T) {
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, AuthConfig{
		Username: "admin",
		Password: "secret",
	})

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
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, AuthConfig{
		Username: "admin",
		Password: "secret",
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func Test_NewServer_allowsProtectedRoutesWithAuth(t *testing.T) {
	handler := NewServer(newTestCtrl(t), &nullStore{}, NewHub(nil), nil, AuthConfig{
		Username: "admin",
		Password: "secret",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}
