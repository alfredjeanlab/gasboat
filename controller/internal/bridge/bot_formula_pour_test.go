package bridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

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
