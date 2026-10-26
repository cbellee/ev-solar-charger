package web

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/controller"
	"github.com/cbellee/ev-solar-charger/internal/storage"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewServer creates an HTTP handler with all routes registered.
//
// Routing layout:
//   - Public: /healthz, /.well-known/..., /auth/tesla*, the SPA bundle (/, /assets/*, /images/*).
//     The SPA must load unauthenticated so its MSAL flow can present a sign-in
//     prompt to first-time visitors.
//   - Protected by `auth`: /api/* and /events (SSE). The React app attaches an
//     ID token in the Authorization header (Bearer scheme), including for SSE.
//     Browsers that have lost MSAL's cache (iOS) instead present the session
//     cookie issued via /auth/session, which is public but self-authenticating.
func NewServer(ctrl *controller.Controller, store storage.Store, hub *Hub, logger *slog.Logger, auth Authenticator, cfg config.Config) http.Handler {
	apiMux := http.NewServeMux()

	apiMux.HandleFunc("GET /api/state", handleState(ctrl))
	apiMux.Handle("GET /events", hub)
	apiMux.HandleFunc("POST /api/control", handleControl(ctrl))
	apiMux.HandleFunc("POST /api/mode", handleMode(ctrl))
	apiMux.HandleFunc("POST /api/refresh", handleRefresh(ctrl))
	apiMux.HandleFunc("POST /api/charge-limit", handleChargeLimit(ctrl))
	apiMux.HandleFunc("GET /api/history", handleHistory(store))
	apiMux.HandleFunc("GET /api/sessions", handleSessions(store))
	apiMux.HandleFunc("GET /api/events", handleEvents(store))
	apiMux.HandleFunc("GET /api/search", handleSearch(store))
	apiMux.HandleFunc("GET /api/usage", handleAPIUsage(ctrl))
	apiMux.HandleFunc("GET /api/usage/history", handleAPIUsageHistory(store))

	rootMux := http.NewServeMux()
	rootMux.HandleFunc("GET /healthz", handleHealthz)

	// OAuth routes are always registered so the refresh token can be
	// bootstrapped via /auth/tesla even when TESLA_TEST_MODE=true.
	oauth := newOAuthServer(cfg, ctrl, logger)
	rootMux.HandleFunc("GET /auth/entra/config", handleEntraConfig(cfg))
	rootMux.HandleFunc("GET /.well-known/appspecific/com.tesla.3p.public-key.pem", oauth.handlePublicKey)
	rootMux.HandleFunc("GET /auth/tesla", oauth.handleOAuthStart)
	rootMux.HandleFunc("GET /auth/tesla/callback", oauth.handleOAuthCallback)
	if sa, ok := auth.(SessionAuthenticator); ok {
		sessionHandler := sa.SessionHandler()
		rootMux.Handle("/auth/session", sessionHandler)
		rootMux.Handle("/auth/session/login", sessionHandler)
	}

	// Protected API + SSE.
	rootMux.Handle("/api/", auth.Middleware(otelhttp.NewHandler(apiMux, "solar-ev-charger-api")))
	rootMux.Handle("/events", auth.Middleware(otelhttp.NewHandler(apiMux, "solar-ev-charger-events")))

	// Public SPA bundle (index.html + /assets/*). The React app gates its
	// own UI via MSAL and presents a sign-in prompt for unauthenticated users.
	rootMux.Handle("/", spaHandler())

	return securityHeaders(rootMux)
}

// securityHeaders sets baseline browser hardening headers on every response.
// Framing is limited to our own origin rather than denied outright because
// MSAL's silent token renewal loads the redirect URI in a hidden iframe.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy", "frame-ancestors 'self'")
		// Browsers ignore HSTS on plain-HTTP responses, so this is inert
		// unless the app is served over TLS.
		h.Set("Strict-Transport-Security", "max-age=31536000")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") || r.URL.Path == "/events" {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
