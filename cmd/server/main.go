package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cbellee/ev-solar-charger/internal/config"
	"github.com/cbellee/ev-solar-charger/internal/controller"
	"github.com/cbellee/ev-solar-charger/internal/inverter"
	"github.com/cbellee/ev-solar-charger/internal/observability"
	"github.com/cbellee/ev-solar-charger/internal/storage"
	"github.com/cbellee/ev-solar-charger/internal/tesla"
	"github.com/cbellee/ev-solar-charger/internal/web"
	"golang.org/x/crypto/acme/autocert"
)

type runtimeDeps struct {
	loadConfig   func() (config.Config, error)
	setupOTelSDK func(context.Context, string, string) (func(context.Context) error, error)
	newLogger    func(string, slog.Level) *slog.Logger
	newMetrics   func() (*observability.Metrics, error)
	newStore     func(string, *slog.Logger) (storage.Store, error)
	newInverter  func(string, int, *slog.Logger, *observability.Metrics) inverter.InverterReader
	newVehicle   func(config.Config, *slog.Logger, *observability.Metrics) (tesla.VehicleController, error)
	newServer    func(*controller.Controller, storage.Store, *web.Hub, *slog.Logger, web.AuthConfig, config.Config, tesla.VehicleController) http.Handler
	newHub       func(*slog.Logger) *web.Hub
}

