package web

import (
	"log/slog"
	"net/http"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/controller"
	"github.com/cbellee/ev-solar-charger/internal/storage"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewServer creates an HTTP handler with all routes registered.
//
// Routing layout:
//   - Public: /healthz, /.well-known/..., /auth/tesla*, the SPA bundle (/, /assets/*, /images/*).
//     The SPA must load unauthenticated so its MSAL flow can present a sign-in
//     prompt to first-time visitors.
//   - Protected by `auth`: /api/* and /events (SSE). The React app attaches an
//     ID token via Authorization: Bearer (or ?access_token= for SSE).
func NewServer(ctrl *controller.Controller, store storage.Store, hub *Hub, logger *slog.Logger, auth Authenticator, cfg config.Config, vehicle tesla.VehicleController) http.Handler {
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
	oauth := newOAuthServer(cfg, vehicle, logger)
	rootMux.HandleFunc("GET /.well-known/appspecific/com.tesla.3p.public-key.pem", oauth.handlePublicKey)
	rootMux.HandleFunc("GET /auth/tesla", oauth.handleOAuthStart)
	rootMux.HandleFunc("GET /auth/tesla/callback", oauth.handleOAuthCallback)

	// Protected API + SSE.
	rootMux.Handle("/api/", auth.Middleware(otelhttp.NewHandler(apiMux, "solar-ev-charger-api")))
	rootMux.Handle("/events", auth.Middleware(otelhttp.NewHandler(apiMux, "solar-ev-charger-events")))

	// Public SPA bundle (index.html + /assets/*). The React app gates its
	// own UI via MSAL and presents a sign-in prompt for unauthenticated users.
	rootMux.Handle("/", spaHandler())

	return rootMux
}
