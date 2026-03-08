package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var primeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Output AI-optimized workflow context",
	Long: `Output essential workflow context in AI-optimized markdown format.

Outputs 5 sections:
1. Workflow context — session close protocol, core rules, essential commands
2. Advice — scoped advice beads matching agent subscriptions
3. Jack awareness — active/expired infrastructure jacks
4. Agent roster — live agents with tasks, idle times, crash state
5. Auto-assign — assigns highest-priority ready task if agent is idle

Agent identity is resolved from KD_ACTOR or KD_AGENT_ID env vars,
or the --for flag.

Examples:
  gb prime
  gb prime --for beads/crew/test-agent
  gb prime --no-advice
  gb prime --json`,
	GroupID: "session",
	RunE:   runPrime,
}

var (
	primeForAgent string
	primeNoAdvice bool
)

func init() {
	primeCmd.Flags().StringVar(&primeForAgent, "for", "", "agent ID to inject matching advice for")
	primeCmd.Flags().BoolVar(&primeNoAdvice, "no-advice", false, "suppress advice output")
}

func runPrime(cmd *cobra.Command, args []string) error {
	w := os.Stdout

	agentID := resolvePrimeAgentIdentity(cmd)

	// Prewarmed agent standby: if BOAT_AGENT_STATE=prewarmed, output a
	// lightweight standby message instead of full workflow context. The agent
	// waits for a nudge from the pool manager when assigned work.
	if os.Getenv("BOAT_AGENT_STATE") == "prewarmed" {
		outputPrewarmedStandby(w, agentID)
		return nil
	}

	// 0. Identity announcement.
	role := os.Getenv("BOAT_ROLE")
	outputIdentity(w, agentID, role)

	// 1. Workflow context.
	outputWorkflowContext(w, role)

	// 2. Advice.
	if !primeNoAdvice && agentID != "" {
		outputAdvice(w, agentID)
		outputAdviceRoleDiff(w, agentID)
	}

	// 2b. Wrap-up expectations.
	if agentID != "" {
		outputWrapUpExpectations(w, agentID)
	}

	// 3. Jack awareness.
	outputJackSection(w)

	// 4. Agent roster.
	outputRosterSection(w, agentID)

	// 5. Auto-assign (if agent has no in_progress bead).
	if agentID != "" {
		outputAutoAssign(w, agentID)
	}

	return nil
}

// outputPrewarmedStandby outputs a standby message for prewarmed agents.
// These agents have Claude running but are waiting for work assignment via nudge.
func outputPrewarmedStandby(w io.Writer, agentID string) {
	fmt.Fprint(w, `# Prewarmed Agent — Standby Mode

You are a **prewarmed agent** waiting in the idle pool for work assignment.

**Do NOT** seek work, run `+"`gb ready`"+`, or create beads. Your session is warm and
ready — when a Slack thread mention or operator assigns you, the pool manager
will inject a nudge with your task description.

**What to do:** Wait for a nudge message. It will contain:
- Thread context (Slack channel, thread, description)
- Your assigned task details

When you receive the nudge, treat it as your starting prompt and begin work immediately.
Follow the standard workflow: claim the task, do the work, commit, push, close the bead, `+"`gb done`"+`.
`)
	if agentID != "" {
		fmt.Fprintf(w, "\nAgent: **%s**\n", agentID)
	}
}

// resolvePrimeAgentIdentity resolves the agent identity for prime output.
func resolvePrimeAgentIdentity(cmd *cobra.Command) string {
	if primeForAgent != "" {
		return primeForAgent
	}
	if v := os.Getenv("KD_ACTOR"); v != "" {
		return v
	}
	if v := os.Getenv("KD_AGENT_ID"); v != "" {
		return v
	}
	if actor != "" && actor != "unknown" {
		return actor
	}
	return ""
}

