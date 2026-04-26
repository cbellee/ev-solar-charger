package web

import (
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/cbellee/solar-ev-charger/internal/config"
	"github.com/cbellee/solar-ev-charger/internal/controller"
	"github.com/cbellee/solar-ev-charger/internal/storage"
	"github.com/cbellee/solar-ev-charger/internal/tesla"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// AuthConfig configures HTTP basic authentication for the UI and API.
type AuthConfig struct {
	Username string
	Password string
}

// NewServer creates an HTTP handler with all routes registered.
func NewServer(ctrl *controller.Controller, store storage.Store, hub *Hub, logger *slog.Logger, auth AuthConfig, cfg config.Config, vehicle tesla.VehicleController) http.Handler {
	privateMux := http.NewServeMux()

	privateMux.HandleFunc("GET /", handleIndex)
	privateMux.Handle("GET /images/", http.FileServer(http.FS(imageFS)))
	privateMux.HandleFunc("GET /api/state", handleState(ctrl))
	privateMux.Handle("GET /events", hub)
	privateMux.HandleFunc("POST /api/control", handleControl(ctrl))
	privateMux.HandleFunc("POST /api/mode", handleMode(ctrl))
	privateMux.HandleFunc("GET /api/history", handleHistory(store))
	privateMux.HandleFunc("GET /api/sessions", handleSessions(store))
	privateMux.HandleFunc("GET /api/events", handleEvents(store))
	privateMux.HandleFunc("GET /api/search", handleSearch(store))
	privateMux.HandleFunc("GET /api/usage", handleAPIUsage(ctrl))
	privateMux.HandleFunc("GET /api/usage/history", handleAPIUsageHistory(store))

	rootMux := http.NewServeMux()
	rootMux.HandleFunc("GET /healthz", handleHealthz)

	if !cfg.TeslaTestMode {
		oauth := newOAuthServer(cfg, vehicle, logger)
		rootMux.HandleFunc("GET /.well-known/appspecific/com.tesla.3p.public-key.pem", oauth.handlePublicKey)
		rootMux.HandleFunc("GET /auth/tesla", oauth.handleOAuthStart)
		rootMux.HandleFunc("GET /auth/tesla/callback", oauth.handleOAuthCallback)
	}

	rootMux.Handle("/", basicAuthMiddleware(otelhttp.NewHandler(privateMux, "solar-ev-charger"), auth))

	return rootMux
}

func basicAuthMiddleware(next http.Handler, auth AuthConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(username), []byte(auth.Username)) != 1 || subtle.ConstantTimeCompare([]byte(password), []byte(auth.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="solar-ev-charger"`)
			http.Error(w, "authorization required", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
