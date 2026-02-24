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
				{Name: "role", Type: "enum", Values: []string{"captain", "mate", "deckhand"}},
				{Name: "agent", Type: "string"},
				{Name: "model", Type: "string"},
				// Agent lifecycle state written back by the controller.
				{Name: "agent_state", Type: "enum", Values: []string{"spawning", "working", "done", "failed"}},
				// Pod lifecycle state written back by the controller.
				{Name: "pod_phase", Type: "enum", Values: []string{"pending", "running", "succeeded", "failed"}},
				{Name: "pod_name", Type: "string"},
				{Name: "pod_namespace", Type: "string"},
				{Name: "pod_ready", Type: "boolean"},
				{Name: "coop_url", Type: "string"},
				{Name: "coop_token", Type: "string"},
			},
		},
		"type:mail": TypeConfig{
			Kind: "data",
		},
		"type:decision": TypeConfig{
			Kind: "data",
			Fields: []FieldDef{
				{Name: "question", Type: "string"},
				{Name: "prompt", Type: "string"},
				{Name: "options", Type: "json", Required: true},
				{Name: "chosen", Type: "string"},
				{Name: "rationale", Type: "string"},
				{Name: "session", Type: "string"},
				{Name: "context", Type: "string"},
				{Name: "requested_by", Type: "string"},
				{Name: "requesting_agent_bead_id", Type: "string"},
				{Name: "responded_by", Type: "string"},
				{Name: "responded_at", Type: "string"},
				{Name: "response_text", Type: "string"},
			},
		},
		// Advice beads are text guidance surfaced at prime time.
		// Targeting uses standard bead labels:
		//   global          — all agents
		//   role:<role>     — captain, crew, job
		//   project:<name>  — agents in that project
		//   agent:<name>    — specific agent
		//
		// Create:  bd create "Always run tests" --type advice -l role:mate
		// Query:   bd view advice
		"type:advice": TypeConfig{
			Kind: "config",
		},
		"type:project": TypeConfig{
			Kind: "config",
			Fields: []FieldDef{
				{Name: "prefix", Type: "string"},
				{Name: "git_url", Type: "string"},
				{Name: "default_branch", Type: "string"},
				{Name: "image", Type: "string"},
				{Name: "storage_class", Type: "string"},
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
		"view:agents:deckhands": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"agent"},
				Labels: []string{"role:deckhand"},
			},
			Sort:    "updated_at",
			Columns: []string{"id", "title", "status", "fields"},
		},
		"view:agents:mates": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"agent"},
				Labels: []string{"role:mate"},
			},
			Sort:    "updated_at",
			Columns: []string{"id", "title", "status", "assignee", "fields"},
		},
		"view:advice": ViewConfig{
			Filter: ViewFilter{
				Status: []string{"open", "in_progress", "blocked", "deferred"},
				Type:   []string{"advice"},
			},
			Sort:    "priority",
			Columns: []string{"id", "title", "labels"},
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
				{Header: "## Deckhands", View: "agents:deckhands", Format: "list", Fields: []string{"id", "title", "status"}},
				{Header: "## Projects", View: "projects", Format: "table"},
				{Header: "## Pending Decisions", View: "decisions:pending", Format: "list", Fields: []string{"id", "title", "status"}},
				{Header: "## Inbox", View: "mail:inbox", Format: "list", Fields: []string{"id", "title", "assignee"}},
				{Header: "## Advice", View: "advice", Format: "list", Fields: []string{"id", "title", "labels"}},
			},
		},
		// Mate: persistent worker — inbox, blockers, and advice.
		"context:mate": ContextConfig{
			Sections: []ContextSection{
				{Header: "## Inbox", View: "mail:inbox", Format: "list", Fields: []string{"id", "title", "assignee"}},
				{Header: "## Pending Decisions", View: "decisions:pending", Format: "list", Fields: []string{"id", "title", "status"}},
				{Header: "## Advice", View: "advice", Format: "list", Fields: []string{"id", "title", "labels"}},
			},
		},
		// Deckhand: advice only — the assignment comes from the agent bead
		// itself (title, description, dependencies), shown by prime.sh.
		"context:deckhand": ContextConfig{
			Sections: []ContextSection{
				{Header: "## Advice", View: "advice", Format: "list", Fields: []string{"id", "title", "labels"}},
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
