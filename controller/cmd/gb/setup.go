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
	Long: `Fetches claude-settings and claude-hooks config beads from the daemon,
merges them by specificity (global → role), and writes:
  - User-level settings to --claude-dir/settings.json (permissions, plugins, model)
  - Workspace-level hooks to --workspace/.claude/settings.json

Config bead keys (checked in order, later overrides earlier):
  claude-settings:global — base user settings for all agents
  claude-settings:<role> — role-specific setting overrides
  claude-hooks:global    — base hooks for all agents
  claude-hooks:<role>    — role-specific hook overrides

Flags:
  --claude-dir Write user-level settings to this directory
  --defaults   Install hardcoded default hooks (no server needed)
  --seed       Create default claude-settings:global config bead in the daemon
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

		claudeDir, _ := cmd.Flags().GetString("claude-dir")

		check, _ := cmd.Flags().GetBool("check")
		if check {
			return runSetupClaudeCheck(workspace)
		}

		remove, _ := cmd.Flags().GetBool("remove")
		if remove {
			return runSetupClaudeRemove(workspace)
		}

		seed, _ := cmd.Flags().GetBool("seed")
		if seed {
			return runSetupClaudeSeed(cmd.Context())
		}

		defaults, _ := cmd.Flags().GetBool("defaults")
		if defaults {
			return runSetupClaudeDefaults(workspace)
		}

		return runSetupClaude(cmd.Context(), workspace, role, claudeDir)
	},
}

func init() {
	setupClaudeCmd.Flags().String("workspace", os.Getenv("KD_WORKSPACE"), "workspace directory")
	setupClaudeCmd.Flags().String("role", "", "agent role (default: $BOAT_ROLE or $KD_ROLE)")
	setupClaudeCmd.Flags().String("claude-dir", "", "user-level Claude directory for settings (default: $HOME/.claude)")
	setupClaudeCmd.Flags().Bool("defaults", false, "install hardcoded default hooks (no server needed)")
	setupClaudeCmd.Flags().Bool("seed", false, "create default claude-settings:global config bead in the daemon")
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

// defaultUserSettings returns hardcoded user-level settings matching the
// values previously baked into entrypoint.sh. These serve as the fallback
// when no claude-settings config beads exist.
func defaultUserSettings() map[string]any {
	return map[string]any{
		"permissions": map[string]any{
			"allow": []any{
				"Bash(*)", "Read(*)", "Write(*)", "Edit(*)",
				"Glob(*)", "Grep(*)", "WebFetch(*)", "WebSearch(*)",
			},
			"deny": []any{},
		},
		"env": map[string]any{
			"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1",
		},
		"alwaysThinkingEnabled":             true,
		"skipDangerousModePermissionPrompt": true,
	}
}

// addDetectedPlugins checks for LSP servers on the PATH and adds them to
// the enabledPlugins field of the settings map.
func addDetectedPlugins(settings map[string]any) {
	plugins := map[string]any{}
	if _, err := exec.LookPath("gopls"); err == nil {
		plugins["gopls-lsp@claude-plugins-official"] = true
		fmt.Fprintf(os.Stderr, "[setup] enabling gopls LSP plugin\n")
	}
	if _, err := exec.LookPath("rust-analyzer"); err == nil {
		plugins["rust-analyzer-lsp@claude-plugins-official"] = true
		fmt.Fprintf(os.Stderr, "[setup] enabling rust-analyzer LSP plugin\n")
	}
	if len(plugins) == 0 {
		return
	}

	existing, _ := settings["enabledPlugins"].(map[string]any)
	if existing == nil {
		settings["enabledPlugins"] = plugins
	} else {
		for k, v := range plugins {
			existing[k] = v
		}
	}
}

func defaultHookSettings() map[string]any {
	return map[string]any{
		"hooks": map[string]any{
			// SessionStart: prime context + check mail + audit worktrees + relay.
			"SessionStart": []any{
				hookEntry("gb hook prime 2>/dev/null || true"),
				hookEntry("gb hook check-mail 2>/dev/null || true"),
				hookEntry("gb workspace audit 2>/dev/null || true"),
				hookEntry("gb hook relay 2>/dev/null || true"),
			},
			// PreCompact: re-prime so context survives compaction + relay.
			"PreCompact": []any{
				hookEntry("gb hook prime 2>/dev/null || true"),
				hookEntry("gb hook relay 2>/dev/null || true"),
			},
			// UserPromptSubmit: check mail on every human message.
			"UserPromptSubmit": []any{
				hookEntry("gb hook check-mail 2>/dev/null || true"),
			},
			// PreToolUse: relay tool events to NATS for doots.
			"PreToolUse": []any{
				hookEntry("gb hook relay 2>/dev/null || true"),
			},
			// PostToolUse: relay tool results to NATS for doots.
			"PostToolUse": []any{
				hookEntry("gb hook relay 2>/dev/null || true"),
			},
			// SubagentStart: relay subagent spawn events.
			"SubagentStart": []any{
				hookEntry("gb hook relay 2>/dev/null || true"),
			},
			// SubagentStop: relay subagent completion events.
			"SubagentStop": []any{
				hookEntry("gb hook relay 2>/dev/null || true"),
			},
			// Stop: relay + gate check — gate must be last (exit 2 blocks).
			"Stop": []any{
				hookEntry("gb hook relay 2>/dev/null || true"),
				hookEntry("gb hook stop-gate"),
			},
			// SessionEnd: relay session termination.
			"SessionEnd": []any{
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

// runSetupClaudeSeed creates the default claude-settings:global config bead
// in the daemon with current hardcoded values. This enables config-bead-based
// settings management without changing existing agent behavior.
func runSetupClaudeSeed(ctx context.Context) error {
	settings := defaultUserSettings()

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshalling default settings: %w", err)
	}

	key := "claude-settings:global"
	if err := daemon.SetConfig(ctx, key, data); err != nil {
		return fmt.Errorf("seeding %s: %w", key, err)
	}

	fmt.Fprintf(os.Stderr, "[setup] seeded %s with default values\n", key)
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

func runSetupClaude(ctx context.Context, workspace, role, claudeDir string) error {
	// --- User-level settings (permissions, model, plugins) ---
	if claudeDir != "" {
		if err := writeUserSettings(ctx, claudeDir, role); err != nil {
			fmt.Fprintf(os.Stderr, "[setup] warning: user settings: %v\n", err)
		}
	}

	// --- Workspace-level hooks ---
	var hookLayers []json.RawMessage

	if cfg, err := daemon.GetConfig(ctx, "claude-hooks:global"); err == nil && cfg != nil {
		hookLayers = append(hookLayers, cfg.Value)
		fmt.Fprintf(os.Stderr, "[setup] loaded claude-hooks:global\n")
	}

	if role != "" {
		if cfg, err := daemon.GetConfig(ctx, "claude-hooks:"+role); err == nil && cfg != nil {
			hookLayers = append(hookLayers, cfg.Value)
			fmt.Fprintf(os.Stderr, "[setup] loaded claude-hooks:%s\n", role)
		}
	}

	if len(hookLayers) == 0 {
		return fmt.Errorf("no claude-hooks config beads found")
	}

	merged := mergeHookLayers(hookLayers)
	appendRTKHooks(merged)

	outDir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating .claude dir: %w", err)
	}

	outPath := filepath.Join(outDir, "settings.json")
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(outPath, data, 0600); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote %s\n", outPath)
	return nil
}

// writeUserSettings fetches claude-settings config beads, merges them with
// detected LSP plugins, and writes the result to claudeDir/settings.json.
// Falls back to hardcoded defaults when no config beads are available.
func writeUserSettings(ctx context.Context, claudeDir, role string) error {
	var layers []json.RawMessage

	if cfg, err := daemon.GetConfig(ctx, "claude-settings:global"); err == nil && cfg != nil {
		layers = append(layers, cfg.Value)
		fmt.Fprintf(os.Stderr, "[setup] loaded claude-settings:global\n")
	}

	if role != "" {
		if cfg, err := daemon.GetConfig(ctx, "claude-settings:"+role); err == nil && cfg != nil {
			layers = append(layers, cfg.Value)
			fmt.Fprintf(os.Stderr, "[setup] loaded claude-settings:%s\n", role)
		}
	}

	var settings map[string]any
	if len(layers) > 0 {
		settings = mergeUserSettingsLayers(layers)
	} else {
		settings = defaultUserSettings()
		fmt.Fprintf(os.Stderr, "[setup] no claude-settings config beads, using defaults\n")
	}

	addDetectedPlugins(settings)

	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("creating claude dir: %w", err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling user settings: %w", err)
	}
	data = append(data, '\n')

	outPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(outPath, data, 0600); err != nil {
		return fmt.Errorf("writing user settings: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote user settings to %s\n", outPath)
	return nil
}

// mergeUserSettingsLayers merges user-settings JSON layers with top-level
// key override semantics: later layers override earlier ones per key.
func mergeUserSettingsLayers(layers []json.RawMessage) map[string]any {
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
