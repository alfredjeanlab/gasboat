package main

import (
	"encoding/json"
	"testing"
)

func TestDefaultUserSettings_ContainsAgentTeamsEnv(t *testing.T) {
	settings := defaultUserSettings()

	envRaw, ok := settings["env"]
	if !ok {
		t.Fatal("defaultUserSettings() missing 'env' key")
	}

	env, ok := envRaw.(map[string]any)
	if !ok {
		t.Fatal("'env' is not a map[string]any")
	}

	val, ok := env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"]
	if !ok {
		t.Fatal("'env' missing CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS key")
	}

	if val != "1" {
		t.Errorf("CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS = %q, want %q", val, "1")
	}
}

func TestDefaultUserSettings_EnvSurvivesMerge(t *testing.T) {
	// Simulate a config bead layer that has permissions but no env.
	layer := json.RawMessage(`{"permissions":{"allow":["Bash(*)"],"deny":[]}}`)
	merged := mergeUserSettingsLayers([]json.RawMessage{layer})

	// Verify that a layer without env doesn't inject one.
	if _, ok := merged["env"]; ok {
		t.Error("merge should not add env when layer lacks it")
	}

	// Now simulate a layer that sets env.
	layerWithEnv := json.RawMessage(`{"env":{"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS":"1"}}`)
	merged = mergeUserSettingsLayers([]json.RawMessage{layer, layerWithEnv})

	env, ok := merged["env"].(map[string]any)
	if !ok {
		t.Fatal("merged settings missing 'env' map after layer with env")
	}

	if env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"] != "1" {
		t.Errorf("env merge produced %v, want CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1", env)
	}
}
