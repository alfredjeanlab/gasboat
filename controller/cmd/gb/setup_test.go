package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultUserSettings(t *testing.T) {
	s := defaultUserSettings()

	perms, ok := s["permissions"].(map[string]any)
	if !ok {
		t.Fatal("expected permissions key")
	}

	allow, ok := perms["allow"].([]any)
	if !ok || len(allow) == 0 {
		t.Fatal("expected non-empty allow list")
	}

	if s["alwaysThinkingEnabled"] != true {
		t.Error("expected alwaysThinkingEnabled=true")
	}
}

func TestMergeSimpleLayers(t *testing.T) {
	global := json.RawMessage(`{"model":"sonnet","permissions":{"allow":["Bash(*)"]}}`)
	role := json.RawMessage(`{"model":"opus"}`)

	merged := mergeSimpleLayers([]json.RawMessage{global, role})

	if merged["model"] != "opus" {
		t.Errorf("expected model=opus (role override), got %v", merged["model"])
	}

	// Permissions should be from global (role didn't override it).
	if merged["permissions"] == nil {
		t.Error("expected permissions key from global layer")
	}
}

func TestMergeSimpleLayers_RoleOverridesGlobal(t *testing.T) {
	global := json.RawMessage(`{"alwaysThinkingEnabled":true,"model":"sonnet"}`)
	role := json.RawMessage(`{"alwaysThinkingEnabled":false}`)

	merged := mergeSimpleLayers([]json.RawMessage{global, role})

	if merged["alwaysThinkingEnabled"] != false {
		t.Errorf("expected alwaysThinkingEnabled=false (role override), got %v", merged["alwaysThinkingEnabled"])
	}
	if merged["model"] != "sonnet" {
		t.Errorf("expected model=sonnet (from global), got %v", merged["model"])
	}
}

func TestWriteUserSettings(t *testing.T) {
	// Override HOME to a temp dir so we don't write to the real home.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	settings := map[string]any{
		"model":                 "opus",
		"alwaysThinkingEnabled": true,
	}

	if err := writeUserSettings(settings); err != nil {
		t.Fatalf("writeUserSettings: %v", err)
	}

	outPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing settings JSON: %v", err)
	}

	if result["model"] != "opus" {
		t.Errorf("expected model=opus, got %v", result["model"])
	}
	if result["alwaysThinkingEnabled"] != true {
		t.Errorf("expected alwaysThinkingEnabled=true, got %v", result["alwaysThinkingEnabled"])
	}
}

