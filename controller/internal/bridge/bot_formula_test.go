package bridge

import (
	"testing"
)

func TestFormulaBuildStepLabels_SameProject(t *testing.T) {
	molLabels := []string{"project:gasboat"}
	stepLabels := []string{"team:alpha"}
	got := formulaBuildStepLabels(molLabels, stepLabels, "gasboat", "gasboat")

	want := map[string]bool{"project:gasboat": true, "team:alpha": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d labels, got %d: %v", len(want), len(got), got)
	}
	for _, l := range got {
		if !want[l] {
			t.Errorf("unexpected label %q", l)
		}
	}
}

func TestFormulaBuildStepLabels_DifferentProject(t *testing.T) {
	molLabels := []string{"project:gasboat"}
	stepLabels := []string{"team:alpha"}
	got := formulaBuildStepLabels(molLabels, stepLabels, "infra", "gasboat")

	want := map[string]bool{"project:infra": true, "team:alpha": true}
	if len(got) != len(want) {
		t.Fatalf("expected %d labels, got %d: %v", len(want), len(got), got)
	}
	for _, l := range got {
		if !want[l] {
			t.Errorf("unexpected label %q", l)
		}
	}
	// Ensure gasboat project label was removed.
	for _, l := range got {
		if l == "project:gasboat" {
			t.Error("molecule project label should have been replaced")
		}
	}
}

func TestFormulaBuildStepLabels_NoStepLabels(t *testing.T) {
	molLabels := []string{"project:gasboat", "priority:high"}
	got := formulaBuildStepLabels(molLabels, nil, "gasboat", "gasboat")

	if len(got) != 2 {
		t.Fatalf("expected 2 labels, got %d: %v", len(got), got)
	}
}

func TestFormulaBuildStepLabels_NoDuplicates(t *testing.T) {
	molLabels := []string{"project:gasboat"}
	stepLabels := []string{"project:gasboat", "extra:tag"}
	got := formulaBuildStepLabels(molLabels, stepLabels, "gasboat", "gasboat")

	count := 0
	for _, l := range got {
		if l == "project:gasboat" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 project:gasboat label, got %d", count)
	}
}

func TestFormulaEvalCondition_Basic(t *testing.T) {
	vars := map[string]string{"env": "prod", "debug": "true"}

	tests := []struct {
		cond string
		want bool
	}{
		{"{{env}} == prod", true},
		{"{{env}} == staging", false},
		{"{{env}} != staging", true},
		{"{{debug}}", true},
		{"!{{debug}}", false},
		{"{{missing}}", false},
	}

	for _, tc := range tests {
		got := formulaEvalCondition(tc.cond, vars)
		if got != tc.want {
			t.Errorf("formulaEvalCondition(%q) = %v, want %v", tc.cond, got, tc.want)
		}
	}
}

func TestFormulaSubstituteVars(t *testing.T) {
	vars := map[string]string{"project": "infra", "role": "crew"}

	tests := []struct {
		input string
		want  string
	}{
		{"Deploy to {{project}}", "Deploy to infra"},
		{"{{role}} agent", "crew agent"},
		{"no vars here", "no vars here"},
		{"{{missing}} stays", "{{missing}} stays"},
	}

	for _, tc := range tests {
		got := formulaSubstituteVars(tc.input, vars)
		if got != tc.want {
			t.Errorf("formulaSubstituteVars(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFormulaStepRoleProjectFields(t *testing.T) {
	// Verify the new fields parse correctly from JSON-like struct.
	step := formulaStep{
		ID:              "deploy",
		Title:           "Deploy",
		Role:            "crew",
		Project:         "infra",
		SuggestNewAgent: true,
	}

	if step.Role != "crew" {
		t.Errorf("expected role=crew, got %s", step.Role)
	}
	if step.Project != "infra" {
		t.Errorf("expected project=infra, got %s", step.Project)
	}
	if !step.SuggestNewAgent {
		t.Error("expected suggest_new_agent=true")
	}
}