// outputIdentity outputs the agent's identity (name, role, project) so it can
// announce itself at session start. This runs before workflow context so the
// agent knows who it is from the very first line of prime output.
func outputIdentity(w io.Writer, agentID, role string) {
	project := defaultGBProject()

	// Only output if we have meaningful identity info.
	if agentID == "" && role == "" && project == "" {
		return
	}

	fmt.Fprintln(w, "## Your Identity")
	fmt.Fprintln(w)
	if agentID != "" {
		fmt.Fprintf(w, "- **Agent**: %s\n", agentID)
	}
	if role != "" {
		fmt.Fprintf(w, "- **Role**: %s\n", role)
	}
	if project != "" {
		fmt.Fprintf(w, "- **Project**: %s\n", project)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Announce your role and project in your first message to the team (e.g. via `gb squawk`).")
	fmt.Fprintln(w)
}

// outputWorkflowContext fetches workflow context from config beads (claude-instructions
// category), falling back to hardcoded defaults.
func outputWorkflowContext(w io.Writer, role string) {
	ctx := context.Background()

	// Build subscriptions for config resolution.
	subs := []string{"global"}
	if role != "" {
		subs = append(subs, "role:"+role)
	}

	merged, count := ResolveConfigBeads(ctx, daemon, "claude-instructions", subs)
	if count > 0 {
		outputConfigSections(w, merged)
		return
	}

	fmt.Fprintln(w, "<!-- no claude-instructions config beads found — run EnsureConfigs or seed config beads -->")
}

// outputConfigSections outputs resolved config bead sections in canonical order.
func outputConfigSections(w io.Writer, config map[string]any) {
	sections := []string{
		"prime_header", "session_close", "core_rules", "commands",
		"workflows", "decisions", "session_resumption", "lifecycle", "stop_gate",
	}
	for _, key := range sections {
		if val, ok := config[key].(string); ok && val != "" {
			fmt.Fprintln(w, val)
		}
	}
}


// outputJackSection fetches active/expired jacks and outputs warnings.
func outputJackSection(w io.Writer) {
	ctx := context.Background()

	result, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"jack"},
		Statuses: []string{"in_progress"},
		Limit:    50,
	})
	if err != nil || len(result.Beads) == 0 {
		return
	}

	now := time.Now()
	type jackSummary struct {
		bead       *beadsapi.BeadDetail
		target     string
		remaining  time.Duration
		expiredAgo time.Duration
		expired    bool
	}

	var jacks []jackSummary
	for _, b := range result.Beads {
		j := jackSummary{bead: b}
		j.target = b.Fields["jack_target"]

		if b.DueAt != "" {
			if dueAt, err := time.Parse(time.RFC3339, b.DueAt); err == nil {
				if now.After(dueAt) {
					j.expired = true
					j.expiredAgo = now.Sub(dueAt)
				} else {
					j.remaining = time.Until(dueAt)
				}
			}
		}

		jacks = append(jacks, j)
	}

	fmt.Fprintf(w, "\n## Active Jacks (%d)\n\n", len(jacks))
	for _, j := range jacks {
		agent := j.bead.Assignee
		if agent == "" {
			agent = j.bead.CreatedBy
		}
		if j.expired {
			fmt.Fprintf(w, "- **EXPIRED** `%s` on `%s` (by %s, expired %s ago) — run `kd jack down %s`\n",
				j.bead.ID, j.target, agent, formatDuration(j.expiredAgo), j.bead.ID)
		} else {
			remaining := "unknown"
			if j.remaining > 0 {
				remaining = formatDuration(j.remaining) + " remaining"
			}
			fmt.Fprintf(w, "- `%s` on `%s` (by %s, %s)\n",
				j.bead.ID, j.target, agent, remaining)
		}
	}
	fmt.Fprintln(w)
}

// outputRosterSection fetches the live agent roster and outputs it.
func outputRosterSection(w io.Writer, self string) {
	ctx := context.Background()

	roster, err := daemon.GetAgentRoster(ctx, 1800) // 30-min threshold
	if err != nil || roster == nil || len(roster.Actors) == 0 {
		return
	}

	// Partition into active vs stale.
	const staleThresholdSecs = 600 // 10 minutes
	var active, stale []beadsapi.RosterActor
	for _, a := range roster.Actors {
		if a.Reaped {
			stale = append(stale, a)
			continue
		}
		isStopped := a.LastEvent == "Stop" && a.IdleSecs > 60
		if isStopped {
			continue
		}
		if a.IdleSecs > staleThresholdSecs {
			stale = append(stale, a)
		} else {
			active = append(active, a)
		}
	}

	if len(active) == 0 && len(stale) == 0 {
		return
	}

	fmt.Fprintf(w, "\n## Active Agents (%d)\n\n", len(active))
	if self != "" {
		fmt.Fprintf(w, "You are **%s**. Do not pick up other agents' in-progress tasks.\n\n", self)
	}

	for _, a := range active {
		idleStr := formatIdleDur(a.IdleSecs)
		youTag := ""
		if self != "" && a.Actor == self {
			youTag = " <- you"
		}

		if a.TaskID != "" {
			epicStr := ""
			if a.EpicTitle != "" {
				epicStr = fmt.Sprintf(" (epic: %s)", a.EpicTitle)
			}
			fmt.Fprintf(w, "- **%s**%s — working on %s: %s%s (idle %s)\n",
				a.Actor, youTag, a.TaskID, a.TaskTitle, epicStr, idleStr)
		} else {
			activityHint := ""
			if a.ToolName != "" {
				activityHint = fmt.Sprintf(", last: %s", a.ToolName)
			}
			fmt.Fprintf(w, "- **%s**%s — active, no claimed task (idle %s%s)\n",
				a.Actor, youTag, idleStr, activityHint)
		}
	}

	// Show stale agents.
	if len(stale) > 0 {
		var crashed, idle []string
		for _, a := range stale {
			idleStr := formatIdleDur(a.IdleSecs)
			if a.Reaped {
				if a.TaskID != "" {
					crashed = append(crashed, fmt.Sprintf("%s (had %s: %s)", a.Actor, a.TaskID, a.TaskTitle))
				} else {
					crashed = append(crashed, fmt.Sprintf("%s (idle %s)", a.Actor, idleStr))
				}
			} else {
				idle = append(idle, fmt.Sprintf("%s (idle %s)", a.Actor, idleStr))
			}
		}
		if len(crashed) > 0 {
			fmt.Fprintf(w, "\n_Crashed (%d): %s_\n", len(crashed), strings.Join(crashed, ", "))
		}
		if len(idle) > 0 {
			fmt.Fprintf(w, "\n_Stale (%d, likely disconnected): %s_\n", len(idle), strings.Join(idle, ", "))
		}
	}

	// Show unclaimed work.
	if len(roster.UnclaimedTasks) > 0 {
		fmt.Fprintf(w, "\n> **Unclaimed in-progress work** (no assignee — consider claiming):\n")
		for _, t := range roster.UnclaimedTasks {
			fmt.Fprintf(w, ">   - %s [P%d]: %s\n", t.ID, t.Priority, t.Title)
		}
	}

	fmt.Fprintln(w)
}