func TestAppendDetectedPlugins(t *testing.T) {
	tests := []struct {
		name        string
		existing    map[string]any // initial enabledPlugins (nil = no key)
		goplsFound  bool
		rustFound   bool
		wantPlugins map[string]bool // expected plugin keys and values; nil = no enabledPlugins key
	}{
		{
			name:        "nothing detected, no existing",
			existing:    nil,
			goplsFound:  false,
			rustFound:   false,
			wantPlugins: nil,
		},
		{
			name:        "nothing detected, preserves existing",
			existing:    map[string]any{"custom-plugin": true},
			goplsFound:  false,
			rustFound:   false,
			wantPlugins: map[string]bool{"custom-plugin": true},
		},
		{
			name:       "gopls only",
			existing:   nil,
			goplsFound: true,
			rustFound:  false,
			wantPlugins: map[string]bool{
				"gopls-lsp@claude-plugins-official": true,
			},
		},
		{
			name:       "rust-analyzer only",
			existing:   nil,
			goplsFound: false,
			rustFound:  true,
			wantPlugins: map[string]bool{
				"rust-analyzer-lsp@claude-plugins-official": true,
			},
		},
		{
			name:       "both detected with existing",
			existing:   map[string]any{"custom-plugin": true},
			goplsFound: true,
			rustFound:  true,
			wantPlugins: map[string]bool{
				"custom-plugin":                            true,
				"gopls-lsp@claude-plugins-official":        true,
				"rust-analyzer-lsp@claude-plugins-official": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := lookPath
			t.Cleanup(func() { lookPath = orig })

			lookPath = func(name string) (string, error) {
				switch name {
				case "gopls":
					if tt.goplsFound {
						return "/usr/bin/gopls", nil
					}
				case "rust-analyzer":
					if tt.rustFound {
						return "/usr/bin/rust-analyzer", nil
					}
				}
				return "", fmt.Errorf("not found: %s", name)
			}

			settings := map[string]any{}
			if tt.existing != nil {
				ep := make(map[string]any, len(tt.existing))
				for k, v := range tt.existing {
					ep[k] = v
				}
				settings["enabledPlugins"] = ep
			}

			appendDetectedPlugins(settings)

			plugins, hasKey := settings["enabledPlugins"].(map[string]any)
			if tt.wantPlugins == nil {
				if hasKey {
					t.Errorf("expected no enabledPlugins key, got %v", plugins)
				}
				return
			}
			if !hasKey {
				t.Fatal("expected enabledPlugins key")
			}
			if len(plugins) != len(tt.wantPlugins) {
				t.Errorf("expected %d plugins, got %d: %v", len(tt.wantPlugins), len(plugins), plugins)
			}
			for k, want := range tt.wantPlugins {
				if got, ok := plugins[k].(bool); !ok || got != want {
					t.Errorf("plugin %q: want %v, got %v", k, want, plugins[k])
				}
			}
		})
	}
}

func TestWriteMCPConfig_NewFile(t *testing.T) {
	workspace := t.TempDir()
	config := map[string]any{
		"mcpServers": map[string]any{
			"playwright": map[string]any{
				"command": "playwright-mcp",
				"args":    []any{"--headless"},
			},
		},
	}

	if err := writeMCPConfig(workspace, config); err != nil {
		t.Fatalf("writeMCPConfig: %v", err)
	}

	outPath := filepath.Join(workspace, ".mcp.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading .mcp.json: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing JSON: %v", err)
	}

	servers, ok := result["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers key")
	}
	if servers["playwright"] == nil {
		t.Error("expected playwright server")
	}
}

