// Package bridge registers bead types, views, and context configs that
// gasboat requires in the beads daemon.  Call EnsureConfigs at startup to
// upsert the canonical definitions; existing user overrides are left alone.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// ConfigSetter can upsert a config key/value in the beads daemon.
type ConfigSetter interface {
	SetConfig(ctx context.Context, key string, value []byte) error
}

// TypeConfig mirrors model.TypeConfig for JSON serialization.
type TypeConfig struct {
	Kind   string     `json:"kind"`
	Fields []FieldDef `json:"fields,omitempty"`
}

// FieldDef mirrors model.FieldDef.
type FieldDef struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Values   []string `json:"values,omitempty"`
}

// ViewConfig is the saved-view schema consumed by `kd view`.
type ViewConfig struct {
	Filter  ViewFilter `json:"filter"`
	Sort    string     `json:"sort,omitempty"`
	Columns []string   `json:"columns,omitempty"`
	Limit   int32      `json:"limit,omitempty"`
}

// ViewFilter matches the filter fields accepted by ListBeads.
type ViewFilter struct {
	Status   []string `json:"status,omitempty"`
	Type     []string `json:"type,omitempty"`
	Kind     []string `json:"kind,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Assignee string   `json:"assignee,omitempty"`
	Search   string   `json:"search,omitempty"`
}

// ContextConfig is the saved-context schema consumed by `kd context`.
type ContextConfig struct {
	Sections []ContextSection `json:"sections"`
}

// ContextSection describes one block of a rendered context.
type ContextSection struct {
	Header string   `json:"header"`
	View   string   `json:"view"`
	Format string   `json:"format,omitempty"` // "table" (default), "list", "count"
	Fields []string `json:"fields,omitempty"` // for "list" format
}

// configs returns every config entry gasboat needs in the daemon.
func configs() map[string]any {
	return map[string]any{
		// --- types -----------------------------------------------------------
		//
		// Both are config-kind beads.  The agent type carries lifecycle state
		// that the controller writes back; the project type holds repo metadata
		// used to configure agent pods.

		"type:agent": TypeConfig{
			Kind: "config",
			Fields: []FieldDef{
				// Agent identity.
				{Name: "project", Type: "string"},
				{Name: "mode", Type: "string"},
				{Name: "role", Type: "string"},
				{Name: "agent", Type: "string"},
				// Agent lifecycle state written back by the controller.
				{Name: "agent_state", Type: "enum", Values: []string{"idle", "spawning", "working", "done", "failed", "rate_limited"}},
				// Pod lifecycle state written back by the controller.
				{Name: "pod_phase", Type: "enum", Values: []string{"pending", "running", "succeeded", "failed"}},
				{Name: "pod_name", Type: "string"},
				{Name: "pod_namespace", Type: "string"},
				{Name: "pod_ready", Type: "boolean"},
				{Name: "coop_url", Type: "string"},
				{Name: "coop_token", Type: "string"},
				// Per-agent overrides (optional).
				{Name: "image", Type: "string"},
				{Name: "mock_scenario", Type: "string"},
				// Agent stop/gate control written by gb stop and gb yield.
				{Name: "stop_requested", Type: "boolean"},
				{Name: "gate_satisfied_by", Type: "string"},
				// Advice subscription overrides.
				{Name: "advice_subscriptions", Type: "string[]"},
				{Name: "advice_subscriptions_exclude", Type: "string[]"},
				// Auto-assignment control: "true" or "false" (default).
				{Name: "auto_assign", Type: "boolean"},
				// Structured wrap-up stored by gb stop --wrapup (JSON-serialized string).
				{Name: "wrapup", Type: "string"},
				// Thread-spawn metadata (written by handleThreadSpawn).
				{Name: "slack_thread_channel", Type: "string"},
				{Name: "slack_thread_ts", Type: "string"},
				{Name: "spawn_source", Type: "string"},
				// Custom prompt passed via /spawn "description" → BOAT_PROMPT env var.
				{Name: "prompt", Type: "string"},
				// Pre-assigned task ID (set by /spawn with ticket or task-first flow).
				{Name: "task_id", Type: "string"},
			},
		},
		"type:mail": TypeConfig{
			Kind: "data",
		},
		"type:message": TypeConfig{
			Kind: "data",
			Fields: []FieldDef{
				{Name: "source_agent", Type: "string"},
				{Name: "text", Type: "string"},
			},
		},
		"type:decision": TypeConfig{
			Kind: "data",
			Fields: []FieldDef{
				{Name: "prompt", Type: "string", Required: true},
				{Name: "options", Type: "json"},
				{Name: "chosen", Type: "string"},
				{Name: "rationale", Type: "string"},
				{Name: "session", Type: "string"},
				{Name: "context", Type: "string"},
				{Name: "requested_by", Type: "string"},
				{Name: "requesting_agent_bead_id", Type: "string"},
				{Name: "responded_by", Type: "string"},
				{Name: "responded_at", Type: "string"},
				{Name: "response_text", Type: "string"},
				{Name: "required_artifact", Type: "string"},
				{Name: "artifact_status", Type: "enum", Values: []string{"pending", "submitted", "accepted"}},
			},
		},
		"type:project": TypeConfig{
			Kind: "config",
			Fields: []FieldDef{
				{Name: "prefix", Type: "string"},
				{Name: "git_url", Type: "string"},
				{Name: "default_branch", Type: "string"},
				{Name: "image", Type: "string"},
				{Name: "storage_class", Type: "string"},
				{Name: "service_account", Type: "string"},
				{Name: "rtk_enabled", Type: "boolean"},
				// Pod resource overrides.
				{Name: "cpu_request", Type: "string"},
				{Name: "cpu_limit", Type: "string"},
				{Name: "memory_request", Type: "string"},
				{Name: "memory_limit", Type: "string"},
				// Per-project secrets, plain env vars, and multi-repo definitions.
				{Name: "secrets", Type: "json"},
				{Name: "env", Type: "json"},
				{Name: "env_json", Type: "json"},
				{Name: "repos", Type: "json"},
				// Slack channel ID for channel-to-project resolution (/spawn).
				{Name: "slack_channel", Type: "string"},
				// Auto-assignment control: "true" (default) or "false".
				// Inherited by agents unless overridden on the agent bead.
				{Name: "auto_assign", Type: "string"},
				// Prewarmed pool configuration (JSON):
				// {"enabled":bool, "mode":"job"|"crew", "role":"thread", "min_size":int, "max_size":int}
				{Name: "prewarmed_pool", Type: "json"},
				// Nudge prompt templates (JSON): role-keyed map of prompt strings.
				// Keys: "thread", "adhoc", "default", "prewarmed".
				// Supports {{.Project}}, {{.ProjectHint}}, {{.TaskHint}}, {{.MonorepoHint}}, {{.BoatPrompt}} placeholders.
				{Name: "nudge_prompts", Type: "json"},
			},
		},

		"type:task": TypeConfig{
			Kind: "issue",
			Fields: []FieldDef{
				{Name: "jira_key", Type: "string"},
				{Name: "jira_project", Type: "string"},
				{Name: "jira_type", Type: "string"},
				{Name: "jira_status", Type: "string"},
				{Name: "jira_url", Type: "string"},
				{Name: "jira_epic", Type: "string"},
				{Name: "jira_reporter", Type: "string"},
				{Name: "jira_xlinks", Type: "string"},
				{Name: "mr_url", Type: "string"},
				{Name: "jira_attachment_count", Type: "string"},
				{Name: "jira_has_images", Type: "string"},
				{Name: "jira_has_video", Type: "string"},
				// Slack attachment fields — set by slack-bridge when files are shared.
				{Name: "slack_attachment_count", Type: "string"},
				{Name: "slack_has_images", Type: "string"},
				// GitLab bridge fields — set by gitlab-bridge when MR events are detected.
				{Name: "mr_merged", Type: "string"},
				{Name: "mr_state", Type: "enum", Values: []string{"opened", "closed", "merged", "locked"}},
				{Name: "mr_pipeline_status", Type: "string"},
				{Name: "mr_pipeline_url", Type: "string"},
				{Name: "mr_approved", Type: "string"},
				{Name: "mr_approvers", Type: "string"},
				{Name: "mr_has_review_comments", Type: "string"},
				{Name: "gitlab_mr_iid", Type: "string"},
				{Name: "gitlab_project_id", Type: "string"},
			},
		},

		// --- formulas & molecules ----------------------------------------
		//
		// Formulas are reusable work definitions with variables and steps.
		// Applying a formula creates a molecule (an epic with child issues,
		// variable-substituted).

		"type:formula": TypeConfig{
			Kind: "data",
			Fields: []FieldDef{
				// Variable definitions: [{name, description, required, default, type, enum}]
				{Name: "vars", Type: "json"},
				// Step definitions: [{id, title, type, description, depends_on, labels, priority, condition, assignee}]
				{Name: "steps", Type: "json"},
				// Default agent to assign molecules to when this formula is poured.
				{Name: "assigned_agent", Type: "string"},
			},
		},
		"type:molecule": TypeConfig{
			Kind: "issue",
			Fields: []FieldDef{
				// ID of the formula bead this molecule was created from.
				{Name: "formula_id", Type: "string"},
				// Variable values applied during instantiation.
				{Name: "applied_vars", Type: "json"},
			},
		},
		"type:report": TypeConfig{
			Kind: "data",
			Fields: []FieldDef{
				{Name: "decision_id", Type: "string"},
				{Name: "report_type", Type: "string"},
				{Name: "content", Type: "string"},
				{Name: "format", Type: "string"},
			},
		},

		// Scheduled agents: cron-driven autonomous tasks.
		"type:schedule": TypeConfig{
			Kind: "config",
			Fields: []FieldDef{
				{Name: "cron", Type: "string", Required: true},
				{Name: "project", Type: "string", Required: true},
				{Name: "role", Type: "string"},
				{Name: "prompt", Type: "string", Required: true},
				{Name: "enabled", Type: "boolean"},
				{Name: "timezone", Type: "string"},
				{Name: "last_run", Type: "string"},
				{Name: "last_agent_id", Type: "string"},
			},
		},

		// --- views -----------------------------------------------------------
		//
		// Core views used by the controller and by context formulas.

		"view:agents:active": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"agent"},
			},
			Sort:    "updated_at",
			Columns: []string{"id", "title", "status", "assignee", "fields"},
		},
		"view:agents:jobs": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"agent"},
				Labels: []string{"role:job"},
			},
			Sort:    "updated_at",
			Columns: []string{"id", "title", "status", "fields"},
		},
		"view:agents:crew": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"agent"},
				Labels: []string{"role:crew"},
			},
			Sort:    "updated_at",
			Columns: []string{"id", "title", "status", "assignee", "fields"},
		},
		"view:agents:reviewers": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"agent"},
				Labels: []string{"role:reviewer"},
			},
			Sort:    "updated_at",
			Columns: []string{"id", "title", "status", "assignee", "fields"},
		},
		"view:decisions:pending": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"decision"},
			},
			Sort:    "updated_at",
			Columns: []string{"id", "title", "status", "labels"},
		},
		"view:mail:inbox": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"mail"},
			},
			Sort:    "updated_at",
			Columns: []string{"id", "title", "status", "assignee", "labels"},
		},
		"view:projects": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"project"},
			},
			Sort:    "title",
			Columns: []string{"id", "title", "labels"},
		},
		"view:schedules": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress"},
				Type:   []string{"schedule"},
			},
			Sort:    "title",
			Columns: []string{"id", "title", "fields"},
		},

		// --- contexts --------------------------------------------------------
		//
		// Rendered by `kd context <name>`.  Each role gets a tailored
		// dashboard that doubles as its session-start priming context.

		// Captain: fleet coordinator — needs the full picture.
		"context:captain": ContextConfig{
			Sections: []ContextSection{
				{Header: "## Active Agents", View: "agents:active", Format: "table"},
				{Header: "## Active Jobs", View: "agents:jobs", Format: "list", Fields: []string{"id", "title", "status"}},
				{Header: "## Schedules", View: "schedules", Format: "list", Fields: []string{"id", "title", "fields"}},
				{Header: "## Projects", View: "projects", Format: "table"},
				{Header: "## Pending Decisions", View: "decisions:pending", Format: "list", Fields: []string{"id", "title", "status"}},
				{Header: "## Inbox", View: "mail:inbox", Format: "list", Fields: []string{"id", "title", "assignee"}},
			},
		},
		// Crew: persistent worker — inbox and blockers only.
		// Hooked work (if any) is surfaced by prime.sh, not here.
		"context:crew": ContextConfig{
			Sections: []ContextSection{
				{Header: "## Inbox", View: "mail:inbox", Format: "list", Fields: []string{"id", "title", "assignee"}},
				{Header: "## Pending Decisions", View: "decisions:pending", Format: "list", Fields: []string{"id", "title", "status"}},
			},
		},
		// Reviewer: MR shepherd — same base dashboard as crew; the
		// advice beads define the actual review workflow.
		"context:reviewer": ContextConfig{
			Sections: []ContextSection{
				{Header: "## Inbox", View: "mail:inbox", Format: "list", Fields: []string{"id", "title", "assignee"}},
				{Header: "## Pending Decisions", View: "decisions:pending", Format: "list", Fields: []string{"id", "title", "status"}},
			},
		},
		// No context:job — a job's entire context is the agent bead
		// itself (title, description, dependencies), shown by prime.sh.

		// --- JIRA views and context ----------------------------------------

		"view:jira:pending": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"task"},
				Labels: []string{"source:jira"},
			},
			Sort:    "priority",
			Columns: []string{"id", "title", "status", "assignee", "fields"},
		},

		// JIRA dispatcher: sees pending JIRA tasks, decisions, and inbox.
		"context:jira-dispatcher": ContextConfig{
			Sections: []ContextSection{
				{Header: "## Pending JIRA Tasks", View: "jira:pending", Format: "list", Fields: []string{"id", "title", "status"}},
				{Header: "## Pending Decisions", View: "decisions:pending", Format: "list", Fields: []string{"id", "title", "status"}},
				{Header: "## Inbox", View: "mail:inbox", Format: "list", Fields: []string{"id", "title", "assignee"}},
			},
		},

		// --- nudge prompts (global defaults) -----------------------------------
		//
		// Seeded as a global config bead so they can be overridden per-project
		// or per-role via nudge-prompts config beads. The gb nudge-prompt
		// command resolves these via the standard config bead merge chain.

		"config:nudge-prompts:global": map[string]string{
			"thread":    "You are a thread-bound agent spawned from a Slack conversation. Your thread context is in your agent bead description. CRITICAL RULES: (1) Do NOT exit prematurely — if you hit an error, debug it; if you are blocked, ask a clarifying question via `gb squawk '<question>'`. Giving up silently is the worst outcome. (2) Create a tracking bead: `kd create '<short title>' --project {{.Project}}` then `kd claim <id>`. (3) Post progress updates to the thread via `gb squawk '<update>'` at key milestones. (4) When done, summarize results via `gb squawk`, push to a feature branch (never main), open a PR if code changed, close your bead, then `gb done`.{{.MonorepoHint}}{{.ProjectHint}} Now read the thread context in your description and begin working.",
			"adhoc":     "You have been spawned with a task.{{.TaskHint}} Before starting: (1) Run `gb news` to check what teammates are working on — do not duplicate in-progress work. (2) When done, deliver via a feature branch + PR (never push to main).{{.MonorepoHint}} Here is your task: {{.BoatPrompt}}",
			"default":   "Check `gb ready` for your workflow steps and begin working.{{.ProjectHint}}{{.TaskHint}}{{.MonorepoHint}} IMPORTANT: (1) Run `gb news` first to see what your teammates are already working on — do not duplicate in-progress work. (2) Run `kd claim <id>` BEFORE starting any task — this atomically marks it in_progress so no other agent picks it up simultaneously.",
			"prewarmed": "You are a **prewarmed agent** waiting in the idle pool for work assignment. **Do NOT** seek work, run `gb ready`, or create beads. When a Slack thread mention or operator assigns you, the pool manager will inject a nudge with your task description. Wait for a nudge message.",
		},

		// --- claude-instructions (workflow context) -------------------------
		//
		// Global default for the crew/captain role.  Section keys match
		// outputConfigSections in gb prime so the config-bead path and
		// the hardcoded fallback render identically.

		"config:claude-instructions:global": map[string]string{
			"prime_header": "# Beads Workflow Context\n\n" +
				"> **Context Recovery**: Run `gb prime` after compaction, clear, or new session\n" +
				"> Hooks auto-call this when configured\n",

			"session_close": "# SESSION CLOSE PROTOCOL\n\n" +
				"**CRITICAL**: Before saying \"done\" or \"complete\", you MUST run this checklist:\n\n" +
				"```\n" +
				"[ ] 1. git status              (check what changed)\n" +
				"[ ] 2. git add <files>         (stage code changes)\n" +
				"[ ] 3. git commit -m \"...\"     (commit code)\n" +
				"[ ] 4. git push                (push to remote)\n" +
				"```\n\n" +
				"**NEVER skip this.** Work is not done until pushed.\n",

			"core_rules": "## Core Rules\n" +
				"- **Default**: Use kd for CRUD (`kd create`, `kd show`, `kd close`), gb for orchestration (`gb ready`, `gb decision`, `gb yield`)\n" +
				"- **Prohibited**: Do NOT use TodoWrite, TaskCreate, or markdown files for task tracking\n" +
				"- **Workflow**: Create kbeads issue BEFORE writing code, `kd claim <id>` when starting\n" +
				"- Persistence you don't need beats lost context\n" +
				"- Git workflow: beads auto-synced by Postgres backend\n" +
				"- Session management: check `gb ready` for available work\n",

			"commands": "## Essential Commands\n\n" +
				"### Finding Work\n" +
				"- `gb ready` - Show issues ready to work (no blockers)\n" +
				"- `gb news` - Show in-progress work by others (check for conflicts before starting)\n" +
				"- `kd list --status=open` - All open issues\n" +
				"- `kd list --status=in_progress` - Your active work\n" +
				"- `kd show <id>` - Detailed issue view with dependencies\n\n" +
				"### Creating & Updating\n" +
				"- `kd create \"...\" --type=task|bug|feature --priority=2` - New issue (title is positional)\n" +
				"  - Priority: 0-4 or P0-P4 (0=critical, 2=medium, 4=backlog). NOT \"high\"/\"medium\"/\"low\"\n" +
				"- `kd claim <id>` - Claim work (sets assignee + status=in_progress)\n" +
				"- `kd update <id> --assignee=username` - Assign to someone\n" +
				"- `kd close <id>` - Mark complete\n" +
				"- **WARNING**: Do NOT use `kd edit` - it opens $EDITOR (vim/nano) which blocks agents\n\n" +
				"### Dependencies & Blocking\n" +
				"- `kd dep add <issue> <depends-on>` - Add dependency\n" +
				"- `kd dep list <id>` - List dependencies of a bead\n" +
				"- `kd show <id>` - See what's blocking/blocked by this issue (shows deps inline)\n\n" +
				"### Project Health\n" +
				"- `kd list --status=open | wc -l` - Count open issues\n" +
				"- `gb gate status` - Show session gate status (decision, commit-push, etc.)\n",

			"workflows": "## Common Workflows\n\n" +
				"**Starting work:**\n" +
				"```bash\n" +
				"gb news            # Check what others are working on (avoid conflicts)\n" +
				"gb ready           # Find available work\n" +
				"kd show <id>       # Review issue details\n" +
				"kd claim <id>      # Claim it (sets assignee + in_progress)\n" +
				"```\n\n" +
				"**Completing work:**\n" +
				"```bash\n" +
				"kd close <id>              # Close completed issue\n" +
				"git add <files> && git commit -m \"...\" && git push\n" +
				"```\n\n" +
				"**Creating dependent work:**\n" +
				"```bash\n" +
				"kd create \"Implement feature X\" --type=feature\n" +
				"kd create \"Write tests for X\" --type=task\n" +
				"kd dep add <tests-id> <feature-id>  # Tests depend on Feature\n" +
				"```\n",

			"decisions": "## Human Decisions\n\n" +
				"When you need human input (approval, choices, clarification), create a decision checkpoint.\n" +
				"Every option MUST declare an `artifact_type` — what you will deliver if that option is chosen.\n\n" +
				"```bash\n" +
				"gb decision create --no-wait \\\n" +
				"  --prompt=\"Completed auth refactor. Tests pass. Two options for session handling:\" \\\n" +
				"  --options='[\n" +
				"    {\"id\":\"jwt\",\"short\":\"Use JWT\",\"label\":\"Stateless JWT tokens with refresh rotation\",\"artifact_type\":\"plan\"},\n" +
				"    {\"id\":\"session\",\"short\":\"Use sessions\",\"label\":\"Server-side sessions with Redis store\",\"artifact_type\":\"plan\"},\n" +
				"    {\"id\":\"skip\",\"short\":\"Defer\",\"label\":\"Keep current impl, file a tech debt issue\",\"artifact_type\":\"bug\"}\n" +
				"  ]'\n" +
				"gb yield  # blocks until human responds\n" +
				"```\n\n" +
				"**Artifact types:** `report` (work summary), `plan` (implementation plan), `checklist` (verification steps), `diff-summary` (code changes), `epic` (feature breakdown), `bug` (bug report)\n\n" +
				"If the chosen option requires an artifact, `gb yield` will tell you — submit it with:\n" +
				"`gb decision report <decision-id> --content '...'`\n\n" +
				"**Decision commands:**\n" +
				"- `gb decision create --prompt=\"...\" --options='[...]'` - Create decision (`--no-wait` to not block)\n" +
				"- `gb yield` - Wait for human response\n" +
				"- `gb decision report <id> --content '...'` - Submit required artifact\n" +
				"- `gb decision list` - Show pending decisions\n" +
				"- `gb decision show <id>` - Decision details\n",

			"session_resumption": "## Session Resumption\n\n" +
				"Two complementary mechanisms restore context after interruptions:\n\n" +
				"**Conversation resume** (`coop --resume`):\n" +
				"- Managed **automatically** by the entrypoint on pod restart\n" +
				"- Restores the previous Claude conversation history\n" +
				"- No agent action required — the entrypoint handles it\n\n" +
				"**Context recovery** (`gb prime`):\n" +
				"- Run by agents after compaction, `/clear`, or a new session\n" +
				"- Injects fresh workflow context: assignment, roster, advice, auto-assign\n" +
				"- Hooks auto-call this on SessionStart — run manually if context is stale\n",

			"lifecycle": "## Agents Are Ephemeral\n\n" +
				"Agents are ephemeral by default: start up, do the work, then despawn. Do NOT linger or idle-loop waiting for more work.\n\n" +
				"**Lifecycle:**\n" +
				"1. Start up → check for claimed in-progress work (resume it) or find new work via `gb ready`\n" +
				"2. Claim a task → do the work thoroughly (commit, push, close bead)\n" +
				"3. Call `gb done` to despawn cleanly\n\n" +
				"```bash\n" +
				"kd close <bead-id>     # close completed work\n" +
				"gb done                # signal entrypoint not to restart this pod\n" +
				"```\n\n" +
				"**Do NOT** just exit without calling `gb done` — exiting alone triggers an automatic restart.\n" +
				"If there is more work in the ready queue, you MAY claim another task before stopping.\n",

			"stop_gate": "## Stop Gate Contract\n\n" +
				"The **decision gate** is the Slack operator's re-entry handle.\n\n" +
				"**Rules:**\n" +
				"- **NEVER** use `gb gate mark decision` to satisfy the gate manually — this is blocked for agents (requires `--force`, operator-only).\n" +
				"- When you are **blocked mid-task** and need human input, create a decision and yield.\n" +
				"- When you have **finished all work**, just call `gb done` — no decision checkpoint needed.\n" +
				"- The only legitimate ways to clear the gate are:\n" +
				"  1. `gb done` — polite despawn when you have finished your work (preferred)\n" +
				"  2. `gb yield` — blocks until a human resolves your decision bead (use when genuinely blocked)\n",

			// stop_gate_blocked is materialized to ~/.claude/stop-gate-text.md
			// by gb setup claude, and injected by the stop-gate hook when the
			// agent tries to stop without satisfying the decision gate.
			"stop_gate_blocked": "<system-reminder>\n" +
				"STOP BLOCKED — decision gate unsatisfied.\n\n" +
				"You CANNOT stop without either creating a decision checkpoint OR calling `gb done`.\n\n" +
				"## If work is DONE\n\n" +
				"```bash\n" +
				"kd close <bead-id>        # close completed beads\n" +
				"gb done                   # despawn cleanly (bypasses gate)\n" +
				"```\n\n" +
				"## If BLOCKED and need human input\n\n" +
				"```bash\n" +
				"gb decision create --no-wait \\\n" +
				"  --prompt=\"Did X. Blocked on Y. Recommending option A because...\" \\\n" +
				"  --options='[\n" +
				"    {\"id\":\"continue\",\"short\":\"Continue work\",\"label\":\"Finish the remaining implementation\",\"artifact_type\":\"report\"},\n" +
				"    {\"id\":\"rethink\",\"short\":\"Change approach\",\"label\":\"Switch to alternative design\",\"artifact_type\":\"plan\"}\n" +
				"  ]'\n" +
				"gb yield\n" +
				"# IMPORTANT: after yield returns, CONTINUE WORKING on the decision outcome.\n" +
				"# Do NOT stop or create another decision. Act on the response.\n" +
				"```\n" +
				"</system-reminder>",
		},

		// Thread agent role override — simplified commands and interactive lifecycle.
		// Merged on top of global defaults by ResolveConfigBeads when role=thread.
		"config:claude-instructions:role:thread": map[string]string{
			"commands": "## Essential Commands\n\n" +
				"- `kd show <id>` - View bead details\n" +
				"- `kd create \"...\" --type=task|bug|feature --priority=2` - Create tracking bead for work\n" +
				"- `kd claim <id>` - Claim work (sets assignee + status=in_progress)\n" +
				"- `kd close <id>` - Mark bead complete\n" +
				"- `gb done` - Despawn when the user dismisses you\n\n" +
				"## Thread Agent Workflow\n\n" +
				"You are bound to a Slack thread. Respond to messages as they arrive.\n\n" +
				"- If the user asks a question, answer it directly\n" +
				"- If the user requests code changes, create a tracking bead, do the work, commit, push, and close the bead\n" +
				"- After responding, **wait for follow-ups** — do NOT call `gb done`\n",

			"workflows": "", // Thread agents don't need Finding Work workflows.

			"lifecycle": "## Thread Agent — Interactive Lifecycle\n\n" +
				"You are bound to a **Slack thread**. You exist to help the conversation\n" +
				"participants with questions, code changes, and investigations.\n\n" +
				"**Lifecycle:**\n" +
				"1. Respond to the current message thoroughly\n" +
				"2. After responding, **stay alive** — do NOT call `gb done`\n" +
				"3. Wait for follow-up messages (they arrive automatically as nudges)\n" +
				"4. Only call `gb done` when the user **explicitly dismisses you**\n" +
				"   (e.g., \"thanks, that's all\", \"you can stop now\", \"dismissed\")\n\n" +
				"**Important rules:**\n" +
				"- **Do NOT** call `gb done` after answering a question — the user may have follow-ups\n" +
				"- **Do NOT** look for work via `gb ready` — you only respond to thread messages\n" +
				"- If you complete code changes, commit and push before waiting for the next message\n" +
				"- If you need human input mid-task, use `gb decision create` + `gb yield`\n",
		},

		// Polecat (single-task) role override — minimal commands and single-task lifecycle.
		"config:claude-instructions:role:polecat": map[string]string{
			"commands": "## Essential Commands\n\n" +
				"- `kd show <id>` - View your assigned task details\n" +
				"- `kd close <id>` - Mark your task complete\n" +
				"- `gb done` - Despawn after completing your task\n",

			"workflows": "", // Polecats don't need Finding Work workflows.

			"lifecycle": "## Single-Task Lifecycle\n\n" +
				"You are a **single-task ephemeral agent**. Your lifecycle is simple:\n\n" +
				"1. Check your pre-assigned task (`BOAT_TASK_ID` or `kd list --status=in_progress`)\n" +
				"2. Do the work thoroughly (commit, push)\n" +
				"3. Close the bead: `kd close <bead-id>`\n" +
				"4. Despawn: `gb done`\n\n" +
				"```bash\n" +
				"kd show <bead-id>          # review your assigned task\n" +
				"# ... do the work ...\n" +
				"kd close <bead-id>         # close completed work\n" +
				"gb done                    # despawn — do NOT look for more work\n" +
				"```\n\n" +
				"**Do NOT** run `gb ready` or look for additional tasks. You exist for one task only.\n" +
				"**Do NOT** just exit without calling `gb done` — exiting alone triggers an automatic restart.\n",
		},
	}
}

// EnsureConfigs upserts all gasboat-managed type, view, and context configs
// into the beads daemon.  It is safe to call on every startup; the daemon
// treats SetConfig as an upsert.
func EnsureConfigs(ctx context.Context, setter ConfigSetter, logger *slog.Logger) error {
	for key, value := range configs() {
		valueJSON, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshalling config %s: %w", key, err)
		}

		if err := setter.SetConfig(ctx, key, valueJSON); err != nil {
			return fmt.Errorf("setting config %s: %w", key, err)
		}

		logger.Info("ensured beads config", "key", key)
	}

	return nil
}
