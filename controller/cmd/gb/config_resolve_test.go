package main

import (
	"context"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// --- Mock for config bead resolution ---

type mockConfigBeadLister struct {
	beads []*beadsapi.BeadDetail
	err   error
}

func (m *mockConfigBeadLister) ListBeadsFiltered(_ context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &beadsapi.ListBeadsResult{Beads: m.beads, Total: len(m.beads)}, nil
}

// --- ResolveConfigBeads tests ---

func TestResolveConfigBeads_GlobalOnly(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "claude-settings",
				Labels:      []string{"global"},
				Description: `{"model":"sonnet","permissions":{"allow":["Bash(*)"]}}`,
			},
		},
	}

	subs := []string{"global", "role:captain"}
	merged, count := ResolveConfigBeads(context.Background(), lister, "claude-settings", subs)

	if count != 1 {
		t.Fatalf("expected 1 layer, got %d", count)
	}
	if merged["model"] != "sonnet" {
		t.Errorf("expected model=sonnet, got %v", merged["model"])
	}
}

func TestResolveConfigBeads_RoleOverridesGlobal(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "claude-settings",
				Labels:      []string{"global"},
				Description: `{"model":"sonnet","alwaysThinkingEnabled":true}`,
			},
			{
				Title:       "claude-settings",
				Labels:      []string{"role:captain"},
				Description: `{"model":"opus"}`,
			},
		},
	}

	subs := []string{"global", "role:captain"}
	merged, count := ResolveConfigBeads(context.Background(), lister, "claude-settings", subs)

	if count != 2 {
		t.Fatalf("expected 2 layers, got %d", count)
	}
	if merged["model"] != "opus" {
		t.Errorf("expected model=opus (role override), got %v", merged["model"])
	}
	if merged["alwaysThinkingEnabled"] != true {
		t.Error("expected alwaysThinkingEnabled from global layer")
	}
}

func TestResolveConfigBeads_HooksConcat(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "claude-hooks",
				Labels:      []string{"global"},
				Description: `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"gb hook prime"}]}]}}`,
			},
			{
				Title:       "claude-hooks",
				Labels:      []string{"role:captain"},
				Description: `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"gb hook extra"}]}]}}`,
			},
		},
	}

	subs := []string{"global", "role:captain"}
	merged, count := ResolveConfigBeads(context.Background(), lister, "claude-hooks", subs)

	if count != 2 {
		t.Fatalf("expected 2 layers, got %d", count)
	}
	hooks, ok := merged["hooks"].(map[string]any)
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

func TestResolveConfigBeads_FiltersNonMatching(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "claude-settings",
				Labels:      []string{"global"},
				Description: `{"model":"sonnet"}`,
			},
			{
				Title:       "claude-settings",
				Labels:      []string{"role:engineer"},
				Description: `{"model":"haiku"}`,
			},
		},
	}

	// Agent is a captain, should NOT get engineer settings.
	subs := []string{"global", "role:captain"}
	merged, count := ResolveConfigBeads(context.Background(), lister, "claude-settings", subs)

	if count != 1 {
		t.Fatalf("expected 1 matching layer (global only), got %d", count)
	}
	if merged["model"] != "sonnet" {
		t.Errorf("expected model=sonnet (global only), got %v", merged["model"])
	}
}

func TestResolveConfigBeads_FiltersWrongCategory(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "claude-mcp",
				Labels:      []string{"global"},
				Description: `{"mcpServers":{}}`,
			},
		},
	}

	subs := []string{"global"}
	merged, count := ResolveConfigBeads(context.Background(), lister, "claude-settings", subs)

	if count != 0 || merged != nil {
		t.Errorf("expected no match for wrong category, got %d layers", count)
	}
}

func TestResolveConfigBeads_NoBeads(t *testing.T) {
	lister := &mockConfigBeadLister{beads: []*beadsapi.BeadDetail{}}

	subs := []string{"global"}
	merged, count := ResolveConfigBeads(context.Background(), lister, "claude-settings", subs)

	if count != 0 || merged != nil {
		t.Errorf("expected 0 layers, got %d", count)
	}
}

func TestResolveConfigBeads_InvalidJSON(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "claude-settings",
				Labels:      []string{"global"},
				Description: `not valid json`,
			},
			{
				Title:       "claude-settings",
				Labels:      []string{"role:captain"},
				Description: `{"model":"opus"}`,
			},
		},
	}

	subs := []string{"global", "role:captain"}
	merged, count := ResolveConfigBeads(context.Background(), lister, "claude-settings", subs)

	if count != 1 {
		t.Fatalf("expected 1 valid layer (skipping invalid), got %d", count)
	}
	if merged["model"] != "opus" {
		t.Errorf("expected model=opus, got %v", merged["model"])
	}
}

func TestResolveConfigBeads_UnknownCategory(t *testing.T) {
	lister := &mockConfigBeadLister{beads: []*beadsapi.BeadDetail{}}

	merged, count := ResolveConfigBeads(context.Background(), lister, "nonexistent", []string{"global"})
	if count != 0 || merged != nil {
		t.Error("expected nil for unknown category")
	}
}

func TestResolveConfigBeads_SpecificityOrder(t *testing.T) {
	// Verify that global < rig < role in merge order.
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "claude-settings",
				Labels:      []string{"role:captain"},
				Description: `{"model":"opus"}`,
			},
			{
				Title:       "claude-settings",
				Labels:      []string{"global"},
				Description: `{"model":"sonnet","extra":"yes"}`,
			},
			{
				Title:       "claude-settings",
				Labels:      []string{"rig:gasboat"},
				Description: `{"model":"haiku","rig":"gasboat"}`,
			},
		},
	}

	subs := []string{"global", "rig:gasboat", "role:captain"}
	merged, count := ResolveConfigBeads(context.Background(), lister, "claude-settings", subs)

	if count != 3 {
		t.Fatalf("expected 3 layers, got %d", count)
	}
	// role:captain is most specific, should win for model.
	if merged["model"] != "opus" {
		t.Errorf("expected model=opus (role wins), got %v", merged["model"])
	}
	// global's "extra" should survive.
	if merged["extra"] != "yes" {
		t.Error("expected extra=yes from global layer")
	}
	// rig's "rig" should survive (not overridden by role).
	if merged["rig"] != "gasboat" {
		t.Error("expected rig=gasboat from rig layer")
	}
}

