package main

// agent_start_k8s.go — gb agent start --k8s
//
// Replaces entrypoint.sh as the PID-1 process in the K8s agent pod.
// Handles all workspace setup, credential provisioning, coop startup,
// background goroutines, signal forwarding, and the restart loop.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// k8sConfig holds all configuration for the K8s agent start command,
// resolved from flags and environment variables at startup.
type k8sConfig struct {
	workspace      string
	coopPort       int
	coopHealthPort int
	maxRestarts    int
	command        string
	sessionResume  bool

	// from env
	role     string
	project  string
	agent    string
	podIP    string
	hostname string
}

func runAgentStartK8s(cmd *cobra.Command, args []string) error {
	workspace, _ := cmd.Flags().GetString("workspace")
	coopPort, _ := cmd.Flags().GetInt("coop-port")
	coopHealthPort, _ := cmd.Flags().GetInt("coop-health-port")
	maxRestarts, _ := cmd.Flags().GetInt("max-restarts")
	if maxRestarts == 0 {
		maxRestarts = intEnvOr("COOP_MAX_RESTARTS", 10)
	}

	hostname, _ := os.Hostname()
	podIP := os.Getenv("POD_IP")
	if podIP == "" {
		podIP = "localhost"
	}

	cfg := k8sConfig{
		workspace:      workspace,
		coopPort:       coopPort,
		coopHealthPort: coopHealthPort,
		maxRestarts:    maxRestarts,
		command:        envOr("BOAT_COMMAND", "claude --dangerously-skip-permissions"),
		sessionResume:  envOr("BOAT_SESSION_RESUME", "1") == "1",
		role:           envOr("BOAT_ROLE", "unknown"),
		project:        os.Getenv("BOAT_PROJECT"),
		agent:          envOr("BOAT_AGENT", "unknown"),
		podIP:          podIP,
		hostname:       hostname,
	}

	fmt.Printf("[gb agent start] starting %s agent (mode: k8s): %s (project: %s)\n",
		cfg.role, cfg.agent, orStr(cfg.project, "none"))

	// ── One-time setup (idempotent on restart) ──────────────────────────

	if err := setupWorkspace(cfg); err != nil {
		return fmt.Errorf("workspace setup: %w", err)
	}

	claudeStateDir, err := setupPVC(cfg)
	if err != nil {
		return fmt.Errorf("PVC setup: %w", err)
	}

	stateDir := cfg.workspace + "/.state"
	coopStateDir := stateDir + "/coop"

	credMode := provisionCredentials(claudeStateDir)

	// Materialize user settings + workspace hooks from config beads.
	// runSetupClaude writes user-level settings (from beads or defaults)
	// and workspace hooks to workspace/.claude/settings.json.
	if err := runSetupClaude(context.Background(), workspace, cfg.role); err != nil {
		fmt.Printf("[gb agent start] config beads not found, installing default hooks\n")
		if err2 := runSetupClaudeDefaults(workspace, cfg.role); err2 != nil {
			fmt.Printf("[gb agent start] warning: could not write workspace .claude/settings.json: %v\n", err2)
		}
	}

	writeOnboardingSkip()

	// ── Context + signal handling ────────────────────────────────────────

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// ── Mux registration ─────────────────────────────────────────────────

	mux := newMuxClient()
	coopURL := fmt.Sprintf("http://%s:%d", cfg.podIP, cfg.coopPort)
	go func() {
		if err := mux.Register(ctx, cfg.hostname, coopURL, cfg.role, cfg.agent, cfg.hostname, cfg.podIP); err != nil {
			fmt.Printf("[gb agent start] mux register error: %v\n", err)
		}
	}()
	defer mux.Deregister(cfg.hostname)

	// ── OAuth refresh (survives coop restarts) ───────────────────────────

	go oauthRefreshLoop(ctx, claudeStateDir, credMode)

	// ── Restart loop ──────────────────────────────────────────────────────

	const minRuntimeSecs = 30
	restarts := 0

	for {
		if ctx.Err() != nil {
			return nil // SIGTERM/SIGINT — clean exit
		}
		if restarts >= cfg.maxRestarts {
			return fmt.Errorf("max restarts (%d) reached", cfg.maxRestarts)
		}

		cleanStalePipes(coopStateDir)
		resumeLog := findResumeSession(claudeStateDir, cfg.sessionResume)

		start := time.Now()
		exitCode, _ := runCoopOnce(ctx, cfg, coopStateDir, resumeLog)
		elapsed := time.Since(start)

		if ctx.Err() != nil {
			return nil // clean SIGTERM exit
		}

		fmt.Printf("[gb agent start] coop exited (code %d) after %s\n", exitCode, elapsed.Round(time.Second))

		if exitCode != 0 && resumeLog != "" {
			retireStaleSession(resumeLog)
		}

		// Check if the agent requested a polite stop before restarting.
		// Close the bead so it transitions to closed status (it remains
		// queryable for historical purposes — closed beads are not deleted).
		// The reconciler excludes closed beads from desired state, so no
		// new pod will be spawned.
		agentBeadID := envOr("KD_AGENT_ID", os.Getenv("BOAT_AGENT_BEAD_ID"))
		if agentBeadID != "" && isStopRequested(ctx, agentBeadID) {
			fmt.Printf("[gb agent start] stop requested — closing bead and exiting cleanly\n")
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer closeCancel()
			if err := daemon.CloseBead(closeCtx, agentBeadID, map[string]string{"agent_state": "done"}); err != nil {
				fmt.Printf("[gb agent start] warning: close agent bead: %v\n", err)
			}
			return nil
		}

		// If rate-limited, sleep until the reset time before restarting.
		if sleepDur := rateLimitSleepDuration(); sleepDur > 0 {
			fmt.Printf("[gb agent start] rate limited — sleeping %s before restart\n", sleepDur.Round(time.Second))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(sleepDur):
			}
			restarts = 0
			continue
		}

		if elapsed >= time.Duration(minRuntimeSecs)*time.Second {
			restarts = 0
		}
		restarts++
		fmt.Printf("[gb agent start] restarting (attempt %d/%d) in 2s...\n", restarts, cfg.maxRestarts)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// runCoopOnce starts coop for a single session, launches per-session
// goroutines, waits for coop to exit, and returns its exit code.
func runCoopOnce(ctx context.Context, cfg k8sConfig, coopStateDir, resumeLog string) (int, error) {
	coopArgs := []string{
		"--agent=claude",
		fmt.Sprintf("--port=%d", cfg.coopPort),
		fmt.Sprintf("--port-health=%d", cfg.coopHealthPort),
		"--cols=200", "--rows=50",
	}
	if resumeLog != "" {
		coopArgs = append(coopArgs, "--resume", resumeLog)
		fmt.Printf("[gb agent start] starting coop (%s/%s) with resume\n", cfg.role, cfg.agent)
	} else {
		fmt.Printf("[gb agent start] starting coop (%s/%s)\n", cfg.role, cfg.agent)
	}
	coopArgs = append(coopArgs, "--", "sh", "-c", cfg.command)

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	coopWorkdir := resolveCoopWorkdir(cfg)

	coopCmd := exec.CommandContext(sessionCtx, "coop", coopArgs...)
	coopCmd.Dir = coopWorkdir
	coopCmd.Stdout = os.Stdout
	coopCmd.Stderr = os.Stderr
	coopCmd.Env = append(os.Environ(),
		"COOP_LOG_LEVEL="+envOr("COOP_LOG_LEVEL", "info"),
	)
	// Send SIGTERM on context cancellation instead of the default SIGKILL.
	// This gives coop a chance to save session state before being killed.
	coopCmd.Cancel = func() error {
		fmt.Printf("[gb agent start] sending SIGTERM to coop (pid %d)\n", coopCmd.Process.Pid)
		return coopCmd.Process.Signal(syscall.SIGTERM)
	}
	coopCmd.WaitDelay = 20 * time.Second

	if err := coopCmd.Start(); err != nil {
		return 1, fmt.Errorf("start coop: %w", err)
	}

	// Per-session goroutines.
	go autoBypassStartup(sessionCtx, cfg.coopPort)
	go injectInitialPrompt(sessionCtx, cfg.coopPort, cfg.role)
	go monitorAgentExit(sessionCtx, cfg.coopPort)

	waitErr := coopCmd.Wait()
	sessionCancel()

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				exitCode = 1
			}
		}
	}
	return exitCode, nil
}

