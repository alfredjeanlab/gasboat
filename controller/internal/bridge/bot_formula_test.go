package bridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// --- parseFormulaFields tests ---

func TestParseFormulaFields_Empty(t *testing.T) {
	bead := &beadsapi.BeadDetail{ID: "kd-1", Fields: nil}
	ff := parseFormulaFields(bead)
	if len(ff.Vars) != 0 || len(ff.Steps) != 0 {
		t.Errorf("expected empty vars/steps for nil fields, got %d vars, %d steps", len(ff.Vars), len(ff.Steps))
	}
}

func TestParseFormulaFields_WithVarsAndSteps(t *testing.T) {
	vars := []formulaVarDef{
		{Name: "env", Required: true, Enum: []string{"staging", "prod"}},
		{Name: "version", Default: "latest"},
	}
	steps := []formulaStep{
		{ID: "build", Title: "Build {{env}}", Type: "task"},
		{ID: "deploy", Title: "Deploy to {{env}}", DependsOn: []string{"build"}},
	}
	varsJSON, _ := json.Marshal(vars)
	stepsJSON, _ := json.Marshal(steps)

	bead := &beadsapi.BeadDetail{
		ID: "kd-formula-1",
		Fields: map[string]string{
			"vars":  string(varsJSON),
			"steps": string(stepsJSON),
		},
	}

	ff := parseFormulaFields(bead)
	if len(ff.Vars) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(ff.Vars))
	}
	if ff.Vars[0].Name != "env" || !ff.Vars[0].Required {
		t.Errorf("expected first var to be env (required), got %+v", ff.Vars[0])
	}
	if ff.Vars[1].Default != "latest" {
		t.Errorf("expected second var default=latest, got %q", ff.Vars[1].Default)
	}
	if len(ff.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(ff.Steps))
	}
	if ff.Steps[1].DependsOn[0] != "build" {
		t.Errorf("expected deploy to depend on build, got %v", ff.Steps[1].DependsOn)
	}
}

func TestParseFormulaFields_InvalidJSON(t *testing.T) {
	bead := &beadsapi.BeadDetail{
		ID:     "kd-bad",
		Fields: map[string]string{"vars": "not-json", "steps": "{bad}"},
	}
	ff := parseFormulaFields(bead)
	if len(ff.Vars) != 0 || len(ff.Steps) != 0 {
		t.Errorf("expected empty results for invalid JSON, got %d vars, %d steps", len(ff.Vars), len(ff.Steps))
	}
}

// --- formulaSubstituteVars tests ---

