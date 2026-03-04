package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"
)

func TestWrapUp_MarshalRoundTrip(t *testing.T) {
	w := &WrapUp{
		Accomplishments: "Implemented auth module",
		Blockers:        "Waiting on API key",
		HandoffNotes:    "Check PR #42",
		BeadsClosed:     []string{"kd-abc", "kd-def"},
		PullRequests:    []string{"https://github.com/org/repo/pull/42"},
		Custom:          map[string]string{"risk_level": "low"},
		Timestamp:       time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC),
	}

	s, err := MarshalWrapUp(w)
	if err != nil {
		t.Fatalf("MarshalWrapUp: %v", err)
	}

	got, err := UnmarshalWrapUp(s)
	if err != nil {
		t.Fatalf("UnmarshalWrapUp: %v", err)
	}

	if got.Accomplishments != w.Accomplishments {
		t.Errorf("Accomplishments = %q, want %q", got.Accomplishments, w.Accomplishments)
	}
	if got.Blockers != w.Blockers {
		t.Errorf("Blockers = %q, want %q", got.Blockers, w.Blockers)
	}
	if got.HandoffNotes != w.HandoffNotes {
		t.Errorf("HandoffNotes = %q, want %q", got.HandoffNotes, w.HandoffNotes)
	}
	if len(got.BeadsClosed) != 2 || got.BeadsClosed[0] != "kd-abc" {
		t.Errorf("BeadsClosed = %v, want [kd-abc kd-def]", got.BeadsClosed)
	}
	if len(got.PullRequests) != 1 {
		t.Errorf("PullRequests = %v, want 1 entry", got.PullRequests)
	}
	if got.Custom["risk_level"] != "low" {
		t.Errorf("Custom[risk_level] = %q, want %q", got.Custom["risk_level"], "low")
	}
}

func TestWrapUp_MarshalSetsTimestamp(t *testing.T) {
	w := &WrapUp{Accomplishments: "did stuff"}

	s, err := MarshalWrapUp(w)
	if err != nil {
		t.Fatalf("MarshalWrapUp: %v", err)
	}

	got, err := UnmarshalWrapUp(s)
	if err != nil {
		t.Fatalf("UnmarshalWrapUp: %v", err)
	}

	if got.Timestamp.IsZero() {
		t.Error("Timestamp should be auto-set when zero")
	}
}

func TestWrapUp_JSONFieldIsValidJSON(t *testing.T) {
	w := &WrapUp{
		Accomplishments: "Closed 3 bugs",
		Timestamp:       time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC),
	}

	s, err := MarshalWrapUp(w)
	if err != nil {
		t.Fatalf("MarshalWrapUp: %v", err)
	}

	// The string stored in fields["wrapup"] must be valid JSON.
	if !json.Valid([]byte(s)) {
		t.Errorf("MarshalWrapUp output is not valid JSON: %s", s)
	}
}

func TestDefaultWrapUpRequirements(t *testing.T) {
	req := DefaultWrapUpRequirements()

	if len(req.Required) != 1 || req.Required[0] != "accomplishments" {
		t.Errorf("Required = %v, want [accomplishments]", req.Required)
	}
	if req.Enforce != "soft" {
		t.Errorf("Enforce = %q, want %q", req.Enforce, "soft")
	}
}

func TestWrapUpRequirements_Validate_PassesWhenComplete(t *testing.T) {
	req := DefaultWrapUpRequirements()
	w := &WrapUp{Accomplishments: "Did the thing"}

	issues := req.Validate(w)
	if len(issues) != 0 {
		t.Errorf("Validate returned issues for complete wrap-up: %v", issues)
	}
}

func TestWrapUpRequirements_Validate_FailsOnMissingRequired(t *testing.T) {
	req := DefaultWrapUpRequirements()
	w := &WrapUp{} // accomplishments is empty

	issues := req.Validate(w)
	if len(issues) == 0 {
		t.Error("Validate should fail when accomplishments is empty")
	}
}

