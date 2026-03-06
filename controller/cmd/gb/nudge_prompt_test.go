package main

import (
	"strings"
	"testing"
)

func TestDetectNudgeType(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		expected string
	}{
		{"default", nil, "default"},
		{"thread", map[string]string{"SLACK_THREAD_CHANNEL": "C123"}, "thread"},
		{"adhoc", map[string]string{"BOAT_PROMPT": "do something"}, "adhoc"},
		{"prewarmed", map[string]string{"BOAT_AGENT_STATE": "prewarmed"}, "prewarmed"},
		{"prewarmed over thread", map[string]string{
			"BOAT_AGENT_STATE":     "prewarmed",
			"SLACK_THREAD_CHANNEL": "C123",
		}, "prewarmed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear relevant env vars.
			for _, k := range []string{"SLACK_THREAD_CHANNEL", "BOAT_PROMPT", "BOAT_AGENT_STATE"} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got := detectNudgeType()
			if got != tt.expected {
				t.Errorf("detectNudgeType() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSubstituteNudgeVars(t *testing.T) {
	vars := nudgeVars{
		Project:      "myproject",
		ProjectHint:  " focus on myproject",
		TaskHint:     " task kd-123",
		MonorepoHint: " repos hint",
		BoatPrompt:   "do the thing",
	}

	tmpl := "Project: {{.Project}}, hint:{{.ProjectHint}}, task:{{.TaskHint}}, mono:{{.MonorepoHint}}, prompt:{{.BoatPrompt}}"
	got := substituteNudgeVars(tmpl, vars)
	expected := "Project: myproject, hint: focus on myproject, task: task kd-123, mono: repos hint, prompt:do the thing"

	if got != expected {
		t.Errorf("substituteNudgeVars() =\n%s\nwant:\n%s", got, expected)
	}
}

func TestHardcodedNudge_Default(t *testing.T) {
	vars := nudgeVars{Project: "gasboat"}
	got := hardcodedNudge("default", vars)
	if !strings.Contains(got, "gb ready") {
		t.Error("default nudge should mention gb ready")
	}
	if !strings.Contains(got, "kd claim") {
		t.Error("default nudge should mention kd claim")
	}
}

func TestHardcodedNudge_Thread(t *testing.T) {
	vars := nudgeVars{Project: "gasboat"}
	got := hardcodedNudge("thread", vars)
	if !strings.Contains(got, "thread-bound agent") {
		t.Error("thread nudge should mention thread-bound agent")
	}
	if !strings.Contains(got, "gb squawk") {
		t.Error("thread nudge should mention gb squawk")
	}
}

func TestHardcodedNudge_Adhoc(t *testing.T) {
	vars := nudgeVars{Project: "gasboat", BoatPrompt: "fix the bug"}
	got := hardcodedNudge("adhoc", vars)
	if !strings.Contains(got, "ad-hoc task") {
		t.Error("adhoc nudge should mention ad-hoc task")
	}
	if !strings.Contains(got, "fix the bug") {
		t.Error("adhoc nudge should include BOAT_PROMPT")
	}
}

func TestHardcodedNudge_Prewarmed(t *testing.T) {
	vars := nudgeVars{}
	got := hardcodedNudge("prewarmed", vars)
	if !strings.Contains(got, "prewarmed agent") {
		t.Error("prewarmed nudge should mention prewarmed agent")
	}
}

func TestHardcodedNudge_WithHints(t *testing.T) {
	vars := nudgeVars{
		Project:      "gasboat",
		ProjectHint:  " Focus on project gasboat.",
		TaskHint:     " Pre-assigned to kd-123.",
		MonorepoHint: " repos hint",
	}
	got := hardcodedNudge("default", vars)
	if !strings.Contains(got, "Focus on project gasboat.") {
		t.Error("default nudge should include project hint")
	}
	if !strings.Contains(got, "Pre-assigned to kd-123.") {
		t.Error("default nudge should include task hint")
	}
	if !strings.Contains(got, "repos hint") {
		t.Error("default nudge should include monorepo hint")
	}
}
