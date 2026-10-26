package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/storage"
	"github.com/coreos/go-oidc/v3/oidc"
)

// EntraConfig configures Microsoft Entra ID (Azure AD) ID-token validation
// for protected routes. The client (the React SPA) must obtain an ID token
// via MSAL and present it as `Authorization: Bearer <id_token>`.
type EntraConfig struct {
	// TenantID is the Entra tenant (directory) ID. Used to derive the
	// issuer URL and to validate the `tid` claim.
	TenantID string
	// ClientID is the Entra app registration's application (client) ID.
	// It is the expected `aud` claim on incoming ID tokens.
	ClientID string
	// AllowedOIDs, when non-empty, restricts access to tokens whose `oid`
	// claim is in the list. If empty, any user from TenantID is allowed.
	AllowedOIDs []string
	// Sessions, when non-nil, enables the server-issued session cookie as an
	// alternative to a bearer token. Optional.
	Sessions *SessionManager
	// Logger receives audit-style messages for denied / accepted requests.
	// Optional.
	Logger *slog.Logger
}

// EntraAuthenticator validates Microsoft Entra ID tokens on incoming requests
// and, when Sessions is configured, the server-issued session cookie.
type EntraAuthenticator struct {
	cfg      EntraConfig
	verify   func(ctx context.Context, rawToken string) (*entraClaims, error)
	sessions *SessionManager
	logger   *slog.Logger
	// streamRecheck is how often an open cookie-authenticated stream
	// re-checks that its session has not been revoked or expired.
	streamRecheck time.Duration
}

