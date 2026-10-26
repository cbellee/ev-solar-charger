package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/storage"
)

const (
	testTenant = "tenant-1"
	testOID    = "oid-1"
)

// memSessionStore is an in-memory SessionStore for tests.
type memSessionStore struct {
	mu         sync.Mutex
	sessions   map[string]storage.AuthSession
	usedNonces map[string]time.Time
	renewals   int
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{sessions: map[string]storage.AuthSession{}, usedNonces: map[string]time.Time{}}
}

func (s *memSessionStore) CreateAuthSession(_ context.Context, a storage.AuthSession, nonceExpiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, used := s.usedNonces[a.NonceHash]; used {
		return storage.ErrLoginNonceUsed
	}
	s.usedNonces[a.NonceHash] = nonceExpiresAt
	s.sessions[a.IDHash] = a
	return nil
}

func (s *memSessionStore) GetAuthSession(_ context.Context, idHash string) (storage.AuthSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.sessions[idHash]
	if !ok {
		return storage.AuthSession{}, storage.ErrAuthSessionNotFound
	}
	return a, nil
}

func (s *memSessionStore) RenewAuthSession(_ context.Context, idHash string, expiresAt, renewedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.sessions[idHash]
	a.ExpiresAt, a.RenewedAt = expiresAt, renewedAt
	s.sessions[idHash] = a
	s.renewals++
	return nil
}

func (s *memSessionStore) DeleteAuthSessionsForUser(_ context.Context, oid string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, a := range s.sessions {
		if a.OID == oid {
			delete(s.sessions, k)
			n++
		}
	}
	return n, nil
}

func (s *memSessionStore) PruneAuthSessions(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.usedNonces {
		if !now.Before(exp) {
			delete(s.usedNonces, k)
		}
	}
	var n int64
	for k, a := range s.sessions {
		if !now.Before(a.ExpiresAt) {
			delete(s.sessions, k)
			n++
		}
	}
	return n, nil
}

