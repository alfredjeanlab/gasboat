package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

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
			return runSetupClaudeDefaults(workspace)
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

// hookEntry creates a single hook entry for a Claude Code settings.json hooks array.
func hookEntry(command string) map[string]any {
	return map[string]any{
		"matcher": "",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command,
			},
		},
	}
}

// defaultUserSettings returns the hardcoded user-level Claude Code settings
// (permissions, thinking mode, dangerous mode bypass). This is the fallback
// when claude-settings config beads are not available.
func defaultUserSettings() map[string]any {
	return map[string]any{
		"permissions": map[string]any{
			"allow": []any{
				"Bash(*)", "Read(*)", "Write(*)", "Edit(*)",
				"Glob(*)", "Grep(*)", "WebFetch(*)", "WebSearch(*)",
			},
			"deny": []any{},
		},
		"alwaysThinkingEnabled":          true,
		"skipDangerousModePermissionPrompt": true,
	}
}

// writeMCPConfig writes project-level MCP config to {workspace}/.mcp.json.
// If the file already exists (e.g. from a cloned repo), mcpServers entries
// are merged without overwriting existing servers.
func writeMCPConfig(workspace string, config map[string]any) error {
	outPath := filepath.Join(workspace, ".mcp.json")

	servers, _ := config["mcpServers"].(map[string]any)
	if len(servers) == 0 {
		fmt.Fprintf(os.Stderr, "[setup] no MCP servers to configure\n")
		return nil
	}

	// Merge with existing .mcp.json if present.
	existing := make(map[string]any)
	if data, err := os.ReadFile(outPath); err == nil {
		_ = json.Unmarshal(data, &existing)
	}

	existingServers, _ := existing["mcpServers"].(map[string]any)
	if existingServers == nil {
		existingServers = make(map[string]any)
	}

	// New servers fill gaps; existing entries are preserved.
	for name, cfg := range servers {
		if _, ok := existingServers[name]; !ok {
			existingServers[name] = cfg
		}
	}
	existing["mcpServers"] = existingServers

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling MCP config: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("writing MCP config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote MCP config to %s\n", outPath)
	return nil
}

// appendDetectedPlugins auto-detects installed LSP servers and adds them
// to the settings' enabledPlugins map.
func appendDetectedPlugins(settings map[string]any) {
	plugins := make(map[string]any)
	if existing, ok := settings["enabledPlugins"].(map[string]any); ok {
		for k, v := range existing {
			plugins[k] = v
		}
	}

	detected := false
	if pathExists("gopls") {
		plugins["gopls-lsp@claude-plugins-official"] = true
		detected = true
	}
	if pathExists("rust-analyzer") {
		plugins["rust-analyzer-lsp@claude-plugins-official"] = true
		detected = true
	}

	if detected {
		settings["enabledPlugins"] = plugins
	}
}

// injectTeamsEnv adds the Claude Code Agent Teams feature flag to the
// settings env block when CLAUDE_TEAMS_ENABLED is set in the environment.
// Claude Code reads env vars from settings.json and injects them into its
// own process, enabling the TeamCreate/TaskCreate/SendMessage tools.
func injectTeamsEnv(settings map[string]any) {
	if v := os.Getenv("CLAUDE_TEAMS_ENABLED"); v != "true" && v != "1" {
		return
	}
	env, ok := settings["env"].(map[string]any)
	if !ok {
		env = make(map[string]any)
	}
	env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"] = "1"
	settings["env"] = env
	fmt.Fprintf(os.Stderr, "[setup] Agent Teams enabled (CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1)\n")
}

// lookPath is the function used to check if an executable exists on PATH.
// Defaults to exec.LookPath; overridden in tests for deterministic behavior.
var lookPath = exec.LookPath

// pathExists checks if an executable exists on PATH.
func pathExists(name string) bool {
	_, err := lookPath(name)
	return err == nil
}

func defaultHookSettings() map[string]any {
	return map[string]any{
		"hooks": map[string]any{
			// SessionStart: prime context + check mail + audit worktrees.
			"SessionStart": []any{
				hookEntry("gb hook prime 2>/dev/null || true"),
				hookEntry("gb hook check-mail 2>/dev/null || true"),
				hookEntry("gb workspace audit 2>/dev/null || true"),
			},
			// PreCompact: re-prime so context survives compaction.
			"PreCompact": []any{
				hookEntry("gb hook prime 2>/dev/null || true"),
			},
			// UserPromptSubmit: check mail on every human message.
			"UserPromptSubmit": []any{
				hookEntry("gb hook check-mail 2>/dev/null || true"),
			},
			// Stop: gate check — exit code 2 blocks the agent.
			"Stop": []any{
				hookEntry("gb hook stop-gate"),
			},
			// Agent Teams: relay teammate and task events to NATS for visibility.
			"TaskCompleted": []any{
				hookEntry("gb hook relay 2>/dev/null || true"),
			},
			"TeammateIdle": []any{
				hookEntry("gb hook relay 2>/dev/null || true"),
			},
		},
	}
}

// rtkEnabled returns true if RTK token optimization is enabled via environment.
func rtkEnabled() bool {
	v := os.Getenv("RTK_ENABLED")
	return v == "true" || v == "1"
}

