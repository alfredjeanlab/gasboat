package main

import (
	"encoding/json"
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

func TestAppendDetectedPlugins_NoPlugins(t *testing.T) {
	settings := map[string]any{}
	// With no gopls or rust-analyzer on PATH, no plugins should be added.
	// We can't guarantee they're absent, so just verify the function doesn't panic.
	appendDetectedPlugins(settings)
	// If no plugins detected, enabledPlugins key may or may not exist.
}

func TestAppendDetectedPlugins_PreservesExisting(t *testing.T) {
	settings := map[string]any{
		"enabledPlugins": map[string]any{
			"custom-plugin": true,
		},
	}
	appendDetectedPlugins(settings)
	plugins := settings["enabledPlugins"].(map[string]any)
	if plugins["custom-plugin"] != true {
		t.Error("existing plugin should be preserved")
	}
}

func TestAppendDetectedPlugins_EmptySettings(t *testing.T) {
	settings := map[string]any{}
	// Should not panic on empty settings.
	appendDetectedPlugins(settings)
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
