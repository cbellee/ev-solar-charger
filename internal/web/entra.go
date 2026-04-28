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
	// Logger receives audit-style messages for denied / accepted requests.
	// Optional.
	Logger *slog.Logger
}

// EntraAuthenticator validates Microsoft Entra ID tokens on incoming requests.
type EntraAuthenticator struct {
	cfg      EntraConfig
	verifier *oidc.IDTokenVerifier
	logger   *slog.Logger
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

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &EntraAuthenticator{cfg: cfg, verifier: verifier, logger: logger}, nil
}

// entraClaims is the subset of ID-token claims the middleware inspects after
// signature/audience/issuer/expiry checks pass.
type entraClaims struct {
	OID    string `json:"oid"`
	TID    string `json:"tid"`
	UPN    string `json:"upn"`
	PrefUN string `json:"preferred_username"`
}

// Middleware enforces a valid bearer ID token on every request reaching `next`.
// Failures return 401 with a small JSON error body. Successful requests have
// the claims attached to the request context (currently unused by handlers but
// available for future audit logging).
func (a *EntraAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := bearerOrQueryToken(r)
		if err != nil {
			a.deny(w, r, http.StatusUnauthorized, "missing_token", err.Error())
			return
		}

		// Verify signature, issuer, audience, expiry.
		idToken, err := a.verifier.Verify(r.Context(), token)
		if err != nil {
			a.deny(w, r, http.StatusUnauthorized, "invalid_token", err.Error())
			return
		}

		var claims entraClaims
		if err := idToken.Claims(&claims); err != nil {
			a.deny(w, r, http.StatusUnauthorized, "claims_decode_failed", err.Error())
			return
		}

		// Tenant pinning: reject tokens minted for a different tenant even
		// if the audience matches (defence-in-depth against multi-tenant
		// app misconfiguration).
		if !strings.EqualFold(claims.TID, a.cfg.TenantID) {
			a.deny(w, r, http.StatusForbidden, "wrong_tenant", "tid does not match configured tenant")
			return
		}

		if len(a.cfg.AllowedOIDs) > 0 && !containsFold(a.cfg.AllowedOIDs, claims.OID) {
			a.deny(w, r, http.StatusForbidden, "user_not_allowed", "oid not in allowlist")
			return
		}

		// Attach claims to context for downstream handlers.
		ctx := context.WithValue(r.Context(), entraClaimsKey{}, &claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type entraClaimsKey struct{}

// ClaimsFromContext returns the verified Entra claims for the current request,
// or nil if the request bypassed authentication.
func ClaimsFromContext(ctx context.Context) *entraClaims {
	v, _ := ctx.Value(entraClaimsKey{}).(*entraClaims)
	return v
}

func (a *EntraAuthenticator) deny(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	a.logger.Warn("entra: request denied",
		"status", status,
		"code", code,
		"detail", detail,
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
	)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// bearerOrQueryToken extracts a token from the Authorization header or, as a
// fallback for EventSource which cannot set headers, from the `access_token`
// query parameter. The query-param fallback is acceptable here because:
//   - traffic is HTTPS-terminated by nginx in front of the app,
//   - tokens are short-lived (Entra defaults to 1 hour),
//   - the SPA is the only client and it strips the token from history before
//     opening the SSE stream.
func bearerOrQueryToken(r *http.Request) (string, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) {
			return "", errors.New("authorization header must use Bearer scheme")
		}
		return strings.TrimSpace(h[len(prefix):]), nil
	}
	if q := r.URL.Query().Get("access_token"); q != "" {
		return q, nil
	}
	return "", errors.New("authorization header missing")
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
