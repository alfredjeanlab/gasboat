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

	// 1. Workflow context.
	role := os.Getenv("BOAT_ROLE")
	outputWorkflowContext(w, role)

	// 2. Advice.
	if !primeNoAdvice && agentID != "" {
		outputAdvice(w, agentID)
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

// outputWorkflowContext fetches workflow context from config beads (claude-instructions
// category), falling back to KV config, then to hardcoded defaults.
func outputWorkflowContext(w io.Writer, role string) {
	ctx := context.Background()

	// Build subscriptions for config resolution.
	subs := []string{"global:*"}
	if role != "" {
		subs = append(subs, "role:"+role)
	}

	merged, count := ResolveConfigBeads(ctx, daemon, "claude-instructions", subs)
	if count > 0 {
		outputConfigSections(w, merged)
		return
	}

	// Fallback: try KV store.
	merged, source := ResolveConfigWithFallback(ctx, daemon, daemon, "claude-instructions", role, subs)
	if source != "" {
		outputConfigSections(w, merged)
		return
	}

	// Final fallback: hardcoded defaults.
	outputWorkflowContextHardcoded(w, role)
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

// outputWorkflowContextHardcoded writes the hardcoded workflow context as a
// fallback when no config beads exist.
// The role parameter controls which sections are included:
//   - "polecat": omits Finding Work / Starting work sections, uses single-task lifecycle
//   - all others (crew, captain, ""): full workflow context
func outputWorkflowContextHardcoded(w io.Writer, role string) {
	header := `# Beads Workflow Context

> **Context Recovery**: Run ` + "`gb prime`" + ` after compaction, clear, or new session
> Hooks auto-call this when configured

# SESSION CLOSE PROTOCOL

**CRITICAL**: Before saying "done" or "complete", you MUST run this checklist:

` + "```" + `
[ ] 1. git status              (check what changed)
[ ] 2. git add <files>         (stage code changes)
[ ] 3. git commit -m "..."     (commit code)
[ ] 4. git push                (push to remote)
` + "```" + `

**NEVER skip this.** Work is not done until pushed.

## Core Rules
- **Default**: Use kd for CRUD (` + "`kd create`" + `, ` + "`kd show`" + `, ` + "`kd close`" + `), gb for orchestration (` + "`gb ready`" + `, ` + "`gb decision`" + `, ` + "`gb yield`" + `)
- **Prohibited**: Do NOT use TodoWrite, TaskCreate, or markdown files for task tracking
- **Workflow**: Create kbeads issue BEFORE writing code, ` + "`kd claim <id>`" + ` when starting
- Persistence you don't need beats lost context
- Git workflow: beads auto-synced by Postgres backend
- Session management: check ` + "`gb ready`" + ` for available work
`
	fmt.Fprint(w, header)

	// Finding Work and Starting work sections — omitted for polecat.
	if role != "polecat" {
		findingWork := `
## Essential Commands

### Finding Work
- ` + "`gb ready`" + ` - Show issues ready to work (no blockers)
- ` + "`gb news`" + ` - Show in-progress work by others (check for conflicts before starting)
- ` + "`kd list --status=open`" + ` - All open issues
- ` + "`kd list --status=in_progress`" + ` - Your active work
- ` + "`kd show <id>`" + ` - Detailed issue view with dependencies

### Creating & Updating
- ` + "`kd create \"...\" --type=task|bug|feature --priority=2`" + ` - New issue (title is positional)
  - Priority: 0-4 or P0-P4 (0=critical, 2=medium, 4=backlog). NOT "high"/"medium"/"low"
- ` + "`kd claim <id>`" + ` - Claim work (sets assignee + status=in_progress)
- ` + "`kd update <id> --assignee=username`" + ` - Assign to someone
- ` + "`kd close <id>`" + ` - Mark complete
- **WARNING**: Do NOT use ` + "`kd edit`" + ` - it opens $EDITOR (vim/nano) which blocks agents

### Dependencies & Blocking
- ` + "`kd dep add <issue> <depends-on>`" + ` - Add dependency
- ` + "`kd dep list <id>`" + ` - List dependencies of a bead
- ` + "`kd show <id>`" + ` - See what's blocking/blocked by this issue (shows deps inline)

### Project Health
- ` + "`kd list --status=open | wc -l`" + ` - Count open issues
- ` + "`gb gate status`" + ` - Show session gate status (decision, commit-push, etc.)

## Common Workflows

**Starting work:**
` + "```bash" + `
gb news            # Check what others are working on (avoid conflicts)
gb ready           # Find available work
kd show <id>       # Review issue details
kd claim <id>      # Claim it (sets assignee + in_progress)
` + "```" + `

**Completing work:**
` + "```bash" + `
kd close <id>              # Close completed issue
git add <files> && git commit -m "..." && git push
` + "```" + `

**Creating dependent work:**
` + "```bash" + `
kd create "Implement feature X" --type=feature
kd create "Write tests for X" --type=task
kd dep add <tests-id> <feature-id>  # Tests depend on Feature
` + "```" + `
`
		fmt.Fprint(w, findingWork)
	} else {
		polecatCommands := `
## Essential Commands

- ` + "`kd show <id>`" + ` - View your assigned task details
- ` + "`kd close <id>`" + ` - Mark your task complete
- ` + "`gb done`" + ` - Despawn after completing your task
`
		fmt.Fprint(w, polecatCommands)
	}

	decisions := `
## Human Decisions

When you need human input (approval, choices, clarification), create a decision checkpoint.
Every option MUST declare an ` + "`artifact_type`" + ` — what you will deliver if that option is chosen.

` + "```bash" + `
gb decision create --no-wait \
  --prompt="Completed auth refactor. Tests pass. Two options for session handling:" \
  --options='[
    {"id":"jwt","short":"Use JWT","label":"Stateless JWT tokens with refresh rotation","artifact_type":"plan"},
    {"id":"session","short":"Use sessions","label":"Server-side sessions with Redis store","artifact_type":"plan"},
    {"id":"skip","short":"Defer","label":"Keep current impl, file a tech debt issue","artifact_type":"bug"}
  ]'
gb yield  # blocks until human responds
` + "```" + `

**Artifact types:** ` + "`report`" + ` (work summary), ` + "`plan`" + ` (implementation plan), ` + "`checklist`" + ` (verification steps), ` + "`diff-summary`" + ` (code changes), ` + "`epic`" + ` (feature breakdown), ` + "`bug`" + ` (bug report)

If the chosen option requires an artifact, ` + "`gb yield`" + ` will tell you — submit it with:
` + "`gb decision report <decision-id> --content '...'`" + `

**Decision commands:**
- ` + "`gb decision create --prompt=\"...\" --options='[...]'`" + ` - Create decision (` + "`--no-wait`" + ` to not block)
- ` + "`gb yield`" + ` - Wait for human response
- ` + "`gb decision report <id> --content '...'`" + ` - Submit required artifact
- ` + "`gb decision list`" + ` - Show pending decisions
- ` + "`gb decision show <id>`" + ` - Decision details

## Session Resumption

Two complementary mechanisms restore context after interruptions:

**Conversation resume** (` + "`coop --resume`" + `):
- Managed **automatically** by the entrypoint on pod restart
- Restores the previous Claude conversation history
- No agent action required — the entrypoint handles it

**Context recovery** (` + "`gb prime`" + `):
- Run by agents after compaction, ` + "`/clear`" + `, or a new session
- Injects fresh workflow context: assignment, roster, advice, auto-assign
- Hooks auto-call this on SessionStart — run manually if context is stale
`
	fmt.Fprint(w, decisions)

	// Lifecycle section — polecat gets single-task language, others get full.
	if role == "polecat" {
		lifecycle := `
## Single-Task Lifecycle

You are a **single-task ephemeral agent**. Your lifecycle is simple:

1. Check your pre-assigned task (` + "`BOAT_TASK_ID`" + ` or ` + "`kd list --status=in_progress`" + `)
2. Do the work thoroughly (commit, push)
3. Close the bead: ` + "`kd close <bead-id>`" + `
4. Despawn: ` + "`gb done`" + `

` + "```bash" + `
kd show <bead-id>          # review your assigned task
# ... do the work ...
kd close <bead-id>         # close completed work
gb done                    # despawn — do NOT look for more work
` + "```" + `

**Do NOT** run ` + "`gb ready`" + ` or look for additional tasks. You exist for one task only.
**Do NOT** just exit without calling ` + "`gb done`" + ` — exiting alone triggers an automatic restart.
`
		fmt.Fprint(w, lifecycle)
	} else {
		lifecycle := `
## Agents Are Ephemeral

Agents are ephemeral by default: start up, do the work, then despawn. Do NOT linger or idle-loop waiting for more work.

**Lifecycle:**
1. Start up → check for claimed in-progress work (resume it) or find new work via ` + "`gb ready`" + `
2. Claim a task → do the work thoroughly (commit, push, close bead)
3. Call ` + "`gb done`" + ` to despawn cleanly

` + "```bash" + `
kd close <bead-id>     # close completed work
gb done                # signal entrypoint not to restart this pod
` + "```" + `

**Do NOT** just exit without calling ` + "`gb done`" + ` — exiting alone triggers an automatic restart.
If there is more work in the ready queue, you MAY claim another task before stopping.
`
		fmt.Fprint(w, lifecycle)
	}

	stopGate := `
## Stop Gate Contract

The **decision gate** is the Slack operator's re-entry handle.

**Rules:**
- **NEVER** use ` + "`gb gate mark decision`" + ` to satisfy the gate manually — this is blocked for agents (requires ` + "`--force`" + `, operator-only).
- When you are **blocked mid-task** and need human input, create a decision and yield.
- When you have **finished all work**, just call ` + "`gb done`" + ` — no decision checkpoint needed.
- The only legitimate ways to clear the gate are:
  1. ` + "`gb done`" + ` — polite despawn when you have finished your work (preferred)
  2. ` + "`gb yield`" + ` — blocks until a human resolves your decision bead (use when genuinely blocked)
`
	fmt.Fprint(w, stopGate)
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
