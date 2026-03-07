package bridge

import (
	"context"
	"strings"
	"testing"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// --- /spawn command tests (new behavior: auto-name, channel-project) ---

func TestHandleSpawnCommand_NoArgs_CreatesAgentWithAutoName(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created with auto-name, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		// Name should be "agent-<4chars>" since no project
		if !strings.HasPrefix(b.Title, "agent-") {
			t.Errorf("expected auto-generated name with prefix 'agent-', got %q", b.Title)
		}
		if !isValidAgentName(b.Title) {
			t.Errorf("auto-generated name %q is not valid", b.Title)
		}
	}
}

func TestHandleSpawnCommand_NoArgs_ChannelProject(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProjectWithChannel("gasboat", "C-GASBOAT")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "",
		ChannelID: "C-GASBOAT",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat from channel mapping, got %s", b.Fields["project"])
		}
		// Name should be "gasboat-<4chars>"
		if !strings.HasPrefix(b.Title, "gasboat-") {
			t.Errorf("expected auto-generated name with prefix 'gasboat-', got %q", b.Title)
		}
	}
}

func TestHandleSpawnCommand_WithTicket_ChannelProject(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProjectWithChannel("gasboat", "C-GASBOAT")
	daemon.mu.Lock()
	daemon.beads["kd-task-77"] = &beadsapi.BeadDetail{
		ID:     "kd-task-77",
		Title:  "Fix auth flow",
		Type:   "task",
		Labels: []string{"project:gasboat"},
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "kd-task-77",
		ChannelID: "C-GASBOAT",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat, got %s", b.Fields["project"])
		}
		if b.Description != "Assigned to task: kd-task-77" {
			t.Errorf("expected description referencing kd-task-77, got %q", b.Description)
		}
		// Name should be derived from task title "Fix auth flow"
		if !strings.HasPrefix(b.Title, "fix-auth-flow-") {
			t.Errorf("expected name derived from task title, got %q", b.Title)
		}
	}
}

func TestHandleSpawnCommand_WithTicket_InfersProjectFromTicket(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("monorepo")
	daemon.mu.Lock()
	daemon.beads["kd-task-88"] = &beadsapi.BeadDetail{
		ID:     "kd-task-88",
		Title:  "Optimize queries",
		Type:   "task",
		Labels: []string{"project:monorepo"},
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// No channel-project mapping, but ticket has project label.
	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "kd-task-88",
		ChannelID: "C-RANDOM",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["project"] != "monorepo" {
			t.Errorf("expected project=monorepo (inferred from ticket), got %s", b.Fields["project"])
		}
	}
}

func TestHandleSpawnCommand_TicketNotFound_NoBeadCreated(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "PE-9999",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 0 {
		t.Errorf("expected no agent beads when ticket not found, got %d", len(agentBeads))
	}
}

func TestHandleSpawnCommand_WithRole(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "--role captain",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["role"] != "captain" {
			t.Errorf("expected role=captain, got %s", b.Fields["role"])
		}
	}
}

func TestHandleSpawnCommand_TaskFirstMode_CreatesTaskBeadAndSpawns(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProjectWithChannel("gasboat", "C-GASBOAT")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      `"fix the login bug"`,
		ChannelID: "C-GASBOAT",
		UserID:    "U456",
	})

	taskBeads := filterBeadsByType(daemon.beads, "task")
	if len(taskBeads) != 1 {
		t.Fatalf("expected 1 task bead created, got %d", len(taskBeads))
	}
	for _, b := range taskBeads {
		if b.Title != "fix the login bug" {
			t.Errorf("expected task title=%q, got %q", "fix the login bug", b.Title)
		}
		if !containsLabel(b.Labels, "project:gasboat") {
			t.Errorf("expected task to have label project:gasboat, got %v", b.Labels)
		}
	}

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat, got %s", b.Fields["project"])
		}
		if b.Description == "" {
			t.Error("expected agent description to reference task bead, got empty")
		}
	}
}

