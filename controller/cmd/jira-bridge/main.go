// Command jira-bridge is a standalone service that polls JIRA for new issues,
// creates task beads in the beads daemon, and syncs bead updates (MR links,
// closures) back to JIRA as comments, remote links, and transitions.
//
// It runs three subsystems:
//   - JIRA poller: periodic JIRA search → task bead creation
//   - JIRA sync: SSE subscription for bead updates → JIRA sync-back
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
	"strings"
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
	logger.Info("starting jira-bridge",
		"version", version,
		"commit", commit,
		"beads_http", cfg.beadsHTTPAddr,
		"jira_base_url", cfg.jiraBaseURL,
		"jira_projects", cfg.jiraProjects,
		"jira_disable_transitions", cfg.jiraDisableTransitions,
		"jira_enable_claim_transition", cfg.jiraEnableClaimTransition,
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

	// Create JIRA client.
	jiraClient := bridge.NewJiraClient(bridge.JiraClientConfig{
		BaseURL:  cfg.jiraBaseURL,
		Email:    cfg.jiraEmail,
		APIToken: cfg.jiraAPIToken,
		Logger:   logger,
	})

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

	// Start JIRA poller goroutine.
	poller := bridge.NewJiraPoller(jiraClient, daemon, bridge.JiraPollerConfig{
		Projects:     cfg.jiraProjects,
		Statuses:     cfg.jiraStatuses,
		IssueTypes:   cfg.jiraIssueTypes,
		ProjectMap:   cfg.jiraProjectMap,
		PollInterval: cfg.jiraPollInterval,
		Logger:       logger,
	})
	go func() {
		if err := poller.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("JIRA poller stopped", "error", err)
		}
	}()

	// Create event deduplicator.
	dedup := bridge.NewDedup(logger)

	// Create SSE event stream for JIRA sync-back.
	sseStream := bridge.NewSSEStream(bridge.SSEStreamConfig{
		BeadsHTTPAddr: cfg.beadsHTTPAddr,
		Topics:        []string{"beads.bead.updated", "beads.bead.closed"},
		Token:         os.Getenv("BEADS_DAEMON_TOKEN"),
		Logger:        logger,
		Dedup:         dedup,
		State:         state,
	})

	// Register JIRA sync handler on the SSE stream.
	jiraSync := bridge.NewJiraSync(bridge.JiraSyncConfig{
		Jira:                  jiraClient,
		Logger:                logger,
		DisableTransitions:    cfg.jiraDisableTransitions,
		EnableClaimTransition: cfg.jiraEnableClaimTransition,
		BotAccountID:          cfg.jiraBotAccountID,
	})
	jiraSync.RegisterHandlers(sseStream)

	// Start the SSE stream.
	go func() {
		if err := sseStream.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("SSE event stream stopped", "error", err)
		}
	}()

	logger.Info("jira-bridge ready",
		"projects", cfg.jiraProjects,
		"poll_interval", cfg.jiraPollInterval)

	// Block until shutdown signal.
	<-ctx.Done()
	logger.Info("shutting down jira-bridge")

	// Graceful HTTP server shutdown.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}
}

// config holds parsed environment configuration for the jira-bridge service.
type config struct {
	beadsHTTPAddr    string
	jiraBaseURL      string
	jiraEmail        string
	jiraAPIToken     string
	jiraProjects     []string
	jiraStatuses     []string
	jiraIssueTypes   []string
	jiraProjectMap         map[string]string // JIRA prefix (upper) → boat project name
	jiraPollInterval       time.Duration
	jiraDisableTransitions    bool
	jiraEnableClaimTransition bool   // override: allow "In Progress" on claim even when disable is true
	jiraBotAccountID          string // optional: JIRA account ID for self-assignment
	listenAddr             string
	logLevel               string
	statePath              string
}

func parseConfig() *config {
	pollInterval := 60 * time.Second
	if v := os.Getenv("JIRA_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			pollInterval = d
		}
	}

	disableTransitions := os.Getenv("JIRA_DISABLE_TRANSITIONS")
	enableClaimTransition := os.Getenv("JIRA_ENABLE_CLAIM_TRANSITION")

	return &config{
		beadsHTTPAddr:          envOrDefault("BEADS_HTTP_ADDR", "http://localhost:8080"),
		jiraBaseURL:            os.Getenv("JIRA_BASE_URL"),
		jiraEmail:              os.Getenv("JIRA_EMAIL"),
		jiraAPIToken:           os.Getenv("JIRA_API_TOKEN"),
		jiraProjects:           splitCSV(envOrDefault("JIRA_PROJECTS", "PE,DEVOPS")),
		jiraStatuses:           splitCSV(envOrDefault("JIRA_STATUSES", "To Do,Ready for Development")),
		jiraIssueTypes:         splitCSV(envOrDefault("JIRA_ISSUE_TYPES", "Bug,Task,Story,Epic")),
		jiraProjectMap:         parseBoatProjects(os.Getenv("BOAT_PROJECTS")),
		jiraPollInterval:       pollInterval,
		jiraDisableTransitions:    disableTransitions == "true" || disableTransitions == "1",
		jiraEnableClaimTransition: enableClaimTransition == "true" || enableClaimTransition == "1",
		jiraBotAccountID:          os.Getenv("JIRA_BOT_ACCOUNT_ID"),
		listenAddr:             envOrDefault("JIRA_LISTEN_ADDR", ":8091"),
		logLevel:               envOrDefault("LOG_LEVEL", "info"),
		statePath:              envOrDefault("STATE_PATH", "/tmp/jira-bridge-state.json"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseBoatProjects parses the BOAT_PROJECTS env var into a JIRA prefix → boat
// project name map. The format is comma-separated entries of the form:
//
//	{project_name}={git_url}:{jira_prefix}
//
// Example: "gasboat=https://github.com/org/gasboat.git:kd,monorepo=https://gitlab.com/org/repo:PE"
// Result:  {"KD": "gasboat", "PE": "monorepo"}
func parseBoatProjects(s string) map[string]string {
	m := make(map[string]string)
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// Split on first '=' to get project name and rest (url:prefix).
		eqIdx := strings.IndexByte(entry, '=')
		if eqIdx < 0 {
			continue
		}
		projectName := entry[:eqIdx]
		rest := entry[eqIdx+1:]
		// The JIRA prefix is after the last ':' in rest.
		colonIdx := strings.LastIndexByte(rest, ':')
		if colonIdx < 0 {
			continue
		}
		jiraPrefix := strings.ToUpper(strings.TrimSpace(rest[colonIdx+1:]))
		if jiraPrefix != "" && projectName != "" {
			m[jiraPrefix] = projectName
		}
	}
	return m
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
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