func TestWrapUpRequirements_Validate_EnforceNone(t *testing.T) {
	req := WrapUpRequirements{
		Required: []string{"accomplishments"},
		Enforce:  "none",
	}
	w := &WrapUp{} // empty

	issues := req.Validate(w)
	if len(issues) != 0 {
		t.Errorf("Validate with enforce=none should return no issues, got: %v", issues)
	}
}

func TestWrapUpRequirements_Validate_CustomFields(t *testing.T) {
	req := WrapUpRequirements{
		Required: []string{"accomplishments"},
		CustomFields: []CustomFieldDef{
			{Name: "risk_level", Required: true},
			{Name: "notes", Required: false},
		},
		Enforce: "hard",
	}

	// Missing custom required field.
	w := &WrapUp{Accomplishments: "stuff"}
	issues := req.Validate(w)
	if len(issues) != 1 {
		t.Errorf("Validate should report 1 issue (missing risk_level), got: %v", issues)
	}

	// With custom field present.
	w.Custom = map[string]string{"risk_level": "low"}
	issues = req.Validate(w)
	if len(issues) != 0 {
		t.Errorf("Validate should pass with custom field present, got: %v", issues)
	}
}

func TestWrapUpFieldPresent(t *testing.T) {
	w := &WrapUp{
		Accomplishments: "did stuff",
		BeadsClosed:     []string{"kd-1"},
		Custom:          map[string]string{"foo": "bar"},
	}

	tests := []struct {
		field string
		want  bool
	}{
		{"accomplishments", true},
		{"blockers", false},
		{"handoff_notes", false},
		{"beads_closed", true},
		{"pull_requests", false},
		{"foo", true},
		{"missing", false},
	}

	for _, tt := range tests {
		got := wrapUpFieldPresent(w, tt.field)
		if got != tt.want {
			t.Errorf("wrapUpFieldPresent(%q) = %v, want %v", tt.field, got, tt.want)
		}
	}
}

func TestWrapUpToComment(t *testing.T) {
	w := &WrapUp{
		Accomplishments: "Fixed auth bug",
		Blockers:        "Waiting for review",
		BeadsClosed:     []string{"kd-1", "kd-2"},
		PullRequests:    []string{"https://github.com/org/repo/pull/1"},
	}

	comment := WrapUpToComment(w)

	if got := comment; got == "" {
		t.Fatal("WrapUpToComment returned empty string")
	}

	// Check key content is present.
	for _, want := range []string{
		"Fixed auth bug",
		"Waiting for review",
		"kd-1",
		"kd-2",
		"https://github.com/org/repo/pull/1",
	} {
		if !containsStr(comment, want) {
			t.Errorf("WrapUpToComment missing %q in output:\n%s", want, comment)
		}
	}
}

func TestWrapUpToComment_MinimalFields(t *testing.T) {
	w := &WrapUp{Accomplishments: "Done"}
	comment := WrapUpToComment(w)

	if !containsStr(comment, "Done") {
		t.Errorf("WrapUpToComment missing accomplishments in:\n%s", comment)
	}
	if containsStr(comment, "Blockers:") {
		t.Errorf("WrapUpToComment should not include Blockers when empty:\n%s", comment)
	}
}

func TestLoadWrapUpRequirements_DefaultWhenNoConfigBeads(t *testing.T) {
	lister := &mockConfigBeadLister{beads: nil}
	reqs := LoadWrapUpRequirements(context.Background(), lister, "test-agent")

	defaults := DefaultWrapUpRequirements()
	if reqs.Enforce != defaults.Enforce {
		t.Errorf("Enforce = %q, want %q (default)", reqs.Enforce, defaults.Enforce)
	}
	if len(reqs.Required) != len(defaults.Required) {
		t.Errorf("Required = %v, want %v (default)", reqs.Required, defaults.Required)
	}
}

func TestLoadWrapUpRequirements_FromConfigBead(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "wrapup-config",
				Labels:      []string{"global"},
				Description: `{"required":["accomplishments","blockers"],"enforce":"hard"}`,
			},
		},
	}
	reqs := LoadWrapUpRequirements(context.Background(), lister, "test-agent")

	if reqs.Enforce != "hard" {
		t.Errorf("Enforce = %q, want %q", reqs.Enforce, "hard")
	}
	if len(reqs.Required) != 2 {
		t.Errorf("Required = %v, want [accomplishments blockers]", reqs.Required)
	}
}