func (s *memSessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// testClock is a settable clock shared by the manager under test.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestSessionManager(t *testing.T, store SessionStore, clock *testClock) *SessionManager {
	t.Helper()
	m, err := NewSessionManager(store, "test-secret", 24*time.Hour, 7*24*time.Hour, discardLogger())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	m.now = clock.now
	return m
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// startTestLogin runs startLogin and returns the login cookie and nonce.
func startTestLogin(t *testing.T, m *SessionManager) (*http.Cookie, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	nonce, err := m.startLogin(rec)
	if err != nil {
		t.Fatalf("startLogin: %v", err)
	}
	c := findCookie(rec.Result().Cookies(), loginCookieName)
	if c == nil {
		t.Fatal("login cookie not set")
	}
	return c, nonce
}

// createTestSession creates a session and returns its cookie.
func createTestSession(t *testing.T, m *SessionManager, clock *testClock) *http.Cookie {
	t.Helper()
	login, nonce := startTestLogin(t, m)
	r := httptest.NewRequest(http.MethodPost, "/auth/session", nil)
	r.AddCookie(login)
	rec := httptest.NewRecorder()
	claims := &entraClaims{OID: testOID, TID: testTenant, PrefUN: "user@example.com", Nonce: nonce, IAT: clock.now().Unix()}
	if err := m.create(context.Background(), rec, r, claims); err != nil {
		t.Fatalf("create: %v", err)
	}
	c := findCookie(rec.Result().Cookies(), SessionCookieName)
	if c == nil {
		t.Fatal("session cookie not set")
	}
	return c
}

func TestNewSessionManagerValidation(t *testing.T) {
	store := newMemSessionStore()
	tests := []struct {
		name    string
		store   SessionStore
		secret  string
		idle    time.Duration
		maxAge  time.Duration
		wantErr bool
	}{
		{"valid", store, "s", time.Hour, 2 * time.Hour, false},
		{"nil store", nil, "s", time.Hour, 2 * time.Hour, true},
		{"empty secret", store, " ", time.Hour, 2 * time.Hour, true},
		{"zero idle", store, "s", 0, 2 * time.Hour, true},
		{"max below idle", store, "s", 2 * time.Hour, time.Hour, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSessionManager(tt.store, tt.secret, tt.idle, tt.maxAge, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSessionCreateRejections(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(claims *entraClaims, r *http.Request, now time.Time)
		noLogin bool
		wantErr error
	}{
		{name: "no login cookie", noLogin: true, wantErr: errNoLogin},
		{name: "nonce mismatch", mutate: func(c *entraClaims, _ *http.Request, _ time.Time) { c.Nonce = "attacker-nonce" }, wantErr: errNonceMismatch},
		{name: "missing nonce", mutate: func(c *entraClaims, _ *http.Request, _ time.Time) { c.Nonce = "" }, wantErr: errNonceMismatch},
		{name: "stale token", mutate: func(c *entraClaims, _ *http.Request, now time.Time) { c.IAT = now.Add(-loginTTL - time.Second).Unix() }, wantErr: errStaleIDToken},
		{name: "future token", mutate: func(c *entraClaims, _ *http.Request, now time.Time) {
			c.IAT = now.Add(maxClockSkew + time.Minute).Unix()
		}, wantErr: errStaleIDToken},
		{name: "missing iat", mutate: func(c *entraClaims, _ *http.Request, _ time.Time) { c.IAT = 0 }, wantErr: errStaleIDToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &testClock{t: time.Unix(1_700_000_000, 0)}
			store := newMemSessionStore()
			m := newTestSessionManager(t, store, clock)
			login, nonce := startTestLogin(t, m)
			r := httptest.NewRequest(http.MethodPost, "/auth/session", nil)
			if !tt.noLogin {
				r.AddCookie(login)
			}
			claims := &entraClaims{OID: testOID, TID: testTenant, Nonce: nonce, IAT: clock.now().Unix()}
			if tt.mutate != nil {
				tt.mutate(claims, r, clock.now())
			}
			err := m.create(context.Background(), httptest.NewRecorder(), r, claims)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if store.count() != 0 {
				t.Fatalf("sessions = %d, want 0", store.count())
			}
		})
	}
}

func TestSessionNonceIsSingleUse(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	m := newTestSessionManager(t, newMemSessionStore(), clock)
	login, nonce := startTestLogin(t, m)
	claims := &entraClaims{OID: testOID, TID: testTenant, Nonce: nonce, IAT: clock.now().Unix()}

	for i, want := range []error{nil, storage.ErrLoginNonceUsed} {
		r := httptest.NewRequest(http.MethodPost, "/auth/session", nil)
		r.AddCookie(login)
		if err := m.create(context.Background(), httptest.NewRecorder(), r, claims); !errors.Is(err, want) {
			t.Fatalf("attempt %d: err = %v, want %v", i, err, want)
		}
	}
}

// A captured exchange (ID token + login cookie) must stay unusable after the
// user signs out, which deletes their sessions.
func TestSessionNonceStaysUsedAfterRevoke(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	m := newTestSessionManager(t, newMemSessionStore(), clock)
	login, nonce := startTestLogin(t, m)
	claims := &entraClaims{OID: testOID, TID: testTenant, Nonce: nonce, IAT: clock.now().Unix()}

	exchange := func() error {
		r := httptest.NewRequest(http.MethodPost, "/auth/session", nil)
		r.AddCookie(login)
		return m.create(context.Background(), httptest.NewRecorder(), r, claims)
	}
	if err := exchange(); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if _, err := m.revokeUser(context.Background(), testOID); err != nil {
		t.Fatalf("revokeUser: %v", err)
	}
	clock.advance(time.Minute)
	if err := exchange(); !errors.Is(err, storage.ErrLoginNonceUsed) {
		t.Fatalf("replay after sign-out: err = %v, want ErrLoginNonceUsed", err)
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	m := newTestSessionManager(t, newMemSessionStore(), clock)
	c := createTestSession(t, m, clock)
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("unexpected cookie attributes: %+v", c)
	}
	if c.MaxAge != int((24 * time.Hour).Seconds()) {
		t.Fatalf("MaxAge = %d, want 24h", c.MaxAge)
	}
}

func TestSessionLookup(t *testing.T) {
	tests := []struct {
		name    string
		steps   []time.Duration // advance + renewing lookup, in order
		final   time.Duration
		wantErr error
	}{
		{name: "fresh", final: time.Minute},
		{name: "idle expired", final: 24 * time.Hour, wantErr: errSessionExpired},
		{name: "kept alive by use", steps: []time.Duration{20 * time.Hour, 20 * time.Hour}, final: 20 * time.Hour},
		{
			name: "absolute cap",
			steps: []time.Duration{
				20 * time.Hour, 20 * time.Hour, 20 * time.Hour, 20 * time.Hour,
				20 * time.Hour, 20 * time.Hour, 20 * time.Hour, 20 * time.Hour,
			},
			// 160h of activity, then 8h more: inside idle, past the 7d cap.
			final:   8 * time.Hour,
			wantErr: errSessionExpired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &testClock{t: time.Unix(1_700_000_000, 0)}
			m := newTestSessionManager(t, newMemSessionStore(), clock)
			cookie := createTestSession(t, m, clock)
			for i, d := range tt.steps {
				clock.advance(d)
				r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
				r.AddCookie(cookie)
				_, err := m.lookup(context.Background(), httptest.NewRecorder(), r, true)
				if err != nil && !errors.Is(err, errSessionExpired) {
					t.Fatalf("step %d: %v", i, err)
				}
			}
			clock.advance(tt.final)
			r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
			r.AddCookie(cookie)
			_, err := m.lookup(context.Background(), httptest.NewRecorder(), r, true)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSessionLookupErrors(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	m := newTestSessionManager(t, newMemSessionStore(), clock)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := m.lookup(context.Background(), httptest.NewRecorder(), r, true); !errors.Is(err, errNoSession) {
		t.Fatalf("no cookie: err = %v, want errNoSession", err)
	}
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "forged"})
	if _, err := m.lookup(context.Background(), httptest.NewRecorder(), r, true); !errors.Is(err, errSessionExpired) {
		t.Fatalf("unknown cookie: err = %v, want errSessionExpired", err)
	}
}

func TestSessionRenewalIsThrottled(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	store := newMemSessionStore()
	m := newTestSessionManager(t, store, clock)
	cookie := createTestSession(t, m, clock)

	lookup := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(cookie)
		rec := httptest.NewRecorder()
		if _, err := m.lookup(context.Background(), rec, r, true); err != nil {
			t.Fatalf("lookup: %v", err)
		}
		return rec
	}

	clock.advance(time.Minute)
	if rec := lookup(); store.renewals != 0 || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("renewed before renewInterval: renewals=%d", store.renewals)
	}
	clock.advance(renewInterval)
	if rec := lookup(); store.renewals != 1 || findCookie(rec.Result().Cookies(), SessionCookieName) == nil {
		t.Fatalf("expected renewal and refreshed cookie: renewals=%d", store.renewals)
	}
}

func TestSessionRevokeUser(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	store := newMemSessionStore()
	m := newTestSessionManager(t, store, clock)
	first := createTestSession(t, m, clock)
	second := createTestSession(t, m, clock)

	n, err := m.revokeUser(context.Background(), testOID)
	if err != nil || n != 2 {
		t.Fatalf("revokeUser = %d, %v; want 2, nil", n, err)
	}
	for _, c := range []*http.Cookie{first, second} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(c)
		if _, err := m.lookup(context.Background(), httptest.NewRecorder(), r, true); !errors.Is(err, errSessionExpired) {
			t.Fatalf("revoked cookie still valid: %v", err)
		}
	}
}

// --- Entra authenticator integration -------------------------------------

type entraFixture struct {
	auth   *EntraAuthenticator
	store  *memSessionStore
	clock  *testClock
	tokens map[string]*entraClaims
}

func newEntraFixture(t *testing.T, allowed []string) *entraFixture {
	t.Helper()
	f := &entraFixture{
		store:  newMemSessionStore(),
		clock:  &testClock{t: time.Unix(1_700_000_000, 0)},
		tokens: map[string]*entraClaims{},
	}
	m := newTestSessionManager(t, f.store, f.clock)
	f.auth = newEntraAuthenticator(EntraConfig{
		TenantID:    testTenant,
		ClientID:    "client",
		AllowedOIDs: allowed,
		Sessions:    m,
		Logger:      discardLogger(),
	}, func(_ context.Context, raw string) (*entraClaims, error) {
		c, ok := f.tokens[raw]
		if !ok {
			return nil, errors.New("bad token")
		}
		cp := *c
		return &cp, nil
	})
	return f
}

func (f *entraFixture) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	sh := f.auth.SessionHandler()
	mux.Handle("/auth/session", sh)
	mux.Handle("/auth/session/login", sh)
	mux.Handle("/api/", f.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	mux.ServeHTTP(rec, r)
	return rec
}

// signIn performs the full nonce-bound flow and returns the session cookie.
func (f *entraFixture) signIn(t *testing.T) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/auth/session/login", nil)
	r.Header.Set(csrfHeader, "XMLHttpRequest")
	rec := f.serve(r)
	if rec.Code != http.StatusOK {
		t.Fatalf("login start status = %d", rec.Code)
	}
	var body struct{ Nonce string }
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Nonce == "" {
		t.Fatalf("decode nonce: %v %q", err, body.Nonce)
	}
	login := findCookie(rec.Result().Cookies(), loginCookieName)

	f.tokens["id-token"] = &entraClaims{OID: testOID, TID: testTenant, PrefUN: "user@example.com", Nonce: body.Nonce, IAT: f.clock.now().Unix()}
	r = httptest.NewRequest(http.MethodPost, "/auth/session", nil)
	r.Header.Set("Authorization", "Bearer id-token")
	r.Header.Set(csrfHeader, "XMLHttpRequest")
	r.AddCookie(login)
	rec = f.serve(r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body)
	}
	c := findCookie(rec.Result().Cookies(), SessionCookieName)
	if c == nil {
		t.Fatal("no session cookie")
	}
	return c
}