// resolveCoopWorkdir determines the working directory for coop/Claude Code.
// If the primary project repo was cloned by init-clone (at /home/agent/bot/{project}/work),
// use that as the cwd so agents start inside the actual codebase. Falls back to
// the scaffold workspace if no project repo exists.
func resolveCoopWorkdir(cfg k8sConfig) string {
	if cfg.project != "" {
		// init-clone puts the primary repo at /home/agent/bot/{project}/work.
		repoDir := filepath.Join("/home/agent/bot", cfg.project, "work")
		if info, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil && info.IsDir() {
			fmt.Printf("[gb agent start] using project repo as cwd: %s\n", repoDir)
			// Set KD_WORKSPACE so hooks and commands can find the repo.
			os.Setenv("KD_WORKSPACE", repoDir)
			os.Setenv("WORKSPACE", repoDir)
			return repoDir
		}
	}

	fmt.Printf("[gb agent start] no project repo found, using scaffold workspace: %s\n", cfg.workspace)
	return cfg.workspace
}

// ── helpers ───────────────────────────────────────────────────────────────

func intEnvOr(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func orStr(s, def string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return def
}

// rateLimitSleepDuration reads the sentinel file written by monitorAgentExit
// when a rate limit is detected, parses the reset time from the message,
// and returns how long to sleep. Returns 0 if no sentinel file exists.
func rateLimitSleepDuration() time.Duration {
	const sentinelPath = "/tmp/rate_limit_reset"
	data, err := os.ReadFile(sentinelPath)
	if err != nil {
		return 0
	}
	os.Remove(sentinelPath)

	msg := string(data)

	// Extract reset hour from message (e.g., "resets 9pm (UTC)" -> "9pm").
	dur := parseResetDuration(msg)
	if dur > 0 {
		return dur + time.Minute // buffer
	}

	// Fallback: sleep 30 minutes if we can't parse the reset time.
	fmt.Printf("[gb agent start] could not parse reset time from: %s\n", msg)
	return 30 * time.Minute
}

// parseResetDuration extracts a reset time like "9pm" from the rate limit
// message and returns the duration until that time (UTC).
func parseResetDuration(msg string) time.Duration {
	// Look for patterns like "9pm", "10am", "12pm"
	idx := strings.Index(msg, "resets ")
	if idx < 0 {
		return 0
	}
	rest := msg[idx+len("resets "):]

	var hour int
	var ampm string
	n, _ := fmt.Sscanf(rest, "%d%s", &hour, &ampm)
	if n < 2 || hour < 1 || hour > 12 {
		return 0
	}
	ampm = strings.ToLower(strings.TrimSpace(ampm))
	// Strip trailing non-alpha (e.g., "pm" from "pm (UTC)")
	if strings.HasPrefix(ampm, "pm") {
		if hour != 12 {
			hour += 12
		}
	} else if strings.HasPrefix(ampm, "am") {
		if hour == 12 {
			hour = 0
		}
	} else {
		return 0
	}

	now := time.Now().UTC()
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
	if target.Before(now) {
		target = target.Add(24 * time.Hour)
	}
	return target.Sub(now)
}
