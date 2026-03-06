package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed claudemd_default.md
var defaultClaudeMDTemplate string

var setupCmd = &cobra.Command{
	Use:     "setup",
	Short:   "Setup commands for agent environment",
	GroupID: "session",
}

// gbHookPrefixes are command prefixes that indicate gb hook installations.
// Both the legacy gb bus emit style and the newer gb hook style are recognised.
var gbHookPrefixes = []string{"gb hook ", "gb bus emit --hook="}

var setupClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Materialize Claude Code settings and hooks from config beads",
	Long: `Fetches claude-settings, claude-mcp, and claude-hooks config beads
from the daemon, merges them by specificity (global → role), and writes:

  ~/.claude/settings.json           — user-level (permissions, plugins)
  {workspace}/.mcp.json             — project-level MCP server config
  {workspace}/.claude/settings.json — workspace-level (hooks)

Config bead keys (checked in order, later overrides earlier):
  claude-settings:global — base user-level settings for all agents
  claude-settings:<role> — role-specific settings overrides
  claude-mcp:global      — base MCP server config for all agents
  claude-mcp:<role>      — role-specific MCP server overrides
  claude-hooks:global    — base hooks for all agents
  claude-hooks:<role>    — role-specific hook overrides

Settings and MCP merge use shallow key override: a role-level key
completely replaces the global value for that key.

Hooks merge uses array concatenation: role hooks are appended to (not
replacing) global hooks for each hook type.

MCP: if no claude-mcp beads exist, no .mcp.json is written (existing
repo .mcp.json files are unaffected). When beads exist, new servers fill
gaps without overwriting existing entries.

Flags:
  --defaults   Install hardcoded settings and hooks (no server needed)
  --check      Verify hooks are installed, exit 1 if missing
  --remove     Remove gb hooks from settings.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		workspace, _ := cmd.Flags().GetString("workspace")
		if workspace == "" {
			workspace, _ = os.Getwd()
		}

		role, _ := cmd.Flags().GetString("role")
		if role == "" {
			role = os.Getenv("BOAT_ROLE")
		}
		if role == "" {
			role = os.Getenv("KD_ROLE")
		}

		check, _ := cmd.Flags().GetBool("check")
		if check {
			return runSetupClaudeCheck(workspace)
		}

		remove, _ := cmd.Flags().GetBool("remove")
		if remove {
			return runSetupClaudeRemove(workspace)
		}

		defaults, _ := cmd.Flags().GetBool("defaults")
		if defaults {
			return runSetupClaudeDefaults(workspace, role)
		}

		return runSetupClaude(cmd.Context(), workspace, role)
	},
}

func init() {
	setupClaudeCmd.Flags().String("workspace", os.Getenv("KD_WORKSPACE"), "workspace directory")
	setupClaudeCmd.Flags().String("role", "", "agent role (default: $BOAT_ROLE or $KD_ROLE)")
	setupClaudeCmd.Flags().Bool("defaults", false, "install hardcoded default hooks (no server needed)")
	setupClaudeCmd.Flags().Bool("check", false, "verify hooks are installed (exit 1 if missing)")
	setupClaudeCmd.Flags().Bool("remove", false, "remove gb hooks from settings.json")
	setupCmd.AddCommand(setupClaudeCmd)
}

func runSetupClaudeDefaults(workspace, role string) error {
	if err := writeUserSettings(defaultUserSettings()); err != nil {
		return fmt.Errorf("writing user settings: %w", err)
	}

	if mcpConfig := defaultMCPConfig(); mcpConfig != nil {
		if err := writeMCPConfig(workspace, mcpConfig); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: failed to write default MCP config: %v\n", err)
		}
	}

	settings := defaultHookSettings()
	appendRTKHooks(settings)
	installRTKContext()

	outDir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating .claude dir: %w", err)
	}

	outPath := filepath.Join(outDir, "settings.json")
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(outPath, data, 0600); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote default hooks to %s\n", outPath)

	claudeMDPath := filepath.Join(workspace, "CLAUDE.md")
	if _, err := os.Stat(claudeMDPath); os.IsNotExist(err) {
		content := defaultClaudeMD(role)
		if err := os.WriteFile(claudeMDPath, []byte(content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: failed to write fallback CLAUDE.md: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[setup] wrote fallback CLAUDE.md to %s\n", claudeMDPath)
		}
	}

	return nil
}

func runSetupClaudeCheck(workspace string) error {
	settingsPath := filepath.Join(workspace, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("hooks not installed: %s not found", settingsPath)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("hooks not installed: invalid JSON in %s", settingsPath)
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return fmt.Errorf("hooks not installed: no hooks key in %s", settingsPath)
	}

	required := []string{"SessionStart", "Stop"}
	for _, ht := range required {
		if !hookContainsGB(hooks, ht) {
			return fmt.Errorf("hooks not installed: missing %s hook with gb command", ht)
		}
	}

	fmt.Fprintf(os.Stderr, "hooks OK: gb hooks installed in %s\n", settingsPath)
	return nil
}

// isGBHookCommand returns true if the command string is a gb hook command.
func isGBHookCommand(cmd string) bool {
	for _, prefix := range gbHookPrefixes {
		if strings.HasPrefix(cmd, prefix) {
			return true
		}
	}
	return false
}

func hookContainsGB(hooks map[string]any, hookType string) bool {
	arr, ok := hooks[hookType].([]any)
	if !ok {
		return false
	}
	for _, entry := range arr {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		innerHooks, ok := entryMap["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range innerHooks {
			hMap, ok := h.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := hMap["command"].(string)
			if isGBHookCommand(cmd) {
				return true
			}
		}
	}
	return false
}

func runSetupClaudeRemove(workspace string) error {
	settingsPath := filepath.Join(workspace, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("no settings file: %w", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		fmt.Fprintf(os.Stderr, "[setup] no hooks to remove\n")
		return nil
	}

	removed := 0
	for hookType, val := range hooks {
		arr, ok := val.([]any)
		if !ok {
			continue
		}
		var filtered []any
		for _, entry := range arr {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				filtered = append(filtered, entry)
				continue
			}
			innerHooks, ok := entryMap["hooks"].([]any)
			if !ok {
				filtered = append(filtered, entry)
				continue
			}
			hasGB := false
			for _, h := range innerHooks {
				hMap, ok := h.(map[string]any)
				if !ok {
					continue
				}
				cmd, _ := hMap["command"].(string)
				if isGBHookCommand(cmd) {
					hasGB = true
					break
				}
			}
			if !hasGB {
				filtered = append(filtered, entry)
			} else {
				removed++
			}
		}
		if len(filtered) == 0 {
			delete(hooks, hookType)
		} else {
			hooks[hookType] = filtered
		}
	}

	if removed == 0 {
		fmt.Fprintf(os.Stderr, "[setup] no gb hooks found to remove\n")
		return nil
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}
	out = append(out, '\n')

	if err := os.WriteFile(settingsPath, out, 0600); err != nil {
		return fmt.Errorf("writing: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] removed %d gb hook(s) from %s\n", removed, settingsPath)
	return nil
}

func runSetupClaude(ctx context.Context, workspace, role string) error {
	subs := buildSubscriptions(role)

	// ── User-level settings (claude-settings) ───────────────────────────
	settings, _ := ResolveConfigBeads(ctx, daemon, "claude-settings", subs)
	if settings != nil {
		if err := writeUserSettings(settings); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: failed to write user settings: %v\n", err)
		}
	} else {
		if err := writeUserSettings(defaultUserSettings()); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: failed to write default user settings: %v\n", err)
		}
	}

	// ── Project-level MCP config (claude-mcp) ───────────────────────────
	mcpConfig, _ := ResolveConfigBeads(ctx, daemon, "claude-mcp", subs)
	if mcpConfig == nil {
		mcpConfig = defaultMCPConfig()
	}
	if mcpConfig != nil {
		if err := writeMCPConfig(workspace, mcpConfig); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: failed to write MCP config: %v\n", err)
		}
	}

	// ── Workspace-level hooks (claude-hooks) ────────────────────────────
	hooks, _ := ResolveConfigBeads(ctx, daemon, "claude-hooks", subs)
	if hooks == nil {
		return fmt.Errorf("no claude-hooks config found")
	}

	appendRTKHooks(hooks)
	installRTKContext()

	outDir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating .claude dir: %w", err)
	}

	outPath := filepath.Join(outDir, "settings.json")
	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(outPath, data, 0600); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote %s\n", outPath)

	// ── Agent instructions (claude-instructions) ────────────────────────
	instructions, _ := ResolveConfigBeads(ctx, daemon, "claude-instructions", subs)
	if instructions != nil {
		writeInstructionFiles(workspace, instructions)
	}

	// ── Claude extensions (agents, skills, commands) ─────────────────────
	symlinkClaudeExtensions(workspace)

	return nil
}
