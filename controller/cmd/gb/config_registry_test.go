package main

import (
	"encoding/json"
	"testing"
)

func TestConfigCategoryNames_ReturnsAll(t *testing.T) {
	names := ConfigCategoryNames()
	if len(names) != len(configCategories) {
		t.Fatalf("expected %d names, got %d", len(configCategories), len(names))
	}

	expected := map[string]bool{
		"claude-settings":     true,
		"claude-hooks":        true,
		"claude-mcp":          true,
		"type":                true,
		"context":             true,
		"view":                true,
		"claude-instructions": true,
		"wrapup-config":       true,
		"nudge-prompts":       true,
	}
	for _, name := range names {
		if !expected[name] {
			t.Errorf("unexpected category name: %s", name)
		}
	}
}

func TestLookupCategory(t *testing.T) {
	cat := LookupCategory("claude-settings")
	if cat == nil {
		t.Fatal("expected claude-settings category")
	}
	if cat.Strategy != MergeOverride {
		t.Errorf("expected MergeOverride for claude-settings, got %d", cat.Strategy)
	}

	cat = LookupCategory("claude-hooks")
	if cat == nil {
		t.Fatal("expected claude-hooks category")
	}
	if cat.Strategy != MergeConcat {
		t.Errorf("expected MergeConcat for claude-hooks, got %d", cat.Strategy)
	}

	cat = LookupCategory("nonexistent")
	if cat != nil {
		t.Errorf("expected nil for nonexistent category, got %v", cat)
	}
}

func TestMergeLayers_Override(t *testing.T) {
	layers := []json.RawMessage{
		json.RawMessage(`{"model":"sonnet","permissions":{"allow":["Bash(*)"]}}`),
		json.RawMessage(`{"model":"opus"}`),
	}

	result := MergeLayers(MergeOverride, layers)

	if result["model"] != "opus" {
		t.Errorf("expected model=opus (override), got %v", result["model"])
	}
	if result["permissions"] == nil {
		t.Error("expected permissions from first layer to survive")
	}
}

func TestMergeLayers_Concat(t *testing.T) {
	layers := []json.RawMessage{
		json.RawMessage(`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"gb hook prime"}]}]}}`),
		json.RawMessage(`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"gb hook check-mail"}]}]}}`),
	}

	result := MergeLayers(MergeConcat, layers)

	hooks, ok := result["hooks"].(map[string]any)
	if !ok {
		t.Fatal("expected hooks key")
	}
	sessionStart, ok := hooks["SessionStart"].([]any)
	if !ok {
		t.Fatal("expected SessionStart array")
	}
	if len(sessionStart) != 2 {
		t.Errorf("expected 2 SessionStart hooks (concatenated), got %d", len(sessionStart))
	}
}

func TestMergeLayers_ConcatNewHookType(t *testing.T) {
	layers := []json.RawMessage{
		json.RawMessage(`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"a"}]}]}}`),
		json.RawMessage(`{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"b"}]}]}}`),
	}

	result := MergeLayers(MergeConcat, layers)

	hooks := result["hooks"].(map[string]any)
	if hooks["SessionStart"] == nil {
		t.Error("expected SessionStart from first layer")
	}
	if hooks["Stop"] == nil {
		t.Error("expected Stop from second layer")
	}
}

func TestMergeLayers_EmptyLayers(t *testing.T) {
	result := MergeLayers(MergeOverride, nil)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}

	result = MergeLayers(MergeConcat, nil)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestMergeLayers_InvalidJSON(t *testing.T) {
	layers := []json.RawMessage{
		json.RawMessage(`not valid json`),
		json.RawMessage(`{"model":"opus"}`),
	}

	result := MergeLayers(MergeOverride, layers)
	if result["model"] != "opus" {
		t.Errorf("expected valid layer to be applied, got %v", result["model"])
	}
}

func TestLookupCategory_ClaudeInstructions(t *testing.T) {
	cat := LookupCategory("claude-instructions")
	if cat == nil {
		t.Fatal("expected claude-instructions category")
	}
	if cat.Strategy != MergeOverride {
		t.Errorf("expected MergeOverride for claude-instructions, got %d", cat.Strategy)
	}
	if cat.Description == "" {
		t.Error("expected non-empty description for claude-instructions")
	}
}

func TestAllCategoriesHaveDescriptions(t *testing.T) {
	for _, cat := range configCategories {
		if cat.Description == "" {
			t.Errorf("category %q has no description", cat.Name)
		}
	}
}