// isAutoAssignEnabled checks whether auto-assignment is enabled for this agent.
// Inheritance: agent bead overrides project bead. Default is DISABLED.
// Agents are ephemeral and should ask for work on startup rather than being
// auto-assigned. Set auto_assign=true on the agent or project bead to opt in.
func isAutoAssignEnabled(ctx context.Context, proj string) bool {
	// 1. Agent bead (highest precedence).
	if agentBeadID := os.Getenv("KD_AGENT_ID"); agentBeadID != "" {
		if agentBead, err := daemon.GetBead(ctx, agentBeadID); err == nil {
			if v, ok := agentBead.Fields["auto_assign"]; ok {
				return v == "true"
			}
		}
	}

	// 2. Project bead.
	if proj != "" {
		result, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
			Types:    []string{"project"},
			Statuses: []string{"open", "in_progress", "blocked", "deferred"},
			Limit:    50,
		})
		if err == nil {
			for _, b := range result.Beads {
				name := strings.TrimPrefix(b.Title, "Project: ")
				if name == proj {
					if b.Fields["auto_assign"] == "true" {
						return true
					}
					break
				}
			}
		}
	}

	// 3. Default: disabled (agents pull work, not pushed).
	return false
}

// outputAutoAssign checks if the agent has in_progress beads and auto-assigns
// the highest-priority ready task if idle. Skips if BOAT_TASK_ID is set (the
// agent was spawned with a specific pre-assigned task) or if auto_assign is
// disabled on the agent or project bead.
func outputAutoAssign(w io.Writer, agentID string) {
	if os.Getenv("BOAT_TASK_ID") != "" {
		return
	}

	// Require a project context to prevent cross-project assignment.
	proj := defaultGBProject()
	if proj == "" {
		return // no project context — refuse to auto-assign
	}

	ctx := context.Background()

	// Check auto_assign setting (agent bead overrides project bead).
	if !isAutoAssignEnabled(ctx, proj) {
		return
	}

	// Check if agent already has in_progress work.
	resp, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Assignee: agentID,
		Statuses: []string{"in_progress"},
		Limit:    1,
	})
	if err != nil || len(resp.Beads) > 0 {
		return // agent already has work
	}

	// Fetch ready issue-kind tasks scoped to this agent's project.
	ready, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Statuses:   []string{"open"},
		Labels:     []string{"project:" + proj},
		Kinds:      []string{"issue"},
		NoOpenDeps: true,
		Sort:       "priority",
		Limit:      1,
	})
	if err != nil || len(ready.Beads) == 0 {
		return
	}

	// Verify the candidate bead actually belongs to our project.
	// Belt-and-suspenders check: the server filters by label, but we
	// double-check client-side to avoid cross-project assignment.
	task := ready.Beads[0]
	hasProjectLabel := false
	for _, l := range task.Labels {
		if l == "project:"+proj {
			hasProjectLabel = true
			break
		}
	}
	if !hasProjectLabel {
		return
	}

	// Auto-claim.
	inProgress := "in_progress"
	err = daemon.UpdateBead(ctx, task.ID, beadsapi.UpdateBeadRequest{
		Assignee: &agentID,
		Status:   &inProgress,
	})
	if err != nil {
		fmt.Fprintf(w, "\nAuto-claim failed for %s: %v\n", task.ID, err)
		return
	}

	fmt.Fprintf(w, "\nAuto-assigned bead %s: %s\n", task.ID, task.Title)
	fmt.Fprintf(w, "Run `kd show %s` for full details.\n", task.ID)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

func formatIdleDur(secs float64) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", int(secs))
	}
	if secs < 3600 {
		return fmt.Sprintf("%dm%ds", int(secs)/60, int(secs)%60)
	}
	h := int(secs) / 3600
	m := (int(secs) % 3600) / 60
	return fmt.Sprintf("%dh%dm", h, m)
}
