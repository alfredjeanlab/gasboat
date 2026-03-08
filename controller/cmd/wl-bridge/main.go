// Command wl-bridge is a standalone service that polls the Wasteland
// (steveyegge/wl-commons on DoltHub) for wanted items, creates task beads
// in the beads daemon, and syncs bead claims/closures back as wasteland
// claims and completions via dolt.
//
// It runs three subsystems:
//   - Wasteland poller: periodic dolt pull + wanted query → task bead creation
//   - Wasteland sync: SSE subscription for bead updates → dolt claim/completion
//   - HTTP server: health/readiness endpoints
//
// This service has ZERO K8s dependencies and can run as a lightweight
// standalone container alongside the gasboat controller.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/bridge"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	cfg := parseConfig()

	logger := setupLogger(cfg.logLevel)
	logger.Info("starting wl-bridge",
		"version", version,
		"commit", commit,
		"beads_http", cfg.beadsHTTPAddr,
		"upstream", cfg.upstream,
		"rig_handle", cfg.rigHandle,
		"poll_interval", cfg.pollInterval,
		"listen_addr", cfg.listenAddr)

	// Handle --join mode.
	if cfg.joinMode {
		if err := runJoin(cfg, logger); err != nil {
			logger.Error("join failed", "error", err)
			os.Exit(1)
		}
		return
	}

	// Load wasteland config.
	wlCfg, err := bridge.LoadWastelandConfig(cfg.dataDir)
	if err != nil {
		logger.Error("wasteland config not found — run with --join first", "error", err)
		os.Exit(1)
	}

	// Create beads daemon HTTP client.
	daemon, err := beadsapi.New(beadsapi.Config{
		HTTPAddr: cfg.beadsHTTPAddr,
		Token:    os.Getenv("BEADS_DAEMON_TOKEN"),
	})
	if err != nil {
		logger.Error("failed to create beads daemon client", "error", err)
		os.Exit(1)
	}
	defer daemon.Close()

	// Register bead types, views, and context configs with the daemon.
	if err := bridge.EnsureConfigs(context.Background(), daemon, logger); err != nil {
		logger.Warn("failed to ensure beads configs (non-fatal)", "error", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// State persistence for SSE last-event-ID.
	state, err := bridge.NewStateManager(cfg.statePath)
	if err != nil {
		logger.Error("failed to load state", "path", cfg.statePath, "error", err)
		os.Exit(1)
	}
	logger.Info("state manager loaded", "path", cfg.statePath)

	// Create dolt client.
	doltClient, err := bridge.NewExecDoltClient(wlCfg.LocalDir)
	if err != nil {
		logger.Error("dolt client init failed", "error", err)
		os.Exit(1)
	}

	// HTTP server with health endpoints.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, version)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	srv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logger.Info("starting HTTP server", "addr", cfg.listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
		}
	}()

	// Start wasteland poller goroutine.
	poller := bridge.NewWastelandPoller(doltClient, daemon, bridge.WastelandPollerConfig{
		PollInterval: cfg.pollInterval,
		RigHandle:    cfg.rigHandle,
		Logger:       logger,
	})
	go func() {
		if err := poller.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("wasteland poller stopped", "error", err)
		}
	}()

	// Create event deduplicator.
	dedup := bridge.NewDedup(logger)

	// Create SSE event stream for wasteland sync-back.
	sseStream := bridge.NewSSEStream(bridge.SSEStreamConfig{
		BeadsHTTPAddr: cfg.beadsHTTPAddr,
		Topics:        []string{"beads.bead.updated", "beads.bead.closed"},
		Token:         os.Getenv("BEADS_DAEMON_TOKEN"),
		Logger:        logger,
		Dedup:         dedup,
		State:         state,
	})

	// Register wasteland sync handler on the SSE stream.
	wlSync := bridge.NewWastelandSync(bridge.WastelandSyncConfig{
		Dolt:      doltClient,
		Logger:    logger,
		RigHandle: cfg.rigHandle,
	})
	wlSync.RegisterHandlers(sseStream)

	// Start the SSE stream.
	go func() {
		if err := sseStream.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("SSE event stream stopped", "error", err)
		}
	}()

	logger.Info("wl-bridge ready",
		"upstream", wlCfg.Upstream,
		"rig_handle", cfg.rigHandle,
		"poll_interval", cfg.pollInterval)

	// Block until shutdown signal.
	<-ctx.Done()
	logger.Info("shutting down wl-bridge")

	// Graceful HTTP server shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}
}

func runJoin(cfg *config, logger *slog.Logger) error {
	token := os.Getenv("DOLTHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("DOLTHUB_TOKEN is required for --join")
	}
	forkOrg := os.Getenv("DOLTHUB_ORG")
	if forkOrg == "" {
		return fmt.Errorf("DOLTHUB_ORG is required for --join")
	}

	_, err := bridge.JoinWasteland(context.Background(), bridge.JoinConfig{
		Upstream:    cfg.upstream,
		ForkOrg:     forkOrg,
		Token:       token,
		RigHandle:   cfg.rigHandle,
		DisplayName: cfg.displayName,
		OwnerEmail:  cfg.ownerEmail,
		DataDir:     cfg.dataDir,
		Logger:      logger,
	})
	return err
}

// config holds parsed environment configuration for the wl-bridge service.
type config struct {
	beadsHTTPAddr string
	upstream      string
	rigHandle     string
	displayName   string
	ownerEmail    string
	pollInterval  time.Duration
	dataDir       string
	listenAddr    string
	logLevel      string
	statePath     string
	joinMode      bool
}

func parseConfig() *config {
	pollInterval := 5 * time.Minute
	if v := os.Getenv("WASTELAND_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			pollInterval = d
		}
	}

	rigHandle := os.Getenv("WASTELAND_RIG_HANDLE")
	if rigHandle == "" {
		rigHandle = os.Getenv("DOLTHUB_ORG")
	}

	// Check for --join flag.
	joinMode := false
	for _, arg := range os.Args[1:] {
		if arg == "--join" {
			joinMode = true
		}
	}

	return &config{
		beadsHTTPAddr: envOrDefault("BEADS_HTTP_ADDR", "http://localhost:8080"),
		upstream:      envOrDefault("WASTELAND_UPSTREAM", "steveyegge/wl-commons"),
		rigHandle:     rigHandle,
		displayName:   os.Getenv("WASTELAND_DISPLAY_NAME"),
		ownerEmail:    os.Getenv("WASTELAND_OWNER_EMAIL"),
		pollInterval:  pollInterval,
		dataDir:       envOrDefault("WASTELAND_DATA_DIR", "/data/wasteland"),
		listenAddr:    envOrDefault("WL_LISTEN_ADDR", ":8092"),
		logLevel:      envOrDefault("LOG_LEVEL", "info"),
		statePath:     envOrDefault("STATE_PATH", "/data/wl-bridge-state.json"),
		joinMode:      joinMode,
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
