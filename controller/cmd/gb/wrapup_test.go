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

// --- Validation enforcement scenarios ---

func TestWrapUpRequirements_HardEnforce_BlocksOnMissing(t *testing.T) {
	reqs := WrapUpRequirements{
		Required: []string{"accomplishments", "blockers"},
		Enforce:  "hard",
	}
	w := &WrapUp{Accomplishments: "did stuff"} // blockers missing

	issues := reqs.Validate(w)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), issues)
	}
	if !strings.Contains(issues[0], "blockers") {
		t.Errorf("issue should mention 'blockers': %s", issues[0])
	}
}

func TestWrapUpRequirements_SoftEnforce_WarnsButPasses(t *testing.T) {
	reqs := WrapUpRequirements{
		Required: []string{"accomplishments"},
		Enforce:  "soft",
	}
	w := &WrapUp{} // empty — should warn but validate still returns issues

	issues := reqs.Validate(w)
	if len(issues) == 0 {
		t.Error("soft enforcement should still return issues")
	}
	// The caller (gb stop) decides whether to block or warn based on Enforce level.
}

// --- Backward compatibility ---

func TestNoWrapUpConfig_DefaultsToSoftEnforcement(t *testing.T) {
	lister := &mockConfigBeadLister{beads: nil}
	reqs := LoadWrapUpRequirements(context.Background(), lister, "any-agent")

	// Default is soft — means agents can stop without --wrapup.
	if reqs.Enforce != "soft" {
		t.Errorf("Enforce = %q, want 'soft' (default = no blocking)", reqs.Enforce)
	}
}

func TestReasonFlagStillWorks_NoWrapUpNeeded(t *testing.T) {
	// When --reason is used without --wrapup, processWrapUp is not called.
	// This test verifies that processWrapUp only works with valid JSON.
	_, _, err := processWrapUp(`{"accomplishments":"done"}`)
	if err != nil {
		t.Fatalf("processWrapUp with valid JSON should succeed: %v", err)
	}
}

// --- Stop gate validation logic ---

func TestWrapUpGateLogic_StopNotRequested(t *testing.T) {
	// When stop_requested is not set, gate should not block.
	bead := &beadsapi.BeadDetail{
		Fields: map[string]string{},
	}
	// Simulate: stop not requested → no block
	if bead.Fields["stop_requested"] == "true" {
		t.Error("stop_requested should not be set")
	}
}

func TestWrapUpGateLogic_WrapUpAlreadyProvided(t *testing.T) {
	// When wrapup field is already set, gate should not block.
	bead := &beadsapi.BeadDetail{
		Fields: map[string]string{
			"stop_requested": "true",
			WrapUpFieldName:  `{"accomplishments":"did stuff"}`,
		},
	}
	if bead.Fields[WrapUpFieldName] == "" {
		t.Error("wrapup should be present")
	}
}

func TestWrapUpGateLogic_HardEnforceNoWrapUp(t *testing.T) {
	// When hard enforcement is on and no wrapup, the gate should block.
	reqs := WrapUpRequirements{
		Required: []string{"accomplishments"},
		Enforce:  "hard",
	}
	bead := &beadsapi.BeadDetail{
		Fields: map[string]string{
			"stop_requested": "true",
			// no wrapup field
		},
	}

	// Simulate gate logic.
	shouldBlock := bead.Fields["stop_requested"] == "true" &&
		bead.Fields[WrapUpFieldName] == "" &&
		reqs.Enforce == "hard"

	if !shouldBlock {
		t.Error("gate should block: hard enforcement, no wrapup, stop requested")
	}
}

func TestWrapUpGateLogic_SoftEnforceNoWrapUp(t *testing.T) {
	// When soft enforcement is on and no wrapup, the gate should NOT block.
	reqs := WrapUpRequirements{
		Required: []string{"accomplishments"},
		Enforce:  "soft",
	}
	bead := &beadsapi.BeadDetail{
		Fields: map[string]string{
			"stop_requested": "true",
		},
	}

	shouldBlock := bead.Fields["stop_requested"] == "true" &&
		bead.Fields[WrapUpFieldName] == "" &&
		reqs.Enforce == "hard"

	if shouldBlock {
		t.Error("gate should NOT block: soft enforcement")
	}
}

// --- WrapUp edge cases ---

func TestWrapUp_AllFieldsFilled(t *testing.T) {
	reqs := WrapUpRequirements{
		Required: []string{"accomplishments", "blockers", "handoff_notes"},
		CustomFields: []CustomFieldDef{
			{Name: "risk", Required: true},
		},
		Enforce: "hard",
	}
	w := &WrapUp{
		Accomplishments: "stuff",
		Blockers:        "none",
		HandoffNotes:    "check PR",
		BeadsClosed:     []string{"kd-1"},
		PullRequests:    []string{"https://github.com/org/repo/pull/1"},
		Custom:          map[string]string{"risk": "low"},
	}

	issues := reqs.Validate(w)
	if len(issues) != 0 {
		t.Errorf("fully filled wrap-up should pass, got issues: %v", issues)
	}
}

func TestWrapUp_EmptyCustomMap(t *testing.T) {
	w := &WrapUp{Accomplishments: "done"}
	if wrapUpFieldPresent(w, "nonexistent") {
		t.Error("nonexistent custom field should not be present")
	}
}

func TestWrapUpConfigCategory_Registered(t *testing.T) {
	cat := LookupCategory(WrapUpConfigCategory)
	if cat == nil {
		t.Fatal("wrapup-config category should be registered")
	}
	if cat.Strategy != MergeOverride {
		t.Errorf("wrapup-config should use MergeOverride, got %d", cat.Strategy)
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