func TestHandleSpawnCommand_ProjectAndTaskDescription(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      `gasboat "fix the auth flow"`,
		ChannelID: "C123",
		UserID:    "U456",
	})

	taskBeads := filterBeadsByType(daemon.beads, "task")
	if len(taskBeads) != 1 {
		t.Fatalf("expected 1 task bead created, got %d", len(taskBeads))
	}
	for _, b := range taskBeads {
		if b.Title != "fix the auth flow" {
			t.Errorf("expected task title=%q, got %q", "fix the auth flow", b.Title)
		}
		if !containsLabel(b.Labels, "project:gasboat") {
			t.Errorf("expected task to have label project:gasboat, got %v", b.Labels)
		}
	}

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat, got %s", b.Fields["project"])
		}
		// Name should be derived from task description.
		if !strings.HasPrefix(b.Title, "fix-the-auth-") {
			t.Errorf("expected name derived from task description, got %q", b.Title)
		}
		if b.Description == "" {
			t.Error("expected agent description to reference task bead, got empty")
		}
	}
}

func TestHandleSpawnCommand_ProjectAndTaskDescriptionWithRole(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      `gasboat "deploy the new service" --role devops`,
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["role"] != "devops" {
			t.Errorf("expected role=devops, got %s", b.Fields["role"])
		}
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat, got %s", b.Fields["project"])
		}
	}

	taskBeads := filterBeadsByType(daemon.beads, "task")
	if len(taskBeads) != 1 {
		t.Fatalf("expected 1 task bead created, got %d", len(taskBeads))
	}
}

func TestHandleSpawnCommand_ProjectAndTaskDescriptionOverridesChannel(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProjectWithChannel("monorepo", "C-MONO")
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Channel maps to monorepo, but explicit project arg says gasboat.
	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      `gasboat "fix something"`,
		ChannelID: "C-MONO",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat (explicit arg), got %s", b.Fields["project"])
		}
	}
}

func TestHandleSpawnCommand_TaskFirstMode_NoProject(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      `"fix the login bug"`,
		ChannelID: "C123",
		UserID:    "U456",
	})

	taskBeads := filterBeadsByType(daemon.beads, "task")
	if len(taskBeads) != 1 {
		t.Fatalf("expected 1 task bead created, got %d", len(taskBeads))
	}

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
}

// --- Shared helper functions ---

// filterAgentBeads returns only the agent-type beads from a beads map.
func filterAgentBeads(beads map[string]*beadsapi.BeadDetail) []*beadsapi.BeadDetail {
	var result []*beadsapi.BeadDetail
	for _, b := range beads {
		if b.Type == "agent" {
			result = append(result, b)
		}
	}
	return result
}

// filterBeadsByType returns beads matching the given type from a beads map.
func filterBeadsByType(beads map[string]*beadsapi.BeadDetail, typ string) []*beadsapi.BeadDetail {
	var result []*beadsapi.BeadDetail
	for _, b := range beads {
		if b.Type == typ {
			result = append(result, b)
		}
	}
	return result
}

// filterTaskBeads returns only the task-type beads from a beads map.
func filterTaskBeads(beads map[string]*beadsapi.BeadDetail) []*beadsapi.BeadDetail {
	var result []*beadsapi.BeadDetail
	for _, b := range beads {
		if b.Type == "task" {
			result = append(result, b)
		}
	}
	return result
}

// containsLabel checks if a label is present in the labels slice.
func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// --- Helper function unit tests ---

