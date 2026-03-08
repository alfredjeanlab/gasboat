package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// envVarPattern matches ${VAR_NAME} placeholders for environment variable expansion.
var envVarPattern = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

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
//
// Environment variable placeholders (${VAR}) in config values are expanded
// from the pod environment before writing. This allows config beads to
// reference secrets injected via K8s (e.g. ${MEZMO_SERVICE_KEY}).
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

	// Expand ${VAR} placeholders from the environment.
	expanded := expandEnvVars(string(data))
	out := []byte(expanded)
	out = append(out, '\n')

	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return fmt.Errorf("writing MCP config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote MCP config to %s\n", outPath)
	return nil
}

// expandEnvVars expands ${VAR} placeholders in s from the environment.
// Unresolved placeholders (empty env var) are replaced with an empty string
// and a warning is logged.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1] // strip ${ and }
		val := os.Getenv(varName)
		if val == "" {
			fmt.Fprintf(os.Stderr, "[setup] warning: env var %s not set (placeholder unresolved)\n", varName)
		}
		return val
	})
}

// defaultMCPConfig returns a fallback MCP config with Playwright if the
// playwright-mcp binary is on PATH. Returns nil if not found.
func defaultMCPConfig() map[string]any {
	if !pathExists("playwright-mcp") {
		return nil
	}
	return map[string]any{
		"mcpServers": map[string]any{
			"playwright": map[string]any{
				"command": "playwright-mcp",
				"args":    []any{"--headless", "--no-sandbox"},
			},
		},
	}
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

// appendRTKHooks adds RTK hooks to settings when RTK is enabled.
func appendRTKHooks(settings map[string]any) {
	if !rtkEnabled() {
		return
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
		settings["hooks"] = hooks
	}

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

	// Prepend so it runs before stop-gate which may block with exit code 2.
	rtkReportHook := hookEntry("/hooks/rtk-report.sh 2>/dev/null || true")
	stopHooks, ok := hooks["Stop"].([]any)
	if !ok {
		stopHooks = []any{}
	}
	hooks["Stop"] = append([]any{rtkReportHook}, stopHooks...)

	fmt.Fprintf(os.Stderr, "[setup] RTK hooks enabled (PreToolUse + Stop report)\n")
}

// installRTKContext copies the RTK.md context file to ~/.claude/RTK.md
// when RTK is enabled and the source file exists.
func installRTKContext() {
	if !rtkEnabled() {
		return
	}
	const src = "/hooks/RTK.md"
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[setup] warning: cannot determine home dir for RTK.md: %v\n", err)
		return
	}
	dst := filepath.Join(homeDir, ".claude", "RTK.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "[setup] warning: mkdir for RTK.md: %v\n", err)
		return
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "[setup] warning: failed to write RTK.md: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "[setup] RTK enabled — installed RTK.md context file\n")
}

// defaultClaudeMD returns a fallback CLAUDE.md template with role-specific
// context. This is used when no claude-instructions config beads are available.
func defaultClaudeMD(role string) string {
	if role == "" {
		role = "crew"
	}
	project := os.Getenv("BOAT_PROJECT")
	agent := os.Getenv("BOAT_AGENT")

	projectSuffix := ""
	if project != "" {
		projectSuffix = " (project: " + project + ")"
	}
	agentLine := ""
	if agent != "" {
		agentLine = "Agent name: " + agent + "\n"
	}
	projectRef := "your project"
	if project != "" {
		projectRef = project
	}

	r := strings.NewReplacer(
		"{{ROLE}}", role,
		"{{PROJECT_SUFFIX}}", projectSuffix,
		"{{AGENT_LINE}}", agentLine,
		"{{PROJECT_REF}}", projectRef,
	)
	return r.Replace(defaultClaudeMDTemplate)
}

// writeUserSettings writes user-level Claude Code settings to ~/.claude/settings.json.
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

// writeInstructionFiles writes CLAUDE.md and stop-gate-text.md from the
// resolved claude-instructions config.
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

// splitRoles splits a comma-separated role string into individual roles.
// Returns nil for empty input. Trims whitespace around each role.
func splitRoles(roles string) []string {
	if roles == "" {
		return nil
	}
	parts := strings.Split(roles, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// buildSubscriptions computes the agent subscription labels for config resolution.
// Supports comma-separated roles (e.g. "thread,crew") — adds a role: subscription
// for each role.
func buildSubscriptions(roles string) []string {
	subs := []string{"global"}
	for _, role := range splitRoles(roles) {
		subs = append(subs, "role:"+role)
	}
	if project := os.Getenv("BOAT_PROJECT"); project != "" {
		subs = append(subs, "project:"+project)
	} else if rig := os.Getenv("BOAT_RIG"); rig != "" {
		subs = append(subs, "project:"+rig)
	}
	return subs
}