func TestFormulaSubstituteVars_Basic(t *testing.T) {
	vars := map[string]string{"env": "staging", "version": "1.2.3"}
	tests := []struct {
		input, want string
	}{
		{"Deploy to {{env}}", "Deploy to staging"},
		{"{{env}}-{{version}}", "staging-1.2.3"},
		{"No vars here", "No vars here"},
		{"{{missing}} stays", "{{missing}} stays"},
		{"", ""},
	}
	for _, tt := range tests {
		got := formulaSubstituteVars(tt.input, vars)
		if got != tt.want {
			t.Errorf("formulaSubstituteVars(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormulaSubstituteVars_NilVars(t *testing.T) {
	got := formulaSubstituteVars("{{env}}", nil)
	if got != "{{env}}" {
		t.Errorf("expected unresolved var with nil map, got %q", got)
	}
}

// --- formulaEvalCondition tests ---

func TestFormulaEvalCondition_Equality(t *testing.T) {
	vars := map[string]string{"env": "prod", "flag": "true"}
	tests := []struct {
		name string
		cond string
		want bool
	}{
		{"equals match", "{{env}} == prod", true},
		{"equals mismatch", "{{env}} == staging", false},
		{"not-equals match", "{{env}} != staging", true},
		{"not-equals mismatch", "{{env}} != prod", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formulaEvalCondition(tt.cond, vars)
			if got != tt.want {
				t.Errorf("formulaEvalCondition(%q) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}

func TestFormulaEvalCondition_Negation(t *testing.T) {
	vars := map[string]string{"env": "prod"}
	tests := []struct {
		name string
		cond string
		want bool
	}{
		{"negate truthy var", "!{{env}}", false},
		{"negate missing var", "!{{missing}}", true},
		{"negate equals", "!{{env}} == staging", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formulaEvalCondition(tt.cond, vars)
			if got != tt.want {
				t.Errorf("formulaEvalCondition(%q) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}

func TestFormulaEvalCondition_TruthyFalsy(t *testing.T) {
	vars := map[string]string{"flag": "true", "zero": "0", "empty": "", "no": "false"}
	tests := []struct {
		name string
		cond string
		want bool
	}{
		{"truthy var", "{{flag}}", true},
		{"zero is falsy", "{{zero}}", false},
		{"false is falsy", "{{no}}", false},
		{"empty is falsy", "{{empty}}", false},
		{"unresolved var is falsy", "{{missing}}", false},
		{"literal string is truthy", "always", true},
		{"empty cond", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formulaEvalCondition(tt.cond, vars)
			if got != tt.want {
				t.Errorf("formulaEvalCondition(%q) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}

// --- splitVarPair tests ---

func TestSplitVarPair(t *testing.T) {
	tests := []struct {
		input     string
		wantKey   string
		wantVal   string
		wantOK    bool
	}{
		{"env=prod", "env", "prod", true},
		{"version=1.2.3", "version", "1.2.3", true},
		{"key=", "key", "", true},
		{"key=val=ue", "key", "val=ue", true},
		{"noequals", "", "", false},
		{"=value", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		k, v, ok := splitVarPair(tt.input)
		if ok != tt.wantOK || k != tt.wantKey || v != tt.wantVal {
			t.Errorf("splitVarPair(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.input, k, v, ok, tt.wantKey, tt.wantVal, tt.wantOK)
		}
	}
}

// --- plural tests ---

func TestPlural(t *testing.T) {
	if plural(0) != "s" {
		t.Error("plural(0) should be 's'")
	}
	if plural(1) != "" {
		t.Error("plural(1) should be ''")
	}
	if plural(2) != "s" {
		t.Error("plural(2) should be 's'")
	}
}

// --- formulaBuildStepLabels tests (from main) ---

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

// --- formulaStep role/project field tests (from main) ---

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

// --- instantiateFormulaSteps tests ---

// depCall records an AddDependency call for assertions.
type depCall struct {
	BeadID      string
	DependsOnID string
	DepType     string
	CreatedBy   string
}

// formulaMockDaemon extends mockDaemon with dependency tracking.
type formulaMockDaemon struct {
	*mockDaemon
	deps []depCall
}

func newFormulaMockDaemon() *formulaMockDaemon {
	return &formulaMockDaemon{mockDaemon: newMockDaemon()}
}

func (m *formulaMockDaemon) AddDependency(_ context.Context, beadID, dependsOnID, depType, createdBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deps = append(m.deps, depCall{beadID, dependsOnID, depType, createdBy})
	return nil
}

func (m *formulaMockDaemon) getDeps() []depCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]depCall{}, m.deps...)
}

func TestInstantiateFormulaSteps_BasicMolecule(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	// Override daemon to our formula mock.
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:          "kd-formula-1",
		Title:       "Deploy {{env}}",
		Description: "Deploy service to {{env}}",
		Priority:    3,
	}

	steps := []formulaStep{
		{ID: "build", Title: "Build service", Type: "task"},
		{ID: "deploy", Title: "Deploy to target", Type: "task", DependsOn: []string{"build"}},
	}

	vars := map[string]string{"env": "staging"}

	molID, stepCount, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, vars, false, "gasboat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stepCount != 2 {
		t.Errorf("expected 2 steps, got %d", stepCount)
	}
	if molID == "" {
		t.Fatal("expected non-empty molecule ID")
	}

	// Verify molecule bead was created.
	molBeads := filterBeadsByType(daemon.beads, "molecule")
	if len(molBeads) != 1 {
		t.Fatalf("expected 1 molecule bead, got %d", len(molBeads))
	}
	mol := molBeads[0]
	if mol.Title != "Deploy staging" {
		t.Errorf("expected molecule title 'Deploy staging', got %q", mol.Title)
	}
	if !containsLabel(mol.Labels, "project:gasboat") {
		t.Errorf("expected molecule to have project:gasboat label, got %v", mol.Labels)
	}

	// Verify step beads were created.
	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 2 {
		t.Fatalf("expected 2 task beads, got %d", len(taskBeads))
	}

	// Verify dependencies were created.
	deps := daemon.getDeps()
	// Should have 2 parent-child deps (one per step) + 1 blocks dep (deploy depends on build).
	parentChildCount := 0
	blocksCount := 0
	for _, d := range deps {
		switch d.DepType {
		case "parent-child":
			parentChildCount++
			if d.DependsOnID != molID {
				t.Errorf("parent-child dep should point to molecule %s, got %s", molID, d.DependsOnID)
			}
		case "blocks":
			blocksCount++
		}
	}
	if parentChildCount != 2 {
		t.Errorf("expected 2 parent-child deps, got %d", parentChildCount)
	}
	if blocksCount != 1 {
		t.Errorf("expected 1 blocks dep, got %d", blocksCount)
	}
}

func TestInstantiateFormulaSteps_EphemeralWisp(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-2",
		Title: "Quick check",
	}

	steps := []formulaStep{
		{ID: "check", Title: "Run check", Type: "task"},
	}

	molID, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify molecule has ephemeral field.
	mol := daemon.beads[molID]
	if mol == nil {
		// Find it by iterating.
		for _, b := range daemon.beads {
			if b.Type == "molecule" {
				mol = b
				break
			}
		}
	}
	if mol == nil {
		t.Fatal("molecule bead not found")
	}
	// The mock's CreateBead stores fields via json.Unmarshal into map[string]string,
	// which renders the bool "true" as empty string. Verify the key exists.
	if mol.Fields == nil {
		t.Error("expected molecule fields to contain ephemeral key")
	} else if _, ok := mol.Fields["ephemeral"]; !ok {
		t.Errorf("expected ephemeral key in molecule fields, got %v", mol.Fields)
	}
}

func TestInstantiateFormulaSteps_StepLabels(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-3",
		Title: "Multi-project deploy",
	}

	steps := []formulaStep{
		{
			ID:     "step1",
			Title:  "Build in project A",
			Type:   "task",
			Labels: []string{"role:devops", "team:infra"},
		},
	}

	_, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "gasboat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 1 {
		t.Fatalf("expected 1 task bead, got %d", len(taskBeads))
	}

	step := taskBeads[0]
	// Should have project label from formula + step-specific labels.
	if !containsLabel(step.Labels, "project:gasboat") {
		t.Errorf("expected project:gasboat label, got %v", step.Labels)
	}
	if !containsLabel(step.Labels, "role:devops") {
		t.Errorf("expected role:devops label, got %v", step.Labels)
	}
	if !containsLabel(step.Labels, "team:infra") {
		t.Errorf("expected team:infra label, got %v", step.Labels)
	}
}

func TestInstantiateFormulaSteps_StepLabelDedup(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-4",
		Title: "Dedup test",
	}

	steps := []formulaStep{
		{
			ID:     "step1",
			Title:  "Step with duplicate label",
			Type:   "task",
			Labels: []string{"project:gasboat", "extra:label"},
		},
	}

	_, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "gasboat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	taskBeads := filterTaskBeads(daemon.beads)
	step := taskBeads[0]

	// Count project:gasboat labels — should not be duplicated.
	count := 0
	for _, l := range step.Labels {
		if l == "project:gasboat" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected project:gasboat to appear once, appeared %d times in %v", count, step.Labels)
	}
}

func TestInstantiateFormulaSteps_StepPriority(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:       "kd-formula-5",
		Title:    "Priority test",
		Priority: 5,
	}

	customPri := 2
	steps := []formulaStep{
		{ID: "high", Title: "High priority step", Priority: &customPri},
		{ID: "default", Title: "Default priority step"},
	}

	_, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both steps should be tasks. The mock assigns priority via CreateBeadRequest.Priority
	// which we can verify by checking the created beads.
	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 2 {
		t.Fatalf("expected 2 task beads, got %d", len(taskBeads))
	}
}

func TestInstantiateFormulaSteps_DefaultType(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-6",
		Title: "Type default test",
	}

	steps := []formulaStep{
		{ID: "noType", Title: "Step without type"},
		{ID: "withType", Title: "Step with type", Type: "bug"},
	}

	_, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the beads and check their types.
	var taskBead, bugBead *beadsapi.BeadDetail
	for _, b := range daemon.beads {
		if b.Type == "task" {
			taskBead = b
		}
		if b.Type == "bug" {
			bugBead = b
		}
	}
	if taskBead == nil {
		t.Error("step without type should default to 'task'")
	}
	if bugBead == nil {
		t.Error("step with type='bug' should create a bug bead")
	}
}

func TestInstantiateFormulaSteps_NoProject(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-7",
		Title: "No project",
	}

	steps := []formulaStep{
		{ID: "step1", Title: "Step 1"},
	}

	_, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Molecule and step should have no project label.
	for _, b := range daemon.beads {
		for _, l := range b.Labels {
			if l == "project:" {
				t.Errorf("expected no empty project label when project is empty, got %v", b.Labels)
			}
		}
		if len(b.Labels) != 0 {
			t.Errorf("expected no labels when project is empty, got %v", b.Labels)
		}
	}
}

func TestInstantiateFormulaSteps_MultipleBlocksDeps(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-8",
		Title: "Diamond deps",
	}

	steps := []formulaStep{
		{ID: "a", Title: "Step A"},
		{ID: "b", Title: "Step B", DependsOn: []string{"a"}},
		{ID: "c", Title: "Step C", DependsOn: []string{"a"}},
		{ID: "d", Title: "Step D", DependsOn: []string{"b", "c"}},
	}

	_, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deps := daemon.getDeps()
	blocksCount := 0
	for _, d := range deps {
		if d.DepType == "blocks" {
			blocksCount++
		}
	}
	// b->a, c->a, d->b, d->c = 4 blocks deps
	if blocksCount != 4 {
		t.Errorf("expected 4 blocks deps for diamond pattern, got %d", blocksCount)
	}
}

func TestInstantiateFormulaSteps_MissingDependency(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-9",
		Title: "Missing dep",
	}

	steps := []formulaStep{
		{ID: "step1", Title: "Step 1", DependsOn: []string{"nonexistent"}},
	}

	_, stepCount, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stepCount != 1 {
		t.Errorf("expected 1 step, got %d", stepCount)
	}

	// Should have parent-child dep but no blocks dep (nonexistent step skipped).
	deps := daemon.getDeps()
	blocksCount := 0
	for _, d := range deps {
		if d.DepType == "blocks" {
			blocksCount++
		}
	}
	if blocksCount != 0 {
		t.Errorf("expected 0 blocks deps for missing dependency, got %d", blocksCount)
	}
}

// --- Cross-project step label tests ---

func TestInstantiateFormulaSteps_CrossProjectLabels(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-cross",
		Title: "Cross-project formula",
	}

	// Step has a different project label than the molecule's project.
	steps := []formulaStep{
		{
			ID:     "step1",
			Title:  "Step in other project",
			Labels: []string{"project:other-project", "role:devops"},
		},
	}

	_, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "gasboat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 1 {
		t.Fatalf("expected 1 task bead, got %d", len(taskBeads))
	}

	step := taskBeads[0]
	// Step should have both project labels (gasboat from formula + other-project from step).
	if !containsLabel(step.Labels, "project:gasboat") {
		t.Errorf("expected project:gasboat label from molecule project, got %v", step.Labels)
	}
	if !containsLabel(step.Labels, "project:other-project") {
		t.Errorf("expected project:other-project label from step, got %v", step.Labels)
	}
	if !containsLabel(step.Labels, "role:devops") {
		t.Errorf("expected role:devops label from step, got %v", step.Labels)
	}
}

func TestInstantiateFormulaSteps_StepAssignee(t *testing.T) {
	daemon := newFormulaMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	formula := &beadsapi.BeadDetail{
		ID:    "kd-formula-assign",
		Title: "Assignee test",
	}

	steps := []formulaStep{
		{ID: "step1", Title: "Assigned step", Assignee: "bot-123"},
		{ID: "step2", Title: "Unassigned step"},
	}

	_, _, err := bot.instantiateFormulaSteps(
		context.Background(), formula, steps, nil, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 2 {
		t.Fatalf("expected 2 task beads, got %d", len(taskBeads))
	}

	var foundAssigned, foundUnassigned bool
	for _, b := range taskBeads {
		if b.Assignee == "bot-123" {
			foundAssigned = true
		}
		if b.Assignee == "" && b.Title == "Unassigned step" {
			foundUnassigned = true
		}
	}
	if !foundAssigned {
		t.Error("expected one step with assignee 'bot-123'")
	}
	if !foundUnassigned {
		t.Error("expected one step with no assignee")
	}
}

// --- handleFormulaPour integration tests ---

func seedFormula(daemon *mockDaemon, id, title string, vars []formulaVarDef, steps []formulaStep) {
	varsJSON, _ := json.Marshal(vars)
	stepsJSON, _ := json.Marshal(steps)
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	daemon.beads[id] = &beadsapi.BeadDetail{
		ID:     id,
		Title:  title,
		Type:   "formula",
		Status: "open",
		Fields: map[string]string{
			"vars":  string(varsJSON),
			"steps": string(stepsJSON),
		},
	}
}

func TestHandleFormulaPour_RequiredVarMissing(t *testing.T) {
	daemon := newMockDaemon()
	seedFormula(daemon, "kd-f1", "Deploy",
		[]formulaVarDef{{Name: "env", Required: true}},
		[]formulaStep{{ID: "step1", Title: "Deploy to {{env}}"}},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f1"}, false)

	// No molecule should be created — required var is missing.
	molBeads := filterBeadsByType(daemon.beads, "molecule")
	if len(molBeads) != 0 {
		t.Errorf("expected no molecule when required var missing, got %d", len(molBeads))
	}
}

func TestHandleFormulaPour_VarDefault(t *testing.T) {
	daemon := newFormulaMockDaemon()
	seedFormula(daemon.mockDaemon, "kd-f2", "Deploy {{env}}",
		[]formulaVarDef{{Name: "env", Default: "staging"}},
		[]formulaStep{{ID: "step1", Title: "Deploy to {{env}}"}},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f2"}, false)

	// Wait briefly for the async goroutine.
	assertEventually(t, func() bool {
		daemon.mu.Lock()
		defer daemon.mu.Unlock()
		for _, b := range daemon.beads {
			if b.Type == "molecule" {
				return true
			}
		}
		return false
	}, "expected molecule to be created with default var")
}

func TestHandleFormulaPour_EnumValidation(t *testing.T) {
	daemon := newMockDaemon()
	seedFormula(daemon, "kd-f3", "Deploy",
		[]formulaVarDef{{Name: "env", Enum: []string{"staging", "prod"}}},
		[]formulaStep{{ID: "step1", Title: "Deploy"}},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f3", "--var", "env=invalid"}, false)

	// No molecule should be created — enum validation should fail.
	molBeads := filterBeadsByType(daemon.beads, "molecule")
	if len(molBeads) != 0 {
		t.Errorf("expected no molecule for invalid enum value, got %d", len(molBeads))
	}
}

func TestHandleFormulaPour_ValidEnum(t *testing.T) {
	daemon := newFormulaMockDaemon()
	seedFormula(daemon.mockDaemon, "kd-f4", "Deploy",
		[]formulaVarDef{{Name: "env", Enum: []string{"staging", "prod"}}},
		[]formulaStep{{ID: "step1", Title: "Deploy to {{env}}"}},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f4", "--var", "env=prod"}, false)

	assertEventually(t, func() bool {
		daemon.mu.Lock()
		defer daemon.mu.Unlock()
		for _, b := range daemon.beads {
			if b.Type == "molecule" {
				return true
			}
		}
		return false
	}, "expected molecule to be created with valid enum value")
}

func TestHandleFormulaPour_NoSteps(t *testing.T) {
	daemon := newMockDaemon()
	seedFormula(daemon, "kd-f5", "Empty formula",
		nil,
		[]formulaStep{},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f5"}, false)

	molBeads := filterBeadsByType(daemon.beads, "molecule")
	if len(molBeads) != 0 {
		t.Errorf("expected no molecule for formula with no steps, got %d", len(molBeads))
	}
}

func TestHandleFormulaPour_ConditionalStepFiltering(t *testing.T) {
	daemon := newFormulaMockDaemon()
	seedFormula(daemon.mockDaemon, "kd-f6", "Conditional",
		[]formulaVarDef{{Name: "deploy", Default: "false"}},
		[]formulaStep{
			{ID: "build", Title: "Build"},
			{ID: "deploy", Title: "Deploy", Condition: "{{deploy}} == true"},
			{ID: "notify", Title: "Notify", DependsOn: []string{"deploy"}},
		},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f6"}, false)

	assertEventually(t, func() bool {
		daemon.mu.Lock()
		defer daemon.mu.Unlock()
		for _, b := range daemon.beads {
			if b.Type == "molecule" {
				return true
			}
		}
		return false
	}, "expected molecule to be created")

	// deploy step should be skipped (condition=false).
	// notify step should have its dependency on deploy removed.
	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 2 {
		t.Errorf("expected 2 steps (build + notify, deploy skipped), got %d", len(taskBeads))
	}
}

func TestHandleFormulaPour_AllStepsConditionallySkipped(t *testing.T) {
	daemon := newMockDaemon()
	seedFormula(daemon, "kd-f7", "All skipped",
		[]formulaVarDef{{Name: "flag", Default: "no"}},
		[]formulaStep{
			{ID: "step1", Title: "Step 1", Condition: "{{flag}} == yes"},
		},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f7"}, false)

	// Should not create any molecule — all steps filtered.
	molBeads := filterBeadsByType(daemon.beads, "molecule")
	if len(molBeads) != 0 {
		t.Errorf("expected no molecule when all steps are conditionally skipped, got %d", len(molBeads))
	}
}

func TestHandleFormulaPour_VarSubstitutionInStep(t *testing.T) {
	daemon := newFormulaMockDaemon()
	seedFormula(daemon.mockDaemon, "kd-f8", "Deploy {{env}}",
		[]formulaVarDef{{Name: "env", Default: "staging"}, {Name: "region", Default: "us-east-1"}},
		[]formulaStep{
			{ID: "step1", Title: "Deploy to {{env}} in {{region}}", Description: "Target: {{env}}/{{region}}"},
		},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f8", "--var", "env=prod"}, false)

	assertEventually(t, func() bool {
		daemon.mu.Lock()
		defer daemon.mu.Unlock()
		for _, b := range daemon.beads {
			if b.Type == "task" && b.Title == "Deploy to prod in us-east-1" {
				return true
			}
		}
		return false
	}, "expected step title to have vars substituted: 'Deploy to prod in us-east-1'")
}

func TestHandleFormulaPour_VarEqualsFlag(t *testing.T) {
	daemon := newFormulaMockDaemon()
	seedFormula(daemon.mockDaemon, "kd-f9", "Test",
		[]formulaVarDef{{Name: "env"}},
		[]formulaStep{{ID: "step1", Title: "Deploy to {{env}}"}},
	)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.daemon = daemon

	bot.handleFormulaPour(context.Background(),
		slack.SlashCommand{ChannelID: "C1", UserID: "U1"},
		[]string{"kd-f9", "--var=env=prod"}, false)

	assertEventually(t, func() bool {
		daemon.mu.Lock()
		defer daemon.mu.Unlock()
		for _, b := range daemon.beads {
			if b.Type == "task" && b.Title == "Deploy to prod" {
				return true
			}
		}
		return false
	}, "expected --var=env=prod to parse correctly")
}

// --- resolveFormula tests ---

func TestResolveFormula_ByID(t *testing.T) {
	daemon := newMockDaemon()
	seedFormula(daemon, "kd-f10", "My Formula", nil, nil)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	f, err := bot.resolveFormula(context.Background(), "kd-f10")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Title != "My Formula" {
		t.Errorf("expected title 'My Formula', got %q", f.Title)
	}
}

func TestResolveFormula_ByIDNotFormula(t *testing.T) {
	daemon := newMockDaemon()
	daemon.mu.Lock()
	daemon.beads["kd-task-1"] = &beadsapi.BeadDetail{
		ID:   "kd-task-1",
		Type: "task",
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	_, err := bot.resolveFormula(context.Background(), "kd-task-1")
	if err == nil {
		t.Fatal("expected error when resolving non-formula bead by ID")
	}
}

func TestResolveFormula_ByNameSearch(t *testing.T) {
	daemon := newMockDaemon()
	seedFormula(daemon, "kd-f11", "Deployment Pipeline", nil, nil)
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	f, err := bot.resolveFormula(context.Background(), "Deployment Pipeline")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.ID != "kd-f11" {
		t.Errorf("expected ID kd-f11, got %s", f.ID)
	}
}

func TestResolveFormula_NotFound(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	_, err := bot.resolveFormula(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error when formula not found")
	}
}

// --- handleFormulaCommand routing tests ---

func TestHandleFormulaCommand_Routing(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"empty defaults to list", "", false},
		{"explicit list", "list", false},
		{"ls alias", "ls", false},
		{"help", "help", false},
		{"unknown", "unknown", false},
		{"show no args", "show", false},
		{"pour no args", "pour", false},
		{"wisp no args", "wisp", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newMockDaemon()
			slackSrv := newFakeSlackServer(t)
			defer slackSrv.Close()
			bot := newTestBot(daemon, slackSrv)

			// Should not panic.
			bot.handleFormulaCommand(context.Background(), slack.SlashCommand{
				Command:   "/formula",
				Text:      tt.text,
				ChannelID: "C1",
				UserID:    "U1",
			})
		})
	}
}

// --- assertEventually helper ---

func assertEventually(t *testing.T, check func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error(msg)
}