func TestEntraSessionEndpoints(t *testing.T) {
	f := newEntraFixture(t, nil)
	cookie := f.signIn(t)

	r := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	r.AddCookie(cookie)
	rec := f.serve(r)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "user@example.com") {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}

	// A stolen ID token replayed without the victim's login cookie fails.
	r = httptest.NewRequest(http.MethodPost, "/auth/session", nil)
	r.Header.Set("Authorization", "Bearer id-token")
	r.Header.Set(csrfHeader, "XMLHttpRequest")
	if rec := f.serve(r); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replay without login cookie: status = %d", rec.Code)
	}

	r = httptest.NewRequest(http.MethodDelete, "/auth/session", nil)
	r.Header.Set(csrfHeader, "XMLHttpRequest")
	r.AddCookie(cookie)
	rec = f.serve(r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", rec.Code)
	}
	if c := findCookie(rec.Result().Cookies(), SessionCookieName); c == nil || c.MaxAge >= 0 {
		t.Fatalf("session cookie not cleared: %+v", c)
	}
	if f.store.count() != 0 {
		t.Fatalf("sessions remaining = %d", f.store.count())
	}

	r = httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	r.AddCookie(cookie)
	if rec := f.serve(r); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET after DELETE: status = %d", rec.Code)
	}
}

