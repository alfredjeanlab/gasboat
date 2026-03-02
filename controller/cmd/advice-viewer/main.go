// Command advice-viewer is a web UI for viewing and managing advice beads.
// It provides per-agent advice views, advice CRUD, and generation dispatch.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gasboat/controller/internal/beadsapi"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	cfg := parseConfig()

	logger := setupLogger(cfg.logLevel)
	logger.Info("starting advice-viewer",
		"version", version,
		"commit", commit,
		"beads_http", cfg.beadsHTTPAddr,
		"listen_addr", cfg.listenAddr)

	daemon, err := beadsapi.New(beadsapi.Config{
		HTTPAddr: cfg.beadsHTTPAddr,
		Token:    os.Getenv("BEADS_DAEMON_TOKEN"),
	})
	if err != nil {
		logger.Error("failed to create beads daemon client", "error", err)
		os.Exit(1)
	}
	defer daemon.Close()

	srv := NewServer(daemon, logger, cfg.basePath)

	mux := http.NewServeMux()

	// Health probes bypass auth.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, version)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	// Application routes (protected by middleware).
	appMux := http.NewServeMux()
	srv.RegisterRoutes(appMux)

	// Build middleware chain: ipWhitelist -> basicAuth -> handler.
	var handler http.Handler = appMux
	handler = basicAuthMiddleware(handler, cfg.authUsername, cfg.authPassword)
	handler = ipWhitelistMiddleware(handler, cfg.allowedCIDRs, logger)

	mux.Handle("/", handler)

	httpSrv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	go func() {
		logger.Info("starting HTTP server", "addr", cfg.listenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down advice-viewer")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}
}

type config struct {
	beadsHTTPAddr string
	listenAddr    string
	logLevel      string
	authUsername  string
	authPassword  string
	allowedCIDRs  string
	basePath      string
}

func parseConfig() *config {
	bp := strings.TrimRight(os.Getenv("BASE_PATH"), "/")
	return &config{
		beadsHTTPAddr: envOrDefault("BEADS_HTTP_ADDR", "http://localhost:8080"),
		listenAddr:    envOrDefault("LISTEN_ADDR", ":8091"),
		logLevel:      envOrDefault("LOG_LEVEL", "info"),
		authUsername:  os.Getenv("AUTH_USERNAME"),
		authPassword:  os.Getenv("AUTH_PASSWORD"),
		allowedCIDRs:  os.Getenv("ALLOWED_CIDRS"),
		basePath:      bp,
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func setupLogger(level string) *slog.Logger {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
}

func init() {
	if v := os.Getenv("VERSION"); v != "" {
		version = v
	}
}