func TestIsTicketRef(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"jira key", "PE-1234", true},
		{"jira multi-letter", "DEVOPS-42", true},
		{"bead id", "kd-abc123", true},
		{"project name", "gasboat", false},
		{"empty", "", false},
		{"no digits", "PE-abc", false},
		{"just prefix", "PE-", false},
		{"lowercase jira", "pe-123", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTicketRef(tc.input); got != tc.want {
				t.Errorf("isTicketRef(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsValidAgentName(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase", "my-bot", true},
		{"digits", "bot2", true},
		{"hyphens", "a-b-c", true},
		{"empty", "", false},
		{"uppercase", "MyBot", false},
		{"underscore", "my_bot", false},
		{"space", "my bot", false},
		{"special", "bot!", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidAgentName(tc.input); got != tc.want {
				t.Errorf("isValidAgentName(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestGenerateAgentName(t *testing.T) {
	cases := []struct {
		name        string
		description string
		wantPrefix  string
	}{
		{"three words", "fix the login bug", "fix-the-login-"},
		{"two words", "fix login", "fix-login-"},
		{"one word", "deploy", "deploy-"},
		{"strips punctuation", "fix the @#$% bug!", "fix-the-bug-"},
		{"uppercase", "Fix The Login Bug", "fix-the-login-"},
		{"empty", "", "agent-"},
		{"only punctuation", "!@#$%", "agent-"},
		{"more than three words", "refactor the entire auth system", "refactor-the-entire-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := generateAgentName(tc.description)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("generateAgentName(%q) = %q, want prefix %q", tc.description, got, tc.wantPrefix)
			}
			suffix := got[len(tc.wantPrefix):]
			if len(suffix) != 3 {
				t.Errorf("expected 3-char suffix, got %q (%d chars)", suffix, len(suffix))
			}
			if !isValidAgentName(got) {
				t.Errorf("generated name %q is not a valid agent name", got)
			}
		})
	}
}

func TestGenerateSpawnName(t *testing.T) {
	cases := []struct {
		name       string
		project    string
		wantPrefix string
	}{
		{"with project", "gasboat", "gasboat-"},
		{"empty project", "", "agent-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := generateSpawnName(tc.project)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("generateSpawnName(%q) = %q, want prefix %q", tc.project, got, tc.wantPrefix)
			}
			suffix := got[len(tc.wantPrefix):]
			if len(suffix) != 4 {
				t.Errorf("expected 4-char suffix, got %q (%d chars)", suffix, len(suffix))
			}
			if !isValidAgentName(got) {
				t.Errorf("generated name %q is not a valid agent name", got)
			}
		})
	}
}

// --- Slash command routing test ---

func TestHandleSlashCommand_RoutesStartAndSpawn(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// /start routes to handleStartCommand (requires agent name).
	bot.handleSlashCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected /start to create 1 agent bead, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Title != "my-bot" {
			t.Errorf("expected /start agent title=my-bot, got %s", b.Title)
		}
	}

	// /spawn routes to handleSpawnCommand (auto-generates name).
	bot.handleSlashCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads = filterAgentBeads(daemon.beads)
	if len(agentBeads) != 2 {
		t.Fatalf("expected /spawn to create a second agent bead, got %d total", len(agentBeads))
	}
}

func TestSplitQuotedArgs_SmartQuotes(t *testing.T) {
	tests := []struct {
		name string
		input string
		want []string
	}{
		{"straight quotes", `"fix the helm chart"`, []string{"fix the helm chart"}},
		{"smart quotes", "\u201cfix the helm chart\u201d", []string{"fix the helm chart"}},
		{"mixed quotes", "\u201cfix the chart\u201d and more", []string{"fix the chart", "and", "more"}},
		{"no quotes", "fix the helm chart", []string{"fix", "the", "helm", "chart"}},
		{"empty", "", nil},
		{"single word", "deploy", []string{"deploy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitQuotedArgs(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("splitQuotedArgs(%q) = %v (len %d), want %v (len %d)", tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitQuotedArgs(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHandleSpawnCommand_SmartQuotes_PassesPrompt(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Simulate Slack sending smart quotes: /spawn "fix the helm chart"
	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "\u201cfix the helm chart\u201d",
		ChannelID: "C123",
		UserID:    "U456",
	})

	// Should have created a task bead + agent bead.
	taskBeads := filterBeadsByType(daemon.beads, "task")
	if len(taskBeads) == 0 {
		t.Fatal("expected a task bead to be created for the prompt")
	}
	found := false
	for _, tb := range taskBeads {
		if tb.Title == "fix the helm chart" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected task bead with title 'fix the helm chart', got %v", taskBeads)
	}

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) == 0 {
		t.Fatal("expected an agent bead to be created")
	}
	// The agent bead should have the prompt in its fields.
	for _, ab := range agentBeads {
		if ab.Fields["prompt"] == "fix the helm chart" {
			return // success
		}
	}
	t.Errorf("expected agent bead to have prompt field 'fix the helm chart'")
}