// NewEntraAuthenticator discovers the tenant's OIDC metadata and prepares
// an ID-token verifier. It performs a network call to the discovery endpoint;
// callers should pass a context with a sensible timeout for startup.
func NewEntraAuthenticator(ctx context.Context, cfg EntraConfig) (*EntraAuthenticator, error) {
	if strings.TrimSpace(cfg.TenantID) == "" {
		return nil, errors.New("entra: TenantID is required")
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("entra: ClientID is required")
	}

	issuer := fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", cfg.TenantID)
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("entra: discover provider: %w", err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	return newEntraAuthenticator(cfg, func(ctx context.Context, rawToken string) (*entraClaims, error) {
		// Verify signature, issuer, audience, expiry.
		idToken, err := verifier.Verify(ctx, rawToken)
		if err != nil {
			return nil, err
		}
		var claims entraClaims
		if err := idToken.Claims(&claims); err != nil {
			return nil, fmt.Errorf("decode claims: %w", err)
		}
		return &claims, nil
	}), nil
}

func newEntraAuthenticator(cfg EntraConfig, verify func(context.Context, string) (*entraClaims, error)) *EntraAuthenticator {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &EntraAuthenticator{
		cfg:           cfg,
		verify:        verify,
		sessions:      cfg.Sessions,
		logger:        logger,
		streamRecheck: defaultStreamRecheck,
	}
}

// entraClaims is the subset of ID-token claims the middleware inspects after
// signature/audience/issuer/expiry checks pass.
type entraClaims struct {
	OID    string `json:"oid"`
	TID    string `json:"tid"`
	UPN    string `json:"upn"`
	PrefUN string `json:"preferred_username"`
	// Nonce and IAT bind session creation to a server-started sign-in.
	Nonce string `json:"nonce"`
	IAT   int64  `json:"iat"`
	// Exp bounds how long a bearer-authenticated stream may stay open.
	Exp int64 `json:"exp"`
}

func (c *entraClaims) displayName() string {
	if c.PrefUN != "" {
		return c.PrefUN
	}
	return c.UPN
}

// authError is a request-denial outcome with its HTTP status and error code.
type authError struct {
	status int
	code   string
	detail string
}

// authorize applies tenant pinning and the optional OID allowlist. It runs on
// every request, including cookie-authenticated ones, so removing a user from
// ENTRA_ALLOWED_OIDS revokes their session immediately.
func (a *EntraAuthenticator) authorize(claims *entraClaims) *authError {
	// Tenant pinning: reject tokens minted for a different tenant even
	// if the audience matches (defence-in-depth against multi-tenant
	// app misconfiguration).
	if !strings.EqualFold(claims.TID, a.cfg.TenantID) {
		return &authError{http.StatusForbidden, "wrong_tenant", "tid does not match configured tenant"}
	}
	if len(a.cfg.AllowedOIDs) > 0 && !containsFold(a.cfg.AllowedOIDs, claims.OID) {
		return &authError{http.StatusForbidden, "user_not_allowed", "oid not in allowlist"}
	}
	return nil
}

func (a *EntraAuthenticator) verifyBearer(ctx context.Context, token string) (*entraClaims, *authError) {
	claims, err := a.verify(ctx, token)
	if err != nil {
		return nil, &authError{http.StatusUnauthorized, "invalid_token", err.Error()}
	}
	if aerr := a.authorize(claims); aerr != nil {
		return nil, aerr
	}
	return claims, nil
}

// verifySession validates the session cookie and slides its idle expiry. It
// returns the session's ID hash so long-lived streams can re-check it.
func (a *EntraAuthenticator) verifySession(w http.ResponseWriter, r *http.Request) (*entraClaims, string, *authError) {
	if a.sessions == nil {
		return nil, "", &authError{http.StatusUnauthorized, "missing_token", "authorization header missing"}
	}
	sess, err := a.sessions.lookup(r.Context(), w, r, true)
	switch {
	case errors.Is(err, errNoSession):
		return nil, "", &authError{http.StatusUnauthorized, "missing_token", "authorization header or session cookie missing"}
	case errors.Is(err, errSessionExpired):
		return nil, "", &authError{http.StatusUnauthorized, "invalid_session", err.Error()}
	case err != nil:
		a.logger.Error("entra: session lookup failed", "error", err)
		return nil, "", &authError{http.StatusServiceUnavailable, "session_unavailable", "session store unavailable"}
	}
	claims := &entraClaims{OID: sess.OID, TID: sess.TID, PrefUN: sess.Name}
	if aerr := a.authorize(claims); aerr != nil {
		return nil, "", aerr
	}
	return claims, sess.IDHash, nil
}

// Middleware enforces either a valid bearer ID token or, when no bearer token
// is supplied, a valid session cookie on every request reaching `next`.
// Cookie-authenticated unsafe methods must carry the X-Requested-With header
// as CSRF defence. Failures return 401/403 with a small JSON error body.
// Successful requests have the claims attached to the request context.
func (a *EntraAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var (
			claims     *entraClaims
			sessIDHash string
			aerr       *authError
		)
		token, err := bearerToken(r)
		switch {
		case err == nil:
			claims, aerr = a.verifyBearer(r.Context(), token)
		case r.Header.Get("Authorization") != "":
			aerr = &authError{http.StatusUnauthorized, "missing_token", err.Error()}
		case !isSafeMethod(r.Method) && r.Header.Get(csrfHeader) == "":
			aerr = &authError{http.StatusForbidden, "csrf_header_required", csrfHeader + " header required"}
		default:
			claims, sessIDHash, aerr = a.verifySession(w, r)
		}
		if aerr != nil {
			a.deny(w, r, aerr)
			return
		}

		// Attach claims to context for downstream handlers.
		ctx := context.WithValue(r.Context(), entraClaimsKey{}, claims)
		if isEventStream(r) {
			var cancel context.CancelFunc
			ctx, cancel = a.streamContext(ctx, claims, sessIDHash)
			defer cancel()
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SessionHandler serves the session endpoints:
//   - POST /auth/session/login starts a sign-in and returns the nonce the SPA
//     must pass to Entra.
//   - POST /auth/session exchanges the ID token from that sign-in (bearer)
//     for a session cookie.
//   - GET /auth/session reports whether the session cookie is valid.
//   - DELETE /auth/session revokes all of the caller's sessions.
//
// Every route returns 404 when sessions are not configured.
func (a *EntraAuthenticator) SessionHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/session/login", a.handleSessionLogin)
	mux.HandleFunc("POST /auth/session", a.handleSessionCreate)
	mux.HandleFunc("GET /auth/session", a.handleSessionGet)
	mux.HandleFunc("DELETE /auth/session", a.handleSessionDelete)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if a.sessions == nil {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *EntraAuthenticator) handleSessionLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) == "" {
		a.deny(w, r, &authError{http.StatusForbidden, "csrf_header_required", csrfHeader + " header required"})
		return
	}
	nonce, err := a.sessions.startLogin(w)
	if err != nil {
		a.logger.Error("entra: start login failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "login_start_failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"nonce": nonce})
}

func (a *EntraAuthenticator) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) == "" {
		a.deny(w, r, &authError{http.StatusForbidden, "csrf_header_required", csrfHeader + " header required"})
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		a.deny(w, r, &authError{http.StatusUnauthorized, "missing_token", "bearer ID token required"})
		return
	}
	claims, aerr := a.verifyBearer(r.Context(), strings.TrimSpace(token))
	if aerr != nil {
		a.deny(w, r, aerr)
		return
	}
	err := a.sessions.create(r.Context(), w, r, claims)
	switch {
	case err == nil:
	case errors.Is(err, errNoLogin), errors.Is(err, errNonceMismatch), errors.Is(err, errStaleIDToken),
		errors.Is(err, storage.ErrLoginNonceUsed):
		a.deny(w, r, &authError{http.StatusUnauthorized, "login_not_verified", err.Error()})
		return
	default:
		a.logger.Error("entra: create session failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "session_create_failed")
		return
	}
	a.logger.Info("entra: session started", "oid", claims.OID, "remote", r.RemoteAddr)
	w.WriteHeader(http.StatusNoContent)
}