func defaultRuntimeDeps() runtimeDeps {
	return runtimeDeps{
		loadConfig:   config.Load,
		setupOTelSDK: observability.SetupOTelSDK,
		newLogger:    observability.NewLogger,
		newMetrics:   observability.NewMetrics,
		newStore: func(dbPath string, logger *slog.Logger) (storage.Store, error) {
			return storage.NewSQLiteStore(dbPath, logger)
		},
		newInverter: func(host string, port int, logger *slog.Logger, metrics *observability.Metrics) inverter.InverterReader {
			return inverter.New(host, port, logger, metrics)
		},
		newVehicle: func(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) (tesla.VehicleController, error) {
			return tesla.New(cfg, logger, metrics)
		},
		newServer: web.NewServer,
		newHub:    web.NewHub,
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func runHealthcheck() error {
	port, err := healthcheckPort()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return checkHealth(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
}

func healthcheckPort() (int, error) {
	portValue := os.Getenv("HTTP_PORT")
	if portValue == "" {
		return 8080, nil
	}

	port, err := strconv.Atoi(portValue)
	if err != nil {
		return 0, fmt.Errorf("invalid HTTP_PORT for healthcheck: %w", err)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid HTTP_PORT for healthcheck: %d", port)
	}

	return port, nil
}

func checkHealth(ctx context.Context, healthURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned status %d", resp.StatusCode)
	}

	return nil
}

func run() error {
	return runWithContext(context.Background(), defaultRuntimeDeps())
}

func runWithContext(parent context.Context, deps runtimeDeps) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// 1. Load configuration.
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// 2. Set up OpenTelemetry.
	shutdown, err := deps.setupOTelSDK(ctx, "solar-ev-charger", "0.1.0")
	if err != nil {
		return fmt.Errorf("failed to setup otel: %w", err)
	}
	defer func() { _ = shutdown(ctx) }()

	// 3. Create logger.
	logger := deps.newLogger("solar-ev-charger", cfg.LogLevel)

	// 4. Create metrics.
	metrics, err := deps.newMetrics()
	if err != nil {
		return fmt.Errorf("failed to create metrics: %w", err)
	}

	// 5. Open storage.
	store, err := deps.newStore(cfg.DBPath, logger)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}
	defer store.Close()

	// 6. Create inverter client.
	inv := deps.newInverter(cfg.SungrowHost, cfg.SungrowPort, logger, metrics)
	if err := inv.Connect(ctx); err != nil {
		logger.Warn("inverter connect failed (will retry)", "error", err)
	}

	// 7. Create Tesla client.
	var vehicle tesla.VehicleController
	if cfg.TeslaTestMode {
		logger.Info("tesla test mode enabled", "message", "vehicle commands disabled; publishing projected surplus only")
		vehicle = tesla.NewTestModeController()
	} else {
		if strings.TrimSpace(cfg.TeslaRefreshToken) == "" {
			if tokenBytes, readErr := os.ReadFile(cfg.TeslaTokenPath); readErr == nil {
				cfg.TeslaRefreshToken = strings.TrimSpace(string(tokenBytes))
			}
		}
		if strings.TrimSpace(cfg.TeslaRefreshToken) == "" {
			return fmt.Errorf("failed to create tesla client: TESLA_REFRESH_TOKEN is empty and token file %q was not usable", cfg.TeslaTokenPath)
		}

		vehicle, err = deps.newVehicle(cfg, logger, metrics)
		if err != nil {
			return fmt.Errorf("failed to create tesla client: %w", err)
		}
	}

	// 8. Create controller.
	ctrl := controller.New(inv, vehicle, store, cfg, logger, metrics)

	// 9. Create SSE hub and wire up controller notifications.
	hub := deps.newHub(logger)
	ctrl.OnUpdate = hub.Broadcast

	// 10. Create web server.
	handler := deps.newServer(ctrl, store, hub, logger, web.AuthConfig{
		Username: cfg.HTTPAuthUser,
		Password: cfg.HTTPAuthPassword,
	}, cfg, vehicle)

	// 11. Start controller loop.
	go ctrl.Run(ctx)

	// 12. Start daily prune goroutine.
	go func() {
		retention := time.Duration(cfg.DBRetentionDays) * 24 * time.Hour
		deleted, err := store.Prune(ctx, retention)
		if err != nil {
			logger.Error("initial prune failed", "error", err)
		} else if deleted > 0 {
			logger.Info("pruned old records", "deleted", deleted)
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				deleted, err := store.Prune(ctx, retention)
				if err != nil {
					logger.Error("prune failed", "error", err)
				} else if deleted > 0 {
					logger.Info("pruned old records", "deleted", deleted)
				}
			}
		}
	}()

	// 13. Start primary HTTP server.
	primaryHTTPServer := &http.Server{
		Addr:    net.JoinHostPort(cfg.HTTPHost, strconv.Itoa(cfg.HTTPPort)),
		Handler: handler,
	}
	servers := []*http.Server{primaryHTTPServer}
	startFns := []func() error{func() error { return primaryHTTPServer.ListenAndServe() }}

	if cfg.TLSEnabled {
		certManager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.TLSDomain),
			Cache:      autocert.DirCache(cfg.TLSCertDir),
		}

		tlsServer := &http.Server{
			Addr:    net.JoinHostPort(cfg.HTTPHost, strconv.Itoa(cfg.TLSPort)),
			Handler: handler,
			TLSConfig: &tls.Config{
				GetCertificate: certManager.GetCertificate,
				MinVersion:     tls.VersionTLS13,
			},
		}

		challengeAndRedirectHandler := certManager.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpsURL := "https://" + cfg.TLSDomain + r.URL.RequestURI()
			http.Redirect(w, r, httpsURL, http.StatusMovedPermanently)
		}))

		challengeServer := &http.Server{
			Addr:    net.JoinHostPort(cfg.HTTPHost, strconv.Itoa(cfg.HTTPChallengePort)),
			Handler: challengeAndRedirectHandler,
		}

		servers = append(servers, tlsServer, challengeServer)
		startFns = append(startFns,
			func() error { return tlsServer.ListenAndServeTLS("", "") },
			func() error { return challengeServer.ListenAndServe() },
		)
	}

	// 14. Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	serverErrCh := make(chan error, 1)

	for i, startFn := range startFns {
		srv := servers[i]
		go func(s *http.Server, fn func() error) {
			logger.Info("server starting", "addr", s.Addr)
			if err := fn(); err != nil && err != http.ErrServerClosed {
				serverErrCh <- err
			}
		}(srv, startFn)
	}

	select {
	case err := <-serverErrCh:
		cancel()
		return fmt.Errorf("http server failed: %w", err)
	case sig := <-sigCh:
		logger.Info("shutting down", "signal", sig.String())
		cancel()
	case <-ctx.Done():
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("shutdown server %s: %w", srv.Addr, err)
		}
	}

	return nil
}
