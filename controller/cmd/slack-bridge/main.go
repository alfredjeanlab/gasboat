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

	"strings"

	slackapi "github.com/slack-go/slack"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

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
		"threading_mode", cfg.threadingMode,
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

	// Unreleased changes API — same data as the /unreleased Slack command.
	// Build image tracking configs from the gasboat repo using the bridge's
	// own version as the deployed tag (all gasboat images share the calver tag).
	imageConfigs := buildImageConfigs(cfg.repos, version)
	mux.HandleFunc("/api/unreleased", bridge.HandleUnreleased(bridge.UnreleasedConfig{
		GitHub:        bridge.NewGitHubClientIfConfigured(cfg.githubToken, cfg.repos, logger),
		Repos:         cfg.repos,
		ControllerURL: cfg.controllerURL,
		Version:       version,
		Images:        imageConfigs,
	}))

	// Decisions web UI and API.
	decisionAPI := bridge.NewDecisionAPI(daemon, logger)
	decisionAPI.RegisterRoutes(mux)
	mux.Handle("/api/decisions/events", bridge.NewDecisionSSEProxy(cfg.beadsHTTPAddr, os.Getenv("BEADS_DAEMON_TOKEN"), logger))
	mux.Handle("/ui/", http.StripPrefix("/ui/", bridge.WebHandler()))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// Initialize Bouncer for IP whitelist management if configured.
	var bouncer *bridge.Bouncer
	if cfg.bouncerEnabled && cfg.bouncerNamespace != "" && len(cfg.bouncerMiddlewares) > 0 {
		k8sCfg, err := rest.InClusterConfig()
		if err != nil {
			logger.Warn("bouncer: failed to get in-cluster config (gate disabled)", "error", err)
		} else {
			dynClient, err := dynamic.NewForConfig(k8sCfg)
			if err != nil {
				logger.Warn("bouncer: failed to create K8s dynamic client (gate disabled)", "error", err)
			} else {
				bouncer = bridge.NewBouncer(bridge.BouncerConfig{
					Client:          dynClient,
					Namespace:       cfg.bouncerNamespace,
					MiddlewareNames: cfg.bouncerMiddlewares,
					Logger:          logger,
				})
				logger.Info("bouncer: IP whitelist management enabled",
					"namespace", cfg.bouncerNamespace,
					"middlewares", cfg.bouncerMiddlewares)
			}
		}
	}

	if cfg.slackBotToken != "" && cfg.slackAppToken != "" {
		// Socket Mode: real-time WebSocket connection for events, interactions, slash commands.
		bot = bridge.NewBot(bridge.BotConfig{
			BotToken:         cfg.slackBotToken,
			AppToken:         cfg.slackAppToken,
			Channel:          cfg.slackChannel,
			ThreadingMode:    cfg.threadingMode,
			Daemon:           daemon,
			State:            state,
			Bouncer:          bouncer,
			Logger:           logger,
			Debug:            cfg.debug,
			GitHubToken:      cfg.githubToken,
			Repos:            cfg.repos,
			Version:          version,
			ControllerURL:    cfg.controllerURL,
			CoopmuxPublicURL: cfg.coopmuxPublicURL,
			ImageConfigs:     imageConfigs,
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

	// Register Slack thread API (read/reply endpoints for agents).
	if bot != nil {
		threadAPI := bridge.NewSlackThreadAPI(bot.API(), logger)
		threadAPI.RegisterRoutes(mux)
	} else if cfg.slackBotToken != "" {
		// Webhook mode — create a standalone Slack client for thread API.
		threadAPI := bridge.NewSlackThreadAPI(slackapi.New(cfg.slackBotToken), logger)
		threadAPI.RegisterRoutes(mux)
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

	// Start Socket Mode bot if configured (auto-reconnect with backoff).
	if bot != nil {
		go func() {
			backoff := time.Second
			const maxBackoff = 30 * time.Second
			for {
				if err := bot.Run(ctx); err != nil && ctx.Err() == nil {
					logger.Error("Socket Mode bot stopped, reconnecting", "error", err, "backoff", backoff)
				}
				if ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, maxBackoff)
			}
		}()
	}

	// Create event deduplicator for preventing duplicate Slack notifications.
	dedup := bridge.NewDedup(logger)

	// Create SSE event stream for decisions, mail, agents, and jacks watchers.
	sseStream := bridge.NewSSEStream(bridge.SSEStreamConfig{
		BeadsHTTPAddr: cfg.beadsHTTPAddr,
		Topics:        []string{"beads.bead.created", "beads.bead.closed", "beads.bead.updated"},
		Token:         os.Getenv("BEADS_DAEMON_TOKEN"),
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

	// Register chat forwarding handler (Slack→agent→Slack relay).
	if bot != nil {
		chat := bridge.NewChat(bridge.ChatConfig{
			Daemon: daemon,
			Bot:    bot,
			State:  state,
			Logger: logger,
		})
		chat.RegisterHandlers(sseStream)
	}

	// Register squawk message handler (agent→Slack informational messages).
	if bot != nil {
		squawk := bridge.NewSquawk(bridge.SquawkConfig{
			Daemon: daemon,
			Bot:    bot,
			Logger: logger,
		})
		squawk.RegisterHandlers(sseStream)
	}

	// Start live agent dashboard if configured.
	if cfg.dashboardEnabled && bot != nil {
		dashChannel := cfg.dashboardChannel
		if dashChannel == "" {
			dashChannel = cfg.slackChannel
		}
		dash := bridge.NewDashboard(bot.API(), daemon, state, logger, bridge.DashboardConfig{
			Enabled:          true,
			ChannelID:        dashChannel,
			Interval:         cfg.dashboardInterval,
			CoopmuxPublicURL: cfg.coopmuxPublicURL,
		})
		dash.RegisterHandlers(sseStream)
		go dash.Run(ctx)
		logger.Info("dashboard enabled", "channel", dashChannel, "interval", cfg.dashboardInterval)
	}

	// Register claimed bead update watcher — nudges agents when their claimed work is updated.
	claimed := bridge.NewClaimed(bridge.ClaimedConfig{
		Daemon: daemon,
		Logger: logger,
	})
	claimed.RegisterHandlers(sseStream)

	// Register bead activity watcher — posts notifications in agent threads when
	// agents create, claim, or close beads.
	var beadActivityNotifier bridge.BeadActivityNotifier
	if bot != nil {
		beadActivityNotifier = bot
	}
	beadActivity := bridge.NewBeadActivity(bridge.BeadActivityConfig{
		Notifier: beadActivityNotifier,
		Logger:   logger,
	})
	beadActivity.RegisterHandlers(sseStream)

	// Start periodic dedup map cleanup to prevent unbounded growth.
	go dedup.StartCleanup(ctx)

	// Catch-up: notify pending decisions that may have been missed during downtime.
	// Run before SSE stream starts to pre-populate dedup map.
	go dedup.CatchUpDecisions(ctx, daemon, notifier, logger)

	// Catch-up: pre-populate dedup for active agent beads so SSE replay
	// doesn't re-fire created events (prevents state flicker on restart).
	dedup.CatchUpAgents(ctx, daemon, logger)

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

	// Threading
	threadingMode string

	// Dashboard
	dashboardEnabled  bool
	dashboardChannel  string
	dashboardInterval time.Duration

	// GitHub /unreleased
	githubToken   string
	repos         []bridge.RepoRef
	controllerURL string

	// Coopmux terminal links
	coopmuxPublicURL string

	// Gate (IP whitelist management)
	bouncerEnabled     bool
	bouncerNamespace   string
	bouncerMiddlewares []string
}

func parseConfig() *config {
	dashInterval := 15 * time.Second
	if v := os.Getenv("SLACK_DASHBOARD_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			dashInterval = d
		}
	}

	dashChannel := os.Getenv("SLACK_DASHBOARD_CHANNEL")
	dashEnabled := os.Getenv("SLACK_DASHBOARD") == "true"
	// Auto-enable if channel is set.
	if dashChannel != "" && os.Getenv("SLACK_DASHBOARD") == "" {
		dashEnabled = true
	}

	threadingMode := os.Getenv("SLACK_THREADING_MODE")
	if threadingMode == "" {
		threadingMode = "agent"
	}

	repos := parseRepoList(envOrDefault("UNRELEASED_REPOS", "groblegark/gasboat,groblegark/kbeads,groblegark/coop"))

	return &config{
		beadsHTTPAddr:      envOrDefault("BEADS_HTTP_ADDR", "http://localhost:8080"),
		slackBotToken:      os.Getenv("SLACK_BOT_TOKEN"),
		slackAppToken:      os.Getenv("SLACK_APP_TOKEN"),
		slackSigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
		slackChannel:       os.Getenv("SLACK_CHANNEL"),
		listenAddr:         envOrDefault("SLACK_LISTEN_ADDR", ":8090"),
		logLevel:           envOrDefault("LOG_LEVEL", "info"),
		statePath:          envOrDefault("STATE_PATH", "/data/slack-bridge-state.json"),
		debug:              os.Getenv("DEBUG") == "true",

		threadingMode: threadingMode,

		dashboardEnabled:  dashEnabled,
		dashboardChannel:  dashChannel,
		dashboardInterval: dashInterval,

		githubToken:   os.Getenv("GITHUB_TOKEN"),
		repos:         repos,
		controllerURL: os.Getenv("CONTROLLER_URL"),

		coopmuxPublicURL: os.Getenv("COOPMUX_PUBLIC_URL"),

		bouncerEnabled:     os.Getenv("BOUNCER_ENABLED") == "true",
		bouncerNamespace:   envOrDefault("BOUNCER_NAMESPACE", os.Getenv("POD_NAMESPACE")),
		bouncerMiddlewares: parseCommaSeparated(os.Getenv("BOUNCER_MIDDLEWARES")),
	}
}

// parseRepoList parses a comma-separated list of "owner/repo" strings.
// A repo suffixed with "~ext" is marked as an external dependency
// (deployed via rolling tag, not its own release tags).
func parseRepoList(s string) []bridge.RepoRef {
	var repos []bridge.RepoRef
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		external := false
		if after, found := strings.CutSuffix(entry, "~ext"); found {
			entry = after
			external = true
		}
		parts := strings.SplitN(entry, "/", 2)
		if len(parts) != 2 {
			continue
		}
		repos = append(repos, bridge.RepoRef{Owner: parts[0], Repo: parts[1], External: external})
	}
	return repos
}

func parseCommaSeparated(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			result = append(result, v)
		}
	}
	return result
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

// buildImageConfigs creates image tracking configs for gasboat images.
// It finds the gasboat repo in the repos list and uses the given version
// as the deployed tag for all images (they share the same calver tag).
func buildImageConfigs(repos []bridge.RepoRef, deployedVersion string) []bridge.ImageTrackConfig {
	// Find the gasboat repo in the configured list.
	for _, r := range repos {
		if r.Repo == "gasboat" {
			return bridge.DefaultGasboatImageConfigs(r, deployedVersion, deployedVersion, deployedVersion)
		}
	}
	return nil
}

