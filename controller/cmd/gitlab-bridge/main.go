// Command gitlab-bridge is a standalone service that watches GitLab for
// merge request merges and updates bead fields (mr_merged, mr_state, etc.).
//
// It runs three subsystems:
//   - Webhook handler: receives GitLab MR events via POST /webhook
//   - Polling fallback: periodic check for recently merged MRs
//   - SSE watcher: listens for bead updates with mr_url → checks MR status
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
	"strconv"
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
	logger.Info("starting gitlab-bridge",
		"version", version,
		"commit", commit,
		"beads_http", cfg.beadsHTTPAddr,
		"gitlab_base_url", cfg.gitlabBaseURL,
		"gitlab_group_id", cfg.gitlabGroupID,
		"poll_interval", cfg.pollInterval,
		"listen_addr", cfg.listenAddr)

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

	// Create GitLab client.
	gitlabClient := bridge.NewGitLabClient(bridge.GitLabClientConfig{
		BaseURL: cfg.gitlabBaseURL,
		Token:   cfg.gitlabAPIToken,
		Logger:  logger,
	})

	// HTTP server with health endpoints and webhook handler.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, version)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	// Webhook endpoint for GitLab MR events.
	mux.Handle("/webhook", bridge.GitLabWebhookHandler(
		gitlabClient, daemon, cfg.gitlabWebhookSecret, logger,
	))

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

	// Start GitLab MR poller goroutine (fallback for webhooks).
	poller := bridge.NewGitLabPoller(bridge.GitLabPollerConfig{
		GitLab:       gitlabClient,
		Daemon:       daemon,
		Logger:       logger,
		GroupID:      cfg.gitlabGroupID,
		PollInterval: cfg.pollInterval,
	})
	go func() {
		if err := poller.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("GitLab poller stopped", "error", err)
		}
	}()

	// Create event deduplicator.
	dedup := bridge.NewDedup(logger)

	// Create SSE event stream for MR status checking on bead updates.
	sseStream := bridge.NewSSEStream(bridge.SSEStreamConfig{
		BeadsHTTPAddr: cfg.beadsHTTPAddr,
		Topics:        []string{"beads.bead.updated"},
		Logger:        logger,
		Dedup:         dedup,
		State:         state,
	})

	// Register GitLab sync handler on the SSE stream.
	gitlabSync := bridge.NewGitLabSync(bridge.GitLabSyncConfig{
		GitLab: gitlabClient,
		Daemon: daemon,
		Logger: logger,
	})
	gitlabSync.RegisterHandlers(sseStream)

	// Start the SSE stream.
	go func() {
		if err := sseStream.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("SSE event stream stopped", "error", err)
		}
	}()

	logger.Info("gitlab-bridge ready",
		"group_id", cfg.gitlabGroupID,
		"poll_interval", cfg.pollInterval)

	// Block until shutdown signal.
	<-ctx.Done()
	logger.Info("shutting down gitlab-bridge")

	// Graceful HTTP server shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}
}

// config holds parsed environment configuration for the gitlab-bridge service.
type config struct {
	beadsHTTPAddr        string
	gitlabBaseURL        string
	gitlabAPIToken       string
	gitlabWebhookSecret  string
	gitlabGroupID        int
	pollInterval         time.Duration
	listenAddr           string
	logLevel             string
	statePath            string
}

func parseConfig() *config {
	pollInterval := 5 * time.Minute
	if v := os.Getenv("GITLAB_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			pollInterval = d
		}
	}

	groupID := 0
	if v := os.Getenv("GITLAB_GROUP_ID"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			groupID = id
		}
	}

	return &config{
		beadsHTTPAddr:       envOrDefault("BEADS_HTTP_ADDR", "http://localhost:8080"),
		gitlabBaseURL:       os.Getenv("GITLAB_BASE_URL"),
		gitlabAPIToken:      os.Getenv("GITLAB_API_TOKEN"),
		gitlabWebhookSecret: os.Getenv("GITLAB_WEBHOOK_SECRET"),
		gitlabGroupID:       groupID,
		pollInterval:        pollInterval,
		listenAddr:          envOrDefault("LISTEN_ADDR", ":8092"),
		logLevel:            envOrDefault("LOG_LEVEL", "info"),
		statePath:           envOrDefault("STATE_PATH", "/tmp/gitlab-bridge-state.json"),
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
