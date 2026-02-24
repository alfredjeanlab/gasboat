// Command slack-bridge is a standalone service that bridges beads lifecycle
// events to Slack notifications and handles Slack interaction webhooks.
//
// It runs three subsystems:
//   - Decisions watcher: SSE subscription for decision beads → Slack notifications
//   - Mail watcher: SSE subscription for mail beads → agent nudges
//   - HTTP server: Slack interaction webhook handler (/slack/interactions)
//
// This service has ZERO K8s dependencies and can run as a lightweight sidecar
// or standalone container alongside the gasboat controller.
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
	logger.Info("starting slack-bridge",
		"version", version,
		"commit", commit,
		"beads_http", cfg.beadsHTTPAddr,
		"slack_channel", cfg.slackChannel,
		"listen_addr", cfg.listenAddr)

	// Create beads daemon HTTP client.
	daemon, err := beadsapi.New(beadsapi.Config{HTTPAddr: cfg.beadsHTTPAddr})
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

	// State persistence for Slack message tracking.
	state, err := bridge.NewStateManager(cfg.statePath)
	if err != nil {
		logger.Error("failed to load state", "path", cfg.statePath, "error", err)
		os.Exit(1)
	}
	logger.Info("state manager loaded", "path", cfg.statePath)

	// Slack notifier (optional — decisions still tracked even without Slack).
	var notifier bridge.Notifier
	var bot *bridge.Bot
	mux := http.NewServeMux()

	// Health endpoints — always available regardless of Slack config.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, version)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if bot != nil && !bot.IsConnected() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"not_ready","reason":"socket_mode_disconnected"}`)
			return
		}
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	if cfg.slackBotToken != "" && cfg.slackAppToken != "" {
		// Socket Mode: real-time WebSocket connection for events, interactions, slash commands.
		bot = bridge.NewBot(bridge.BotConfig{
			BotToken: cfg.slackBotToken,
			AppToken: cfg.slackAppToken,
			Channel:  cfg.slackChannel,
			Daemon:   daemon,
			State:    state,
			Logger:   logger,
			Debug:    cfg.debug,
		})
		notifier = bot
		logger.Info("Slack Socket Mode bot enabled", "channel", cfg.slackChannel)
	} else if cfg.slackBotToken != "" {
		// Webhook fallback: raw HTTP Slack notifier with interaction webhook handler.
		slack := bridge.NewSlackNotifier(
			cfg.slackBotToken,
			cfg.slackSigningSecret,
			cfg.slackChannel,
			daemon,
			logger,
		)
		notifier = slack
		mux.HandleFunc("/slack/interactions", slack.HandleInteraction)
		logger.Info("Slack webhook notifier enabled", "channel", cfg.slackChannel)
	} else {
		logger.Warn("SLACK_BOT_TOKEN not set — running without Slack notifications")
	}

	// Start HTTP server (always — serves health endpoints + optional webhook handler).
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

	// Start Socket Mode bot if configured.
	if bot != nil {
		go func() {
			if err := bot.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Error("Socket Mode bot stopped", "error", err)
			}
		}()
	}

	// Create event deduplicator for preventing duplicate Slack notifications.
	dedup := bridge.NewDedup(logger)

	// Create SSE event stream for decisions, mail, agents, and jacks watchers.
	sseStream := bridge.NewSSEStream(bridge.SSEStreamConfig{
		BeadsHTTPAddr: cfg.beadsHTTPAddr,
		Topics:        []string{"beads.bead.created", "beads.bead.closed", "beads.bead.updated"},
		Logger:        logger,
		Dedup:         dedup,
		State:         state,
	})

	// Register decisions handler on the SSE stream.
	decisions := bridge.NewDecisions(bridge.DecisionsConfig{
		Daemon:   daemon,
		Notifier: notifier,
		Logger:   logger,
	})
	decisions.RegisterHandlers(sseStream)

	// Register mail handler on the SSE stream.
	mail := bridge.NewMail(bridge.MailConfig{
		Daemon: daemon,
		Logger: logger,
	})
	mail.RegisterHandlers(sseStream)

	// Register agents watcher for crash notifications.
	var agentNotifier bridge.AgentNotifier
	if bot != nil {
		agentNotifier = bot
	}
	agents := bridge.NewAgents(bridge.AgentsConfig{
		Notifier: agentNotifier,
		Logger:   logger,
	})
	agents.RegisterHandlers(sseStream)

	// Register jacks watcher for jack lifecycle notifications.
	var jackNotifier bridge.JackNotifier
	if bot != nil {
		jackNotifier = bot
	}
	jacks := bridge.NewJacks(bridge.JacksConfig{
		Notifier: jackNotifier,
		Logger:   logger,
	})
	jacks.RegisterHandlers(sseStream)

	// Catch-up: notify pending decisions that may have been missed during downtime.
	// Run before SSE stream starts to pre-populate dedup map.
	go dedup.CatchUpDecisions(ctx, daemon, notifier, logger)

	// Start the shared SSE stream (delivers events to all watchers).
	go func() {
		if err := sseStream.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("SSE event stream stopped", "error", err)
		}
	}()

	logger.Info("slack-bridge ready",
		"socket_mode", bot != nil,
		"webhook_mode", bot == nil && notifier != nil)

	// Block until shutdown signal.
	<-ctx.Done()
	logger.Info("shutting down slack-bridge")

	// Graceful HTTP server shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}
}

// config holds parsed environment configuration for the slack-bridge service.
type config struct {
	beadsHTTPAddr      string
	slackBotToken      string
	slackAppToken      string
	slackSigningSecret string
	slackChannel       string
	listenAddr         string
	logLevel           string
	statePath          string
	debug              bool
}

func parseConfig() *config {
	return &config{
		beadsHTTPAddr:      envOrDefault("BEADS_HTTP_ADDR", "http://localhost:8080"),
		slackBotToken:      os.Getenv("SLACK_BOT_TOKEN"),
		slackAppToken:      os.Getenv("SLACK_APP_TOKEN"),
		slackSigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
		slackChannel:       os.Getenv("SLACK_CHANNEL"),
		listenAddr:         envOrDefault("SLACK_LISTEN_ADDR", ":8090"),
		logLevel:           envOrDefault("LOG_LEVEL", "info"),
		statePath:          envOrDefault("STATE_PATH", "/tmp/slack-bridge-state.json"),
		debug:              os.Getenv("DEBUG") == "true" || os.Getenv("LOG_LEVEL") == "debug",
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

