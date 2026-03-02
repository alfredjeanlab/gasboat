package main

// gb stop — request polite agent despawn.
//
// Sets stop_requested=true on the agent bead and POSTs to the local coop
// API to trigger a graceful session shutdown. The entrypoint restart loop
// checks the stop_requested flag after coop exits and terminates cleanly
// instead of restarting, then closes the bead so the reconciler stops
// tracking this pod.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Request polite agent despawn — prevents pod restart after exit",
	Long: `Mark this agent as stop-requested.

After calling 'gb stop', exit normally (finish your current turn). The entrypoint
will detect the stop request and close the agent bead instead of restarting,
causing the pod to terminate cleanly.

This is the correct way to despawn an agent voluntarily. Crashing or simply
exiting without calling 'gb stop' will trigger an automatic restart.

By default, the session JSONL is left intact so a re-spawned agent (with the same
name) resumes where it left off via coop --resume. Use --clean to mark all session
logs as stale instead, causing the next spawn to start a fresh conversation.

Usage:
  gb stop                          # request despawn, then exit normally
  gb stop --reason "finished work" # leave a note on the agent bead
  gb stop --clean                  # also retire session JSONL (fresh start on re-spawn)
  gb stop --force                  # skip in-progress work check`,
	GroupID: "session",
	RunE:    runStop,
}

var (
	stopForce  bool
	stopReason string
	stopClean  bool
)

func init() {
	stopCmd.Flags().BoolVar(&stopForce, "force", false, "skip in-progress work check")
	stopCmd.Flags().StringVar(&stopReason, "reason", "", "reason for stopping (added as a comment on the agent bead)")
	stopCmd.Flags().BoolVar(&stopClean, "clean", false, "retire session JSONL so next spawn starts a fresh conversation")
}

func runStop(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	agentID, err := resolveAgentIDWithFallback(ctx, "")
	if err != nil {
		return fmt.Errorf("agent identity required: %w", err)
	}

	// Warn if agent has unclaimed in-progress work (unless --force).
	if !stopForce {
		task, taskErr := daemon.ListAssignedTask(ctx, actor)
		if taskErr == nil && task != nil {
			fmt.Fprintf(os.Stderr, "Warning: you have claimed in-progress work:\n")
			fmt.Fprintf(os.Stderr, "  %s: %s\n\n", task.ID, task.Title)
			fmt.Fprintf(os.Stderr, "Close it first ('kd close %s') or use --force to override.\n", task.ID)
			return fmt.Errorf("claimed work pending — use --force to override")
		}
	}

	// Add a comment to the agent bead explaining the stop reason.
	reason := stopReason
	if reason == "" {
		reason = "agent self-stopped"
	}
	commentAuthor := actor
	if commentAuthor == "" || commentAuthor == "unknown" {
		commentAuthor = agentID
	}
	if err := daemon.AddComment(ctx, agentID, commentAuthor, "gb stop: "+reason); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not add stop comment: %v\n", err)
	}

	// --clean: retire all session JSONL files so next spawn starts fresh.
	if stopClean {
		if retired, errs := retireAgentSessions(); retired > 0 || len(errs) > 0 {
			fmt.Printf("Retired %d session log(s) (next spawn will start fresh).\n", retired)
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "Warning: %v\n", e)
			}
		}
	}

	if err := daemon.UpdateBeadFields(ctx, agentID, map[string]string{
		"stop_requested": "true",
	}); err != nil {
		return fmt.Errorf("setting stop_requested on bead %s: %w", agentID, err)
	}

	fmt.Printf("Stop requested for agent %s.\n", agentID)

	// Request coop shutdown so the session actually terminates.
	// Without this, the entrypoint blocks on wait(COOP_PID) and never
	// reaches the stop_requested check.
	requestCoopShutdown()

	fmt.Printf("Coop shutdown requested — pod will terminate after session ends.\n")
	return nil
}

// requestCoopShutdown POSTs to the local coop API to trigger a graceful
// session shutdown. This mirrors what monitor_agent_exit() does in the
// entrypoint. Failures are non-fatal since the agent may not be running
// inside coop (e.g. local dev).
func requestCoopShutdown() {
	coopURL := os.Getenv("COOP_URL")
	if coopURL == "" {
		coopURL = "http://localhost:8080"
	}
	shutdownURL := strings.TrimRight(coopURL, "/") + "/api/v1/shutdown"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(shutdownURL, "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Note: could not reach coop at %s (not running inside coop?): %v\n", shutdownURL, err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "Note: coop shutdown returned status %d\n", resp.StatusCode)
	}
}

// retireAgentSessions renames all .jsonl session logs under ~/.claude/projects/
// to .jsonl.stale, causing the restart loop's findResumeSession to skip them.
// Returns the count of retired files and any errors encountered.
func retireAgentSessions() (int, []error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return 0, []error{fmt.Errorf("finding home dir: %w", err)}
	}
	projectsDir := filepath.Join(homeDir, ".claude", "projects")

	var errs []error
	retired := 0
	_ = filepath.Walk(projectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") || strings.Contains(path, "/subagents/") {
			return nil
		}
		stalePath := path + ".stale"
		if renameErr := os.Rename(path, stalePath); renameErr != nil {
			errs = append(errs, fmt.Errorf("retiring %s: %w", path, renameErr))
		} else {
			retired++
		}
		return nil
	})
	return retired, errs
}