func TestWriteMCPConfig_PreservesExisting(t *testing.T) {
	workspace := t.TempDir()

	// Write an existing .mcp.json with a mezmo server.
	existing := `{"mcpServers":{"mezmo":{"type":"http","url":"https://mcp.mezmo.com"}}}`
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	// Write new config with playwright — should NOT overwrite mezmo.
	config := map[string]any{
		"mcpServers": map[string]any{
			"playwright": map[string]any{
				"command": "playwright-mcp",
			},
		},
	}

	if err := writeMCPConfig(workspace, config); err != nil {
		t.Fatalf("writeMCPConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	servers := result["mcpServers"].(map[string]any)
	if servers["mezmo"] == nil {
		t.Error("expected existing mezmo server to be preserved")
	}
	if servers["playwright"] == nil {
		t.Error("expected new playwright server to be added")
	}
}

func TestWriteMCPConfig_DoesNotOverrideExistingServer(t *testing.T) {
	workspace := t.TempDir()

	// Existing .mcp.json has playwright with custom args.
	existing := `{"mcpServers":{"playwright":{"command":"playwright-mcp","args":["--custom"]}}}`
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	// New config tries to set playwright with different args.
	config := map[string]any{
		"mcpServers": map[string]any{
			"playwright": map[string]any{
				"command": "playwright-mcp",
				"args":    []any{"--headless", "--no-sandbox"},
			},
		},
	}

	if err := writeMCPConfig(workspace, config); err != nil {
		t.Fatalf("writeMCPConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspace, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	servers := result["mcpServers"].(map[string]any)
	pw := servers["playwright"].(map[string]any)
	args := pw["args"].([]any)
	if len(args) != 1 || args[0] != "--custom" {
		t.Errorf("existing server should be preserved, got args=%v", args)
	}
}

func TestWriteMCPConfig_EmptyServers(t *testing.T) {
	workspace := t.TempDir()
	config := map[string]any{
		"mcpServers": map[string]any{},
	}

	if err := writeMCPConfig(workspace, config); err != nil {
		t.Fatalf("writeMCPConfig: %v", err)
	}

	// Should not create the file when there are no servers.
	outPath := filepath.Join(workspace, ".mcp.json")
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Error("expected no .mcp.json file when servers are empty")
	}
}

func TestWriteUserSettings_FilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := writeUserSettings(map[string]any{"model": "test"}); err != nil {
		t.Fatalf("writeUserSettings: %v", err)
	}

	outPath := filepath.Join(tmpDir, ".claude", "settings.json")
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected file permissions 0600, got %o", perm)
	}
}

func TestWriteInstructionFiles_ClaudeMD(t *testing.T) {
	workspace := t.TempDir()
	config := map[string]any{
		"claude_md": "# Test Agent\n\nYou are a test agent.",
	}

	writeInstructionFiles(workspace, config)

	data, err := os.ReadFile(filepath.Join(workspace, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("expected CLAUDE.md: %v", err)
	}
	if string(data) != "# Test Agent\n\nYou are a test agent." {
		t.Errorf("unexpected CLAUDE.md content: %s", data)
	}
}

func TestWriteInstructionFiles_StopGate(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	config := map[string]any{
		"stop_gate_blocked": "<system-reminder>STOP BLOCKED</system-reminder>",
	}

	writeInstructionFiles("/nonexistent", config)

	data, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "stop-gate-text.md"))
	if err != nil {
		t.Fatalf("expected stop-gate-text.md: %v", err)
	}
	if string(data) != "<system-reminder>STOP BLOCKED</system-reminder>" {
		t.Errorf("unexpected stop-gate-text.md content: %s", data)
	}
}

func TestWriteInstructionFiles_EmptyConfig(t *testing.T) {
	workspace := t.TempDir()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Neither key present — should not create any files.
	writeInstructionFiles(workspace, map[string]any{"other_key": "value"})

	if _, err := os.Stat(filepath.Join(workspace, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("expected no CLAUDE.md when key is absent")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".claude", "stop-gate-text.md")); !os.IsNotExist(err) {
		t.Error("expected no stop-gate-text.md when key is absent")
	}
}

func TestRunSetupClaudeDefaults_WritesBothFiles(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	workspace := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}

	if err := runSetupClaudeDefaults(workspace); err != nil {
		t.Fatalf("runSetupClaudeDefaults: %v", err)
	}

	// User-level settings should exist.
	userPath := filepath.Join(tmpDir, ".claude", "settings.json")
	userData, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("expected user settings at %s: %v", userPath, err)
	}
	var userSettings map[string]any
	if err := json.Unmarshal(userData, &userSettings); err != nil {
		t.Fatalf("invalid user settings JSON: %v", err)
	}
	if userSettings["permissions"] == nil {
		t.Error("expected permissions in user settings")
	}

	// No .mcp.json should be created without config beads.
	mcpPath := filepath.Join(workspace, ".mcp.json")
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Error("expected no .mcp.json in defaults mode (no config beads)")
	}

	// Workspace-level hooks should exist.
	wsPath := filepath.Join(workspace, ".claude", "settings.json")
	wsData, err := os.ReadFile(wsPath)
	if err != nil {
		t.Fatalf("expected workspace settings at %s: %v", wsPath, err)
	}
	var wsSettings map[string]any
	if err := json.Unmarshal(wsData, &wsSettings); err != nil {
		t.Fatalf("invalid workspace settings JSON: %v", err)
	}
	if wsSettings["hooks"] == nil {
		t.Error("expected hooks in workspace settings")
	}
}
