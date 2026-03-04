package main

// gb stop — request polite agent despawn.
//
// Sets stop_requested=true on the agent bead and POSTs to the local coop
// API to trigger a graceful session shutdown. The entrypoint restart loop
// checks the stop_requested flag after coop exits and terminates cleanly
// instead of restarting, then closes the bead so the reconciler stops
// tracking this pod.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:     "stop",
	Aliases: []string{"done"},
	Short:   "Request polite agent despawn — prevents pod restart after exit",
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
  gb stop --wrapup '{"accomplishments":"Closed 3 bugs","blockers":"API key pending"}'
  gb stop --clean                  # also retire session JSONL (fresh start on re-spawn)
  gb stop --force                  # skip in-progress work check`,
	GroupID: "session",
	RunE:    runStop,
}

var (
	stopForce  bool
	stopReason string
	stopClean  bool
	stopWrapUp string
)

func init() {
	stopCmd.Flags().BoolVar(&stopForce, "force", false, "skip in-progress work check")
	stopCmd.Flags().StringVar(&stopReason, "reason", "", "reason for stopping (added as a comment on the agent bead)")
	stopCmd.Flags().BoolVar(&stopClean, "clean", false, "retire session JSONL so next spawn starts a fresh conversation")
	stopCmd.Flags().StringVar(&stopWrapUp, "wrapup", "", "structured wrap-up message as JSON (see WrapUp schema)")
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

	// Check delivery: if the agent has unpushed commits, block stop.
	if !stopForce {
		if err := checkDelivery(); err != nil {
			return err
		}
	}

	commentAuthor := actor
	if commentAuthor == "" || commentAuthor == "unknown" {
		commentAuthor = agentID
	}

	// Handle structured wrap-up if --wrapup is provided.
	if stopWrapUp != "" {
		wrapup, commentText, err := processWrapUp(stopWrapUp)
		if err != nil {
			return err
		}

		// Validate against requirements from config beads (falls back to defaults).
		reqs := LoadWrapUpRequirements(ctx, daemon, agentID)
		if issues := reqs.Validate(wrapup); len(issues) > 0 {
			if reqs.Enforce == "hard" && !stopForce {
				fmt.Fprintf(os.Stderr, "Wrap-up validation failed:\n")
				for _, issue := range issues {
					fmt.Fprintf(os.Stderr, "  - %s\n", issue)
				}
				fmt.Fprintf(os.Stderr, "\nUse --force to override.\n")
				return fmt.Errorf("wrap-up incomplete — use --force to override")
			}
			// Soft enforcement: warn but proceed.
			for _, issue := range issues {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", issue)
			}
		}

		// Store structured wrap-up as a JSON field on the agent bead.
		wrapupJSON, err := MarshalWrapUp(wrapup)
		if err != nil {
			return fmt.Errorf("serializing wrap-up: %w", err)
		}
		if err := daemon.UpdateBeadFields(ctx, agentID, map[string]string{
			WrapUpFieldName: wrapupJSON,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not store wrap-up: %v\n", err)
		}

		// Also add a human-readable comment for the activity log.
		if err := daemon.AddComment(ctx, agentID, commentAuthor, commentText); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not add wrap-up comment: %v\n", err)
		}
		fmt.Println("Wrap-up recorded.")
	} else {
		// Legacy path: use --reason or default message.
		reason := stopReason
		if reason == "" {
			reason = "agent self-stopped"
		}
		if err := daemon.AddComment(ctx, agentID, commentAuthor, "gb stop: "+reason); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not add stop comment: %v\n", err)
		}
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

// checkDelivery verifies that the agent has pushed its work before stopping.
// Returns an error if there are unpushed commits on the current branch.
func checkDelivery() error {
	// Find the workspace directory — check common agent workspace paths.
	workspace := os.Getenv("WORKSPACE")
	if workspace == "" {
		workspace = os.Getenv("HOME") + "/workspace"
	}

	// Check if it's a git repo.
	gitDir := filepath.Join(workspace, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil // not a git repo, nothing to check
	}

	// Check for unpushed commits: compare HEAD with upstream.
	// git rev-list @{u}..HEAD counts commits ahead of upstream.
	cmd := exec.Command("git", "-C", workspace, "rev-list", "--count", "@{u}..HEAD")
	out, err := cmd.Output()
	if err != nil {
		// No upstream set — check if there are any local commits at all.
		cmd2 := exec.Command("git", "-C", workspace, "log", "--oneline", "-1")
		if out2, err2 := cmd2.Output(); err2 == nil && len(strings.TrimSpace(string(out2))) > 0 {
			// There are commits but no upstream — agent never pushed.
			branch := currentBranch(workspace)
			if branch == "main" || branch == "master" {
				fmt.Fprintf(os.Stderr, "Warning: you have local commits on '%s' that were never pushed to a feature branch.\n", branch)
				fmt.Fprintf(os.Stderr, "Create a branch, push it, and open a PR before stopping.\n")
				fmt.Fprintf(os.Stderr, "Use --force to override.\n")
				return fmt.Errorf("unpushed commits on %s — create a branch + PR, or use --force", branch)
			}
			fmt.Fprintf(os.Stderr, "Warning: branch '%s' has no upstream. Push with: git push -u origin %s\n", branch, branch)
			fmt.Fprintf(os.Stderr, "Use --force to override.\n")
			return fmt.Errorf("unpushed branch '%s' — push and create a PR, or use --force", branch)
		}
		return nil // no commits at all, fine
	}

	count := strings.TrimSpace(string(out))
	if count != "0" {
		branch := currentBranch(workspace)
		fmt.Fprintf(os.Stderr, "Warning: %s unpushed commit(s) on branch '%s'.\n", count, branch)
		fmt.Fprintf(os.Stderr, "Push your branch and create a PR before stopping.\n")
		fmt.Fprintf(os.Stderr, "Use --force to override.\n")
		return fmt.Errorf("%s unpushed commit(s) on '%s' — push and create a PR, or use --force", count, branch)
	}

	return nil
}

// currentBranch returns the current git branch name in the given directory.
func currentBranch(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// processWrapUp parses the --wrapup flag value into a WrapUp struct and
// generates a human-readable comment. The flag value must be valid JSON
// matching the WrapUp schema.
func processWrapUp(raw string) (*WrapUp, string, error) {
	var w WrapUp
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return nil, "", fmt.Errorf("invalid --wrapup JSON: %w\nExpected format: {\"accomplishments\":\"...\",\"blockers\":\"...\",\"handoff_notes\":\"...\"}", err)
	}
	return &w, WrapUpToComment(&w), nil
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