func (a *EntraAuthenticator) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	claims, _, aerr := a.verifySession(w, r)
	if aerr != nil {
		if aerr.code == "invalid_session" || aerr.status == http.StatusForbidden {
			a.sessions.clear(w)
		}
		a.deny(w, r, aerr)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"name": claims.displayName()})
}

// handleSessionDelete signs the caller out everywhere: every session for
// their OID is deleted, so copied cookies and other devices stop working.
func (a *EntraAuthenticator) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) == "" {
		a.deny(w, r, &authError{http.StatusForbidden, "csrf_header_required", csrfHeader + " header required"})
		return
	}
	var oid string
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if claims, aerr := a.verifyBearer(r.Context(), strings.TrimSpace(token)); aerr == nil {
			oid = claims.OID
		}
	}
	if oid == "" {
		if sess, err := a.sessions.lookup(r.Context(), w, r, false); err == nil {
			oid = sess.OID
		}
	}
	if oid != "" {
		n, err := a.sessions.revokeUser(r.Context(), oid)
		if err != nil {
			a.logger.Error("entra: revoke sessions failed", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "session_revoke_failed")
			return
		}
		a.logger.Info("entra: sessions revoked", "oid", oid, "count", n, "remote", r.RemoteAddr)
	}
	a.sessions.clear(w)
	w.WriteHeader(http.StatusNoContent)
}

type entraClaimsKey struct{}

// ClaimsFromContext returns the verified Entra claims for the current request,
// or nil if the request bypassed authentication.
func ClaimsFromContext(ctx context.Context) *entraClaims {
	v, _ := ctx.Value(entraClaimsKey{}).(*entraClaims)
	return v
}

func (a *EntraAuthenticator) deny(w http.ResponseWriter, r *http.Request, aerr *authError) {
	a.logger.Warn("entra: request denied",
		"status", aerr.status,
		"code", aerr.code,
		"detail", aerr.detail,
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
	)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	w.WriteHeader(aerr.status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": aerr.code})
}

// bearerToken extracts a token from the Authorization header. Tokens are not
// accepted in the URL (e.g. ?access_token=), where proxies and browser
// history would record them; the SPA's SSE client sends the header.
func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("authorization header missing")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("authorization header must use the Bearer scheme")
	}
	return strings.TrimSpace(h[len(prefix):]), nil
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(strings.TrimSpace(h), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

// HealthCheckTimeout is the timeout used when initialising the verifier at
// startup, exposed for callers that don't already have a context.
const HealthCheckTimeout = 15 * time.Second
