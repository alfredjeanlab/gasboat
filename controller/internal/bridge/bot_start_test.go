package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// newTestBot creates a Bot wired to a fake Slack API server for unit tests.
// The server responds OK to all requests (PostEphemeral, etc.).
func newTestBot(daemon BeadClient, slackSrv *httptest.Server) *Bot {
	api := slack.New("xoxb-test", slack.OptionAPIURL(slackSrv.URL+"/"))
	return &Bot{
		api:           api,
		daemon:        daemon,
		logger:        slog.Default(),
		messages:      make(map[string]MessageRef),
		agentCards:    make(map[string]MessageRef),
		agentPending:  make(map[string]int),
		agentState:    make(map[string]string),
		agentSeen:     make(map[string]time.Time),
		agentPodName:  make(map[string]string),
		agentImageTag: make(map[string]string),
	}
}

// newFakeSlackServer returns an httptest.Server that accepts any Slack API call
// and returns a generic OK response.
func newFakeSlackServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message_ts": "1234.5678"})
	}))
}

// --- /start command tests (old /spawn behavior, requires agent name) ---

func TestHandleStartCommand_SpawnsAgentWithProject(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot gasboat",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Title != "my-bot" {
			t.Errorf("expected title=my-bot, got %s", b.Title)
		}
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat, got %s", b.Fields["project"])
		}
		if b.Fields["role"] != "crew" {
			t.Errorf("expected default role=crew, got %s", b.Fields["role"])
		}
	}
}

func TestHandleStartCommand_SpawnsAgentWithRole(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot gasboat --role captain",
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

func TestHandleStartCommand_SpawnsAgentWithRoleEquals(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot gasboat --role=jirafix",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["role"] != "jirafix" {
			t.Errorf("expected role=jirafix, got %s", b.Fields["role"])
		}
	}
}

func TestHandleStartCommand_SpawnsAgentWithTask(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot gasboat kd-task-42",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Description != "Assigned to task: kd-task-42" {
			t.Errorf("expected description %q, got %q", "Assigned to task: kd-task-42", b.Description)
		}
	}
}

func TestHandleStartCommand_SpawnsAgentNoProject(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 1 {
		t.Fatalf("expected 1 bead created, got %d", len(daemon.beads))
	}
}

func TestHandleStartCommand_EmptyArgs_NoBeadCreated(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 0 {
		t.Errorf("expected no bead created for empty args, got %d", len(daemon.beads))
	}
}

func TestHandleStartCommand_InvalidAgentName_NoBeadCreated(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "My_Bot!",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 0 {
		t.Errorf("expected no bead created for invalid name, got %d", len(daemon.beads))
	}
}

func TestHandleStartCommand_ResolvesJiraTicket(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("monorepo")
	daemon.mu.Lock()
	daemon.beads["kd-resolved-1"] = &beadsapi.BeadDetail{
		ID:     "kd-resolved-1",
		Title:  "Fix login bug",
		Type:   "task",
		Labels: []string{"jira:PE-1234", "project:monorepo"},
		Fields: map[string]string{"jira_key": "PE-1234"},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot PE-1234",
		ChannelID: "C123",
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
		if b.Description != "Assigned to task: kd-resolved-1" {
			t.Errorf("expected description referencing resolved bead ID, got %q", b.Description)
		}
	}
}

func TestHandleStartCommand_ResolvesBeadID(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	daemon.mu.Lock()
	daemon.beads["kd-task-99"] = &beadsapi.BeadDetail{
		ID:     "kd-task-99",
		Title:  "Implement feature X",
		Type:   "task",
		Labels: []string{"project:gasboat"},
		Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot kd-task-99",
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat (inferred from bead labels), got %s", b.Fields["project"])
		}
		if b.Description != "Assigned to task: kd-task-99" {
			t.Errorf("expected description referencing kd-task-99, got %q", b.Description)
		}
	}
}

func TestHandleStartCommand_TicketNotFound_NoBeadCreated(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "my-bot PE-9999",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 0 {
		t.Errorf("expected no bead created when ticket not found, got %d", len(daemon.beads))
	}
}

func TestHandleStartCommand_TicketWithRole(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("monorepo")
	daemon.mu.Lock()
	daemon.beads["kd-resolved-2"] = &beadsapi.BeadDetail{
		ID:     "kd-resolved-2",
		Title:  "Deploy service",
		Type:   "task",
		Labels: []string{"jira:DEVOPS-42", "project:monorepo"},
		Fields: map[string]string{"jira_key": "DEVOPS-42"},
	}
	daemon.mu.Unlock()

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      "deploy-bot DEVOPS-42 --role devops",
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
		if b.Fields["project"] != "monorepo" {
			t.Errorf("expected project=monorepo, got %s", b.Fields["project"])
		}
	}
}

func TestHandleStartCommand_TaskFirstMode(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      `"fix the login bug" gasboat`,
		ChannelID: "C123",
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
		if b.Description == "" {
			t.Errorf("expected agent description to reference task, got empty")
		}
	}

	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 1 {
		t.Fatalf("expected 1 task bead created, got %d", len(taskBeads))
	}
	for _, b := range taskBeads {
		if b.Title != "fix the login bug" {
			t.Errorf("expected task title=%q, got %q", "fix the login bug", b.Title)
		}
	}
}

func TestHandleStartCommand_TaskFirstModeWithRole(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      `"deploy the new service" gasboat --role devops`,
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
}

func TestHandleStartCommand_TaskFirstModeNoProject(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      `"fix the login bug"`,
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead created, got %d", len(agentBeads))
	}

	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 1 {
		t.Fatalf("expected 1 task bead created, got %d", len(taskBeads))
	}
}

func TestHandleStartCommand_TaskFirstModeInvalidProject(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleStartCommand(context.Background(), slack.SlashCommand{
		Command:   "/start",
		Text:      `"fix the login bug" nonexistent`,
		ChannelID: "C123",
		UserID:    "U456",
	})

	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 0 {
		t.Errorf("expected no agent beads for invalid project, got %d", len(agentBeads))
	}
	taskBeads := filterTaskBeads(daemon.beads)
	if len(taskBeads) != 0 {
		t.Errorf("expected no task beads for invalid project, got %d", len(taskBeads))
	}
}
