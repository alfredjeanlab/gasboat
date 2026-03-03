package main

import (
	"testing"
)

func TestSpawnIsValidAgentName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid lowercase", "my-agent", true},
		{"valid with numbers", "agent-42", true},
		{"valid single word", "worker", true},
		{"empty", "", false},
		{"uppercase", "MyAgent", false},
		{"spaces", "my agent", false},
		{"underscores", "my_agent", false},
		{"special chars", "agent@1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := spawnIsValidAgentName(tt.input); got != tt.want {
				t.Errorf("spawnIsValidAgentName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSpawnGenerateAgentName(t *testing.T) {
	tests := []struct {
		name  string
		title string
	}{
		{"simple title", "Fix authentication bug"},
		{"long title", "Implement the new feature with many words in the title"},
		{"special chars", "Update API: handle /users endpoint"},
		{"empty title", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := spawnGenerateAgentName(tt.title)
			if name == "" {
				t.Error("expected non-empty name")
			}
			// Should end with a 3-char random suffix after a hyphen.
			if len(name) < 5 { // at least "x-abc"
				t.Errorf("name too short: %q", name)
			}
			// Should be a valid agent name.
			if !spawnIsValidAgentName(name) {
				t.Errorf("generated name %q is not a valid agent name", name)
			}
		})
	}
}

func TestSpawnProjectFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"with project", []string{"role:crew", "project:gasboat"}, "gasboat"},
		{"no project", []string{"role:crew"}, ""},
		{"empty", nil, ""},
		{"multiple labels", []string{"project:monorepo", "role:engineer"}, "monorepo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := spawnProjectFromLabels(tt.labels); got != tt.want {
				t.Errorf("spawnProjectFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}