func TestEntraSessionCSRFHeaderRequired(t *testing.T) {
	f := newEntraFixture(t, nil)
	cookie := f.signIn(t)
	tests := []struct {
		method, path string
	}{
		{http.MethodPost, "/auth/session/login"},
		{http.MethodPost, "/auth/session"},
		{http.MethodDelete, "/auth/session"},
		{http.MethodPost, "/api/control"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.AddCookie(cookie)
			if rec := f.serve(r); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
		})
	}
	if f.store.count() != 1 {
		t.Fatalf("session revoked without CSRF header")
	}
}

func TestEntraMiddlewareWithSession(t *testing.T) {
	f := newEntraFixture(t, nil)
	cookie := f.signIn(t)
	f.tokens["good"] = &entraClaims{OID: testOID, TID: testTenant}

	tests := []struct {
		name     string
		method   string
		cookie   bool
		auth     string
		csrf     bool
		wantCode int
	}{
		{"cookie GET", http.MethodGet, true, "", false, http.StatusOK},
		{"cookie POST with header", http.MethodPost, true, "", true, http.StatusOK},
		{"cookie POST without header", http.MethodPost, true, "", false, http.StatusForbidden},
		{"no credentials", http.MethodGet, false, "", false, http.StatusUnauthorized},
		{"bearer wins over cookie", http.MethodGet, true, "Bearer bad", false, http.StatusUnauthorized},
		{"bearer POST without header", http.MethodPost, false, "Bearer good", false, http.StatusOK},
		{"malformed auth header", http.MethodGet, true, "Basic abc", false, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/api/control", nil)
			if tt.cookie {
				r.AddCookie(cookie)
			}
			if tt.auth != "" {
				r.Header.Set("Authorization", tt.auth)
			}
			if tt.csrf {
				r.Header.Set(csrfHeader, "XMLHttpRequest")
			}
			if rec := f.serve(r); rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}

func TestEntraSessionAllowlistRechecked(t *testing.T) {
	f := newEntraFixture(t, []string{testOID})
	cookie := f.signIn(t)

	f.auth.cfg.AllowedOIDs = []string{"someone-else"}
	r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	r.AddCookie(cookie)
	if rec := f.serve(r); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 after removal from allowlist", rec.Code)
	}
}

func TestEntraSessionHandlerDisabled(t *testing.T) {
	a := newEntraAuthenticator(EntraConfig{TenantID: testTenant, Logger: discardLogger()}, nil)
	r := httptest.NewRequest(http.MethodPost, "/auth/session/login", nil)
	r.Header.Set(csrfHeader, "XMLHttpRequest")
	rec := httptest.NewRecorder()
	a.SessionHandler().ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
