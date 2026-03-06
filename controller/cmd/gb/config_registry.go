package main

import "encoding/json"

// MergeStrategy defines how config layers are combined when multiple
// beads match an agent's context (e.g. global + role-specific).
type MergeStrategy int

const (
	// MergeOverride uses shallow key override: later (more specific) layers
	// replace earlier values for the same top-level key.
	// Used for: claude-settings, claude-mcp, type, context, view.
	MergeOverride MergeStrategy = iota

	// MergeConcat concatenates array values within the "hooks" key rather
	// than replacing them. Non-hook keys still use override semantics.
	// Used for: claude-hooks.
	MergeConcat
)

// ConfigCategory describes a known config bead category: its name,
// how layers merge, and where the resolved output is written.
type ConfigCategory struct {
	// Name is the config category identifier, used as the bead title
	// and the config KV namespace. E.g. "claude-settings".
	Name string

	// Strategy controls how multiple matching layers are combined.
	Strategy MergeStrategy

	// Description is a human-readable summary of this category's purpose.
	Description string
}

// configCategories is the authoritative list of config bead categories
// that gb knows how to resolve and merge.
//
// Output paths are not stored here because they depend on runtime context
// (workspace dir, home dir) and are handled by the setup command.
var configCategories = []ConfigCategory{
	{
		Name:        "claude-settings",
		Strategy:    MergeOverride,
		Description: "User-level Claude Code settings (model, permissions, plugins, thinking mode)",
	},
	{
		Name:        "claude-hooks",
		Strategy:    MergeConcat,
		Description: "Claude Code hooks (SessionStart, PreCompact, UserPromptSubmit, Stop)",
	},
	{
		Name:        "claude-mcp",
		Strategy:    MergeOverride,
		Description: "MCP server configuration (mcpServers key/value pairs)",
	},
	{
		Name:     "type",
		Strategy: MergeOverride,
		Description: "Bead type system definitions (task, agent, epic schemas)",
	},
	{
		Name:        "context",
		Strategy:    MergeOverride,
		Description: "Contextual data for agent sessions",
	},
	{
		Name:        "view",
		Strategy:    MergeOverride,
		Description: "View definitions for bead listing and display",
	},
	{
		Name:        "claude-instructions",
		Strategy:    MergeOverride,
		Description: "Agent instruction text (workflow context, lifecycle, stop gate) — consumed by gb prime",
	},
	{
		Name:        "wrapup-config",
		Strategy:    MergeOverride,
		Description: "Wrap-up message requirements for gb stop (required fields, enforcement level, custom fields)",
	},
	{
		Name:        "nudge-prompts",
		Strategy:    MergeOverride,
		Description: "Nudge prompt templates for agent startup (thread, adhoc, default, prewarmed)",
	},
}

// configCategoryMap provides O(1) lookup by category name.
var configCategoryMap map[string]*ConfigCategory

func init() {
	configCategoryMap = make(map[string]*ConfigCategory, len(configCategories))
	for i := range configCategories {
		configCategoryMap[configCategories[i].Name] = &configCategories[i]
	}
}

// ConfigCategoryNames returns the names of all known config categories.
// This replaces the old configNamespaces variable.
func ConfigCategoryNames() []string {
	names := make([]string, len(configCategories))
	for i, c := range configCategories {
		names[i] = c.Name
	}
	return names
}

// LookupCategory returns the ConfigCategory for a given name, or nil.
func LookupCategory(name string) *ConfigCategory {
	return configCategoryMap[name]
}

// MergeLayers combines multiple JSON config layers according to the
// given strategy. Layers are applied in order (first = lowest priority,
// last = highest priority / most specific).
func MergeLayers(strategy MergeStrategy, layers []json.RawMessage) map[string]any {
	switch strategy {
	case MergeConcat:
		return mergeHookLayers(layers)
	default:
		return mergeSimpleLayers(layers)
	}
}
