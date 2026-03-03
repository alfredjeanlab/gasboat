package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// TestPruneStaleAgentCards_RemovesClosedAgents verifies that agent cards for
// agents whose beads are no longer active (closed) are deleted on startup.
func TestPruneStaleAgentCards_RemovesClosedAgents(t *testing.T) {
	daemon := newMockDaemon()

	// Seed one active agent (bead is open, state=working).
	daemon.beads["bd-active"] = &beadsapi.BeadDetail{
		ID:    "bd-active",
		Title: "active-bot",
		Type:  "agent",
		Fields: map[string]string{
			"agent":       "active-bot",
			"project":     "gasboat",
			"role":        "crew",
			"agent_state": "working",
		},
	}
	// No bead for "dead-bot" — simulates a closed agent whose bead is gone.

	var deletedMessages []string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.delete" {
			_ = r.ParseForm()
			deletedMessages = append(deletedMessages, r.FormValue("ts"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Pre-populate agent cards as if hydrated from state file.
	bot.agentCards["active-bot"] = MessageRef{ChannelID: "C123", Timestamp: "1111.1111"}
	bot.agentCards["dead-bot"] = MessageRef{ChannelID: "C123", Timestamp: "2222.2222"}

	bot.pruneStaleAgentCards(context.Background())

	// Active bot's card should remain.
	if _, ok := bot.agentCards["active-bot"]; !ok {
		t.Error("active agent card should not be pruned")
	}

	// Dead bot's card should be removed.
	if _, ok := bot.agentCards["dead-bot"]; ok {
		t.Error("stale agent card should be pruned")
	}

	// Slack delete should have been called for the dead bot's message.
	if len(deletedMessages) != 1 {
		t.Fatalf("expected 1 Slack message deleted, got %d", len(deletedMessages))
	}
	if deletedMessages[0] != "2222.2222" {
		t.Errorf("expected deleted timestamp 2222.2222, got %s", deletedMessages[0])
	}
}

// TestPruneStaleAgentCards_RemovesDoneAgents verifies that agent cards for
// agents with agent_state=done (bead still open) are pruned on restart.
func TestPruneStaleAgentCards_RemovesDoneAgents(t *testing.T) {
	daemon := newMockDaemon()

	// Agent with state=done but bead still open.
	daemon.beads["bd-done"] = &beadsapi.BeadDetail{
		ID:    "bd-done",
		Title: "done-bot",
		Type:  "agent",
		Fields: map[string]string{
			"agent":       "done-bot",
			"project":     "gasboat",
			"role":        "crew",
			"agent_state": "done",
		},
	}
	// Agent with state=working (should be kept).
	daemon.beads["bd-working"] = &beadsapi.BeadDetail{
		ID:    "bd-working",
		Title: "working-bot",
		Type:  "agent",
		Fields: map[string]string{
			"agent":       "working-bot",
			"project":     "gasboat",
			"role":        "crew",
			"agent_state": "working",
		},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.agentCards["done-bot"] = MessageRef{ChannelID: "C123", Timestamp: "3333.3333"}
	bot.agentCards["working-bot"] = MessageRef{ChannelID: "C123", Timestamp: "4444.4444"}

	bot.pruneStaleAgentCards(context.Background())

	if _, ok := bot.agentCards["done-bot"]; ok {
		t.Error("done agent card should be pruned")
	}
	if _, ok := bot.agentCards["working-bot"]; !ok {
		t.Error("working agent card should not be pruned")
	}
}

// TestPruneStaleAgentCards_RemovesStopRequested verifies that agent cards for
// agents with stop_requested set are pruned on restart.
func TestPruneStaleAgentCards_RemovesStopRequested(t *testing.T) {
	daemon := newMockDaemon()

	// Agent with stop_requested but bead still open and state=working.
	daemon.beads["bd-stopping"] = &beadsapi.BeadDetail{
		ID:    "bd-stopping",
		Title: "stopping-bot",
		Type:  "agent",
		Fields: map[string]string{
			"agent":          "stopping-bot",
			"project":        "gasboat",
			"role":           "crew",
			"agent_state":    "working",
			"stop_requested": "true",
		},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.agentCards["stopping-bot"] = MessageRef{ChannelID: "C123", Timestamp: "5555.5555"}

	bot.pruneStaleAgentCards(context.Background())

	if _, ok := bot.agentCards["stopping-bot"]; ok {
		t.Error("stop-requested agent card should be pruned")
	}
}

// TestPruneStaleAgentCards_NoCards verifies that pruning is a no-op when
// there are no hydrated agent cards.
func TestPruneStaleAgentCards_NoCards(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Should not panic or error.
	bot.pruneStaleAgentCards(context.Background())

	if len(bot.agentCards) != 0 {
		t.Errorf("expected 0 agent cards, got %d", len(bot.agentCards))
	}
}
