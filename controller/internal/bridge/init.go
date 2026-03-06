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

		// --- templates & bundles -----------------------------------------
		//
		// Templates are reusable work definitions (ported from beads formulas).
		// A template defines variables and steps; applying a template creates
		// a bundle (an epic with child issues, variable-substituted).

		"type:template": TypeConfig{
			Kind: "data",
			Fields: []FieldDef{
				// Variable definitions: [{name, description, required, default, type, enum}]
				{Name: "vars", Type: "json"},
				// Step definitions: [{id, title, type, description, depends_on, labels, priority, condition, assignee}]
				{Name: "steps", Type: "json"},
			},
		},
		"type:bundle": TypeConfig{
			Kind: "issue",
			Fields: []FieldDef{
				// ID of the template bead this bundle was created from.
				{Name: "template_id", Type: "string"},
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

		// --- views -----------------------------------------------------------
		//
		// Core views used by the controller and by context templates.

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

		// --- contexts --------------------------------------------------------
		//
		// Rendered by `kd context <name>`.  Each role gets a tailored
		// dashboard that doubles as its session-start priming context.

		// Captain: fleet coordinator — needs the full picture.
		"context:captain": ContextConfig{
			Sections: []ContextSection{
				{Header: "## Active Agents", View: "agents:active", Format: "table"},
				{Header: "## Active Jobs", View: "agents:jobs", Format: "list", Fields: []string{"id", "title", "status"}},
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
