package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"log/slog"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// newTestBot creates a Bot wired to a fake Slack API server for unit tests.
// The server responds OK to all requests (PostEphemeral, etc.).
func newTestBot(daemon BeadClient, slackSrv *httptest.Server) *Bot {
	api := slack.New("xoxb-test", slack.OptionAPIURL(slackSrv.URL+"/"))
	return &Bot{
		api:          api,
		daemon:       daemon,
		logger:       slog.Default(),
		messages:     make(map[string]MessageRef),
		agentCards:   make(map[string]MessageRef),
		agentPending: make(map[string]int),
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

func TestHandleSpawnCommand_SpawnsAgentWithProject(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "my-bot gasboat",
		ChannelID: "C123",
		UserID:    "U456",
	})

	// SpawnAgent should have created an agent bead (plus the seeded project bead).
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

func TestHandleSpawnCommand_SpawnsAgentWithRole(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
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

func TestHandleSpawnCommand_SpawnsAgentWithRoleEquals(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
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

func TestHandleSpawnCommand_SpawnsAgentWithTask(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
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

func TestHandleSpawnCommand_SpawnsAgentNoProject(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "my-bot",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 1 {
		t.Fatalf("expected 1 bead created, got %d", len(daemon.beads))
	}
}

func TestHandleSpawnCommand_EmptyArgs_NoBeadCreated(t *testing.T) {
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

	if len(daemon.beads) != 0 {
		t.Errorf("expected no bead created for empty args, got %d", len(daemon.beads))
	}
}

func TestHandleSpawnCommand_InvalidAgentName_NoBeadCreated(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "My_Bot!",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 0 {
		t.Errorf("expected no bead created for invalid name, got %d", len(daemon.beads))
	}
}

func TestHandleSpawnCommand_ResolvesJiraTicket(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("monorepo")
	// Seed a task bead that represents a JIRA ticket.
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

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
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

func TestHandleSpawnCommand_ResolvesBeadID(t *testing.T) {
	daemon := newMockDaemon()
	daemon.seedProject("gasboat")
	// Seed a task bead with a kd- ID.
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

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
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

func TestHandleSpawnCommand_TicketNotFound_NoBeadCreated(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "my-bot PE-9999",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 0 {
		t.Errorf("expected no bead created when ticket not found, got %d", len(daemon.beads))
	}
}

func TestHandleSpawnCommand_TicketWithRole(t *testing.T) {
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

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
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