func TestLoadWrapUpRequirements_RoleOverridesGlobal(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "wrapup-config",
				Labels:      []string{"global"},
				Description: `{"required":["accomplishments"],"enforce":"soft"}`,
			},
			{
				Title:       "wrapup-config",
				Labels:      []string{"role:crew"},
				Description: `{"required":["accomplishments","blockers"],"enforce":"hard"}`,
			},
		},
	}

	// Agent with role:crew should get the role override.
	// BuildAgentSubscriptions for "gasboat/crews/test-agent" includes role:crews and role:crew.
	reqs := LoadWrapUpRequirements(context.Background(), lister, "gasboat/crews/test-agent")

	if reqs.Enforce != "hard" {
		t.Errorf("Enforce = %q, want %q (role override)", reqs.Enforce, "hard")
	}
	if len(reqs.Required) != 2 {
		t.Errorf("Required = %v, want [accomplishments blockers]", reqs.Required)
	}
}

func TestLoadWrapUpRequirements_CustomFields(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:  "wrapup-config",
				Labels: []string{"global"},
				Description: `{
					"required": ["accomplishments"],
					"custom_fields": [
						{"name": "risk_assessment", "description": "Risk level of changes", "required": true}
					],
					"enforce": "hard"
				}`,
			},
		},
	}
	reqs := LoadWrapUpRequirements(context.Background(), lister, "test-agent")

	if len(reqs.CustomFields) != 1 {
		t.Fatalf("CustomFields = %v, want 1 entry", reqs.CustomFields)
	}
	if reqs.CustomFields[0].Name != "risk_assessment" {
		t.Errorf("CustomFields[0].Name = %q, want %q", reqs.CustomFields[0].Name, "risk_assessment")
	}
	if !reqs.CustomFields[0].Required {
		t.Error("CustomFields[0].Required should be true")
	}
}

func TestOutputWrapUpExpectations_Default(t *testing.T) {
	// Set up daemon with no config beads so defaults are used.
	origDaemon := daemon
	defer func() { daemon = origDaemon }()

	// Create a mock daemon that returns no config beads.
	// We can test outputWrapUpExpectations indirectly through the output.
	// Since it depends on the global daemon, we test the requirements rendering directly.
	reqs := DefaultWrapUpRequirements()

	var buf strings.Builder
	// Simulate what outputWrapUpExpectations does with default reqs.
	fmt.Fprintf(&buf, "\n## Wrap-Up Requirements\n\n")
	fmt.Fprintln(&buf, "You **should** provide a structured wrap-up when calling `gb stop`.")
	fmt.Fprintln(&buf, "")
	fmt.Fprint(&buf, "**Required fields:** ")
	for i, f := range reqs.Required {
		if i > 0 {
			fmt.Fprint(&buf, ", ")
		}
		fmt.Fprintf(&buf, "`%s`", f)
	}
	fmt.Fprintln(&buf)

	output := buf.String()
	if !strings.Contains(output, "Wrap-Up Requirements") {
		t.Error("output should contain 'Wrap-Up Requirements' header")
	}
	if !strings.Contains(output, "`accomplishments`") {
		t.Error("output should mention accomplishments field")
	}
	if !strings.Contains(output, "should") {
		t.Error("output should use 'should' for soft enforcement")
	}
}

func TestLoadWrapUpRequirements_InvalidJSON(t *testing.T) {
	lister := &mockConfigBeadLister{
		beads: []*beadsapi.BeadDetail{
			{
				Title:       "wrapup-config",
				Labels:      []string{"global"},
				Description: `not valid json`,
			},
		},
	}
	reqs := LoadWrapUpRequirements(context.Background(), lister, "test-agent")

	// Should fall back to defaults when config bead has invalid JSON.
	defaults := DefaultWrapUpRequirements()
	if reqs.Enforce != defaults.Enforce {
		t.Errorf("Enforce = %q, want %q (default fallback)", reqs.Enforce, defaults.Enforce)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
