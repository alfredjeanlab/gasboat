package main

// gb hook — Claude Code agent hook subcommands.
//
// Replaces the shell scripts that implement Claude Code hook behaviour:
//   - check-mail.sh + drain-queue.sh  →  gb hook check-mail
//   - prime.sh                         →  gb hook prime
//   - stop-gate.sh                     →  gb hook stop-gate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:     "hook",
	Short:   "Agent hook subcommands (replaces shell hook scripts)",
	GroupID: "orchestration",
}

// ── gb hook check-mail ────────────────────────────────────────────────────

var hookCheckMailCmd = &cobra.Command{
	Use:   "check-mail",
	Short: "Inject unread mail as a system-reminder (replaces check-mail.sh)",
	RunE: func(cmd *cobra.Command, args []string) error {
		me := resolveMailActor()
		if me == "" || me == "unknown" {
			return nil
		}

		result, err := daemon.ListBeadsFiltered(cmd.Context(), beadsapi.ListBeadsQuery{
			Types:    []string{"mail", "message"},
			Statuses: []string{"open"},
			Assignee: me,
			Sort:     "-created_at",
			Limit:    20,
		})
		if err != nil || len(result.Beads) == 0 {
			return nil
		}

		var sb strings.Builder
		sb.WriteString("## Inbox\n\n")
		for _, b := range result.Beads {
			sender := senderFromLabels(b.Labels)
			sb.WriteString(fmt.Sprintf("- %s | %s | %s\n", b.ID, b.Title, sender))
		}
		fmt.Printf("<system-reminder>\n%s</system-reminder>\n", sb.String())
		return nil
	},
}

// ── gb hook prime ─────────────────────────────────────────────────────────

var hookPrimeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Output workflow context as system-reminder (replaces prime.sh)",
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID := resolvePrimeAgentFromEnv(actor)
		outputPrimeForHook(os.Stdout, agentID)
		// Show this agent's assignment bead (BOAT_AGENT_BEAD_ID set by controller).
		beadID := os.Getenv("BOAT_AGENT_BEAD_ID")
		if beadID == "" {
			beadID, _ = resolveAgentID("")
		}
		if beadID != "" {
			out, err := exec.CommandContext(cmd.Context(), "kd", "show", beadID).Output()
			if err == nil && len(out) > 0 {
				fmt.Printf("<system-reminder>\n## Assignment\n\n%s</system-reminder>\n", out)
			}
		}
		// Warn if agent has no claimed work and no open decision.
		outputClaimReminder(cmd.Context(), actor)
		return nil
	},
}

// ── gb hook stop-gate ─────────────────────────────────────────────────────

var hookStopGateCmd = &cobra.Command{
	Use:   "stop-gate",
	Short: "Emit Stop hook event and handle gate block (replaces stop-gate.sh)",
	RunE: func(cmd *cobra.Command, args []string) error {
		var stdinEvent map[string]any
		if err := json.NewDecoder(os.Stdin).Decode(&stdinEvent); err != nil {
			stdinEvent = map[string]any{}
		}

		claudeSessionID, _ := stdinEvent["session_id"].(string)
		cwd, _ := stdinEvent["cwd"].(string)
		if cwd == "" {
			cwd, _ = os.Getwd()
		}

		agentBeadID, _ := resolveAgentID("")
		if agentBeadID == "" {
			agentBeadID = resolveAgentByActor(cmd.Context(), actor)
		}

		req := beadsapi.EmitHookRequest{
			AgentBeadID:     agentBeadID,
			HookType:        "Stop",
			ClaudeSessionID: claudeSessionID,
			CWD:             cwd,
			Actor:           actor,
		}
		resp, err := emitHookWithRetry(cmd.Context(), req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gb hook stop-gate: server error after retries: %v\n", err)
			os.Exit(1)
		}

		for _, w := range resp.Warnings {
			fmt.Printf("<system-reminder>%s</system-reminder>\n", w)
		}
		if resp.Inject != "" {
			fmt.Print(resp.Inject)
		}

		if resp.Block {
			blockJSON, _ := json.Marshal(map[string]string{
				"decision": "block",
				"reason":   resp.Reason,
			})
			fmt.Fprintf(os.Stderr, "%s\n", blockJSON)
			os.Exit(2)
		}

		return nil
	},
}

// ── gb hook workspace-check ───────────────────────────────────────────────

var hookWorkspaceCheckCmd = &cobra.Command{
	Use:   "workspace-check",
	Short: "Warn if working directory is not inside a per-bead worktree",
	Long: `Checks whether the agent's current working directory is inside a
per-bead worktree (under .beads/worktrees/<bead-id>/). If not, emits a
system-reminder so the agent knows to run 'gb workspace setup <bead-id>'.

Exits 0 always — non-blocking by design.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, _ := os.Getwd()
		// Check if cwd is inside .beads/worktrees/.
		if strings.Contains(filepath.ToSlash(cwd), "/.beads/worktrees/") {
			return nil
		}
		// Not in a worktree — emit a reminder.
		fmt.Printf("<system-reminder>[workspace] Working directory is not a per-bead worktree.\n" +
			"Run 'gb workspace setup <bead-id>' to create an isolated worktree before making changes.</system-reminder>\n")
		return nil
	},
}

func init() {
	hookCmd.AddCommand(hookCheckMailCmd)
	hookCmd.AddCommand(hookPrimeCmd)
	hookCmd.AddCommand(hookStopGateCmd)
	hookCmd.AddCommand(hookWorkspaceCheckCmd)
}

// emitHookWithRetry calls daemon.EmitHook with increasing backoff on transient
// errors. Returns an error only after all retries are exhausted.
func emitHookWithRetry(ctx context.Context, req beadsapi.EmitHookRequest) (*beadsapi.EmitHookResponse, error) {
	backoffs := []time.Duration{5 * time.Second, 30 * time.Second, 1 * time.Minute, 5 * time.Minute}
	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		resp, err := daemon.EmitHook(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt < len(backoffs) {
			fmt.Fprintf(os.Stderr, "gb hook: EmitHook failed (attempt %d/%d), retrying in %s: %v\n",
				attempt+1, len(backoffs)+1, backoffs[attempt], err)
			select {
			case <-time.After(backoffs[attempt]):
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			}
		}
	}
	return nil, lastErr
}

// outputClaimReminder checks if the agent has any in-progress claimed work or
// open decisions. If not, emits a system-reminder nudging them to claim a bead
// before starting work.
func outputClaimReminder(ctx context.Context, agentName string) {
	if agentName == "" {
		return
	}

	// Check for any in-progress work claimed by this agent.
	task, err := daemon.ListAssignedTask(ctx, agentName)
	if err == nil && task != nil {
		return // Agent already has claimed work — no reminder needed.
	}

	// Check for any open decisions this agent is waiting on.
	decisions, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"decision"},
		Statuses: []string{"open"},
		Assignee: agentName,
		Limit:    1,
	})
	if err == nil && len(decisions.Beads) > 0 {
		return // Agent has a pending decision — already engaged.
	}

	fmt.Printf("<system-reminder>\nNo claimed work found. Run `gb ready` to see available beads, then `kd claim <id>` to claim one before starting work.\n</system-reminder>\n")
}