// appendRTKHooks adds RTK hooks to settings when RTK is enabled:
// - PreToolUse:Bash — rewrites commands through rtk
// - Stop — reports token savings before the stop-gate
func appendRTKHooks(settings map[string]any) {
	if !rtkEnabled() {
		return
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
		settings["hooks"] = hooks
	}

	// PreToolUse: rewrite Bash commands through RTK.
	rtkRewriteHook := map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": "/hooks/rtk-rewrite.sh",
			},
		},
	}
	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok {
		preToolUse = []any{}
	}
	hooks["PreToolUse"] = append(preToolUse, rtkRewriteHook)

	// Stop: report token savings (runs before stop-gate).
	// Prepend so it runs before stop-gate which may block with exit code 2.
	rtkReportHook := hookEntry("/hooks/rtk-report.sh 2>/dev/null || true")
	stopHooks, ok := hooks["Stop"].([]any)
	if !ok {
		stopHooks = []any{}
	}
	hooks["Stop"] = append([]any{rtkReportHook}, stopHooks...)

	fmt.Fprintf(os.Stderr, "[setup] RTK hooks enabled (PreToolUse + Stop report)\n")
}

func runSetupClaudeDefaults(workspace string) error {
	// Write user-level settings (permissions, plugins, thinking).
	if err := writeUserSettings(defaultUserSettings()); err != nil {
		return fmt.Errorf("writing user settings: %w", err)
	}

	// Write workspace-level hooks.
	settings := defaultHookSettings()
	appendRTKHooks(settings)

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
	return nil
}

// writeUserSettings writes user-level Claude Code settings to ~/.claude/settings.json.
// Auto-detects installed LSP plugins and injects feature flags.
func writeUserSettings(settings map[string]any) error {
	appendDetectedPlugins(settings)
	injectTeamsEnv(settings)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home dir: %w", err)
	}

	claudeDir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("creating ~/.claude dir: %w", err)
	}

	outPath := filepath.Join(claudeDir, "settings.json")
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling user settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(outPath, data, 0600); err != nil {
		return fmt.Errorf("writing user settings: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote user settings to %s\n", outPath)
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

// buildSubscriptions computes the agent subscription labels for config
// resolution. Uses the role to build role: labels, plus always includes global.
func buildSubscriptions(role string) []string {
	subs := []string{"global"}
	if role != "" {
		subs = append(subs, "role:"+role)
	}
	if rig := os.Getenv("BOAT_RIG"); rig != "" {
		subs = append(subs, "rig:"+rig)
	}
	if project := os.Getenv("BOAT_PROJECT"); project != "" {
		subs = append(subs, "project:"+project)
	}
	return subs
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
	// Symlink .claude/agents, skills, commands from the project repo into
	// the workspace so Claude discovers custom extensions at session init.
	symlinkClaudeExtensions(workspace)

	return nil
}

// writeInstructionFiles writes CLAUDE.md and stop-gate-text.md from the
// resolved claude-instructions config. Only writes each file if the
// corresponding key exists in the config.
func writeInstructionFiles(workspace string, config map[string]any) {
	if claudeMD, ok := config["claude_md"].(string); ok && claudeMD != "" {
		path := filepath.Join(workspace, "CLAUDE.md")
		if err := os.WriteFile(path, []byte(claudeMD), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: failed to write CLAUDE.md: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[setup] wrote %s from claude-instructions config\n", path)
		}
	}

	if stopGate, ok := config["stop_gate_blocked"].(string); ok && stopGate != "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: cannot determine home dir for stop-gate-text.md: %v\n", err)
			return
		}
		claudeDir := filepath.Join(homeDir, ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: failed to create %s: %v\n", claudeDir, err)
			return
		}
		path := filepath.Join(claudeDir, "stop-gate-text.md")
		if err := os.WriteFile(path, []byte(stopGate), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: failed to write stop-gate-text.md: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[setup] wrote %s from claude-instructions config\n", path)
		}
	}
}

// mergeSimpleLayers merges JSON config layers with simple key override.
// Later layers override earlier ones. Used for user-level settings
// (permissions, model, plugins) where the last value wins.
func mergeSimpleLayers(layers []json.RawMessage) map[string]any {
	result := make(map[string]any)
	for _, raw := range layers {
		var layer map[string]any
		if err := json.Unmarshal(raw, &layer); err != nil {
			continue
		}
		for k, v := range layer {
			result[k] = v
		}
	}
	return result
}

func mergeHookLayers(layers []json.RawMessage) map[string]any {
	result := make(map[string]any)
	for _, raw := range layers {
		var layer map[string]any
		if err := json.Unmarshal(raw, &layer); err != nil {
			continue
		}
		mergeSettingsLayer(result, layer)
	}
	return result
}

func mergeSettingsLayer(dst, src map[string]any) {
	for k, v := range src {
		if k == "hooks" {
			srcHooks, ok := v.(map[string]any)
			if !ok {
				dst[k] = v
				continue
			}
			dstHooks, ok := dst[k].(map[string]any)
			if !ok {
				dstHooks = make(map[string]any)
				dst[k] = dstHooks
			}
			mergeHooksField(dstHooks, srcHooks)
		} else {
			dst[k] = v
		}
	}
}

func mergeHooksField(dst, src map[string]any) {
	for hookType, srcVal := range src {
		srcArr, ok := srcVal.([]any)
		if !ok {
			dst[hookType] = srcVal
			continue
		}
		dstArr, ok := dst[hookType].([]any)
		if !ok {
			dst[hookType] = srcArr
			continue
		}
		dst[hookType] = append(dstArr, srcArr...)
	}
}
