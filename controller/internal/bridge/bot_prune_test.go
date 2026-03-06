package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// TestPruneStaleAgentCards_CompressesClosedAgents verifies that agent cards for
// agents whose beads are no longer active (closed) are compressed on startup.
func TestPruneStaleAgentCards_CompressesClosedAgents(t *testing.T) {
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

	var updatedMessages []string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.update" {
			_ = r.ParseForm()
			updatedMessages = append(updatedMessages, r.FormValue("ts"))
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

	// Active bot's card should remain unchanged.
	if _, ok := bot.agentCards["active-bot"]; !ok {
		t.Error("active agent card should not be pruned")
	}

	// Dead bot's card should still exist (compressed, not deleted).
	if _, ok := bot.agentCards["dead-bot"]; !ok {
		t.Error("stale agent card should be compressed, not removed")
	}

	// Slack update should have been called for the dead bot's message.
	if len(updatedMessages) != 1 {
		t.Fatalf("expected 1 Slack message updated, got %d", len(updatedMessages))
	}
	if updatedMessages[0] != "2222.2222" {
		t.Errorf("expected updated timestamp 2222.2222, got %s", updatedMessages[0])
	}
}

// TestPruneStaleAgentCards_CompressesDoneAgents verifies that agent cards for
// agents with agent_state=done (bead still open) are compressed on restart.
func TestPruneStaleAgentCards_CompressesDoneAgents(t *testing.T) {
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
	bot.agentState["done-bot"] = "done"

	bot.pruneStaleAgentCards(context.Background())

	// Done bot's card should still exist (compressed, not deleted).
	if _, ok := bot.agentCards["done-bot"]; !ok {
		t.Error("done agent card should be compressed, not removed")
	}
	if _, ok := bot.agentCards["working-bot"]; !ok {
		t.Error("working agent card should not be pruned")
	}
}

// TestPruneStaleAgentCards_CompressesStopRequested verifies that agent cards for
// agents with stop_requested set are compressed on restart.
func TestPruneStaleAgentCards_CompressesStopRequested(t *testing.T) {
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

	// Card should still exist (compressed, not deleted).
	if _, ok := bot.agentCards["stopping-bot"]; !ok {
		t.Error("stop-requested agent card should be compressed, not removed")
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

// TestNotifyAgentSpawn_SkipsClosedBead verifies that NotifyAgentSpawn does not
// post a card for an agent bead that is already closed (zombie prevention on
// SSE replay after restart).
func TestNotifyAgentSpawn_SkipsClosedBead(t *testing.T) {
	daemon := newMockDaemon()
	// Seed a closed agent bead.
	daemon.beads["agent-closed-1"] = &beadsapi.BeadDetail{
		ID:       "agent-closed-1",
		Type:     "agent",
		Status:   "closed",
		Title:    "dead-agent",
		Assignee: "gasboat/crew/dead-agent",
		Fields:   map[string]string{"agent": "dead-agent", "agent_state": "done"},
	}

	var postedMessages int
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			postedMessages++
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message_ts": "9999.9999"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"

	// Simulate SSE replay of a created event for an already-closed agent.
	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:       "agent-closed-1",
		Type:     "agent",
		Title:    "dead-agent",
		Assignee: "gasboat/crew/dead-agent",
		Fields:   map[string]string{"agent": "dead-agent"},
	})

	// No Slack message should have been posted.
	if postedMessages != 0 {
		t.Errorf("expected 0 Slack messages for closed bead, got %d", postedMessages)
	}

	// No agent state should have been recorded.
	if _, ok := bot.agentState["dead-agent"]; ok {
		t.Error("expected no agent state recorded for closed bead")
	}
}

// TestNotifyAgentSpawn_AllowsOpenBead verifies that NotifyAgentSpawn proceeds
// normally for an open (active) agent bead.
func TestNotifyAgentSpawn_AllowsOpenBead(t *testing.T) {
	daemon := newMockDaemon()
	// Seed an open agent bead.
	daemon.beads["agent-open-1"] = &beadsapi.BeadDetail{
		ID:       "agent-open-1",
		Type:     "agent",
		Status:   "open",
		Title:    "live-agent",
		Assignee: "gasboat/crew/live-agent",
		Fields:   map[string]string{"agent": "live-agent", "agent_state": "spawning"},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"

	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:       "agent-open-1",
		Type:     "agent",
		Title:    "live-agent",
		Assignee: "gasboat/crew/live-agent",
		Fields:   map[string]string{"agent": "live-agent"},
	})

	// Agent state should have been recorded.
	if state, ok := bot.agentState["live-agent"]; !ok || state != "spawning" {
		t.Errorf("expected agent state 'spawning', got %q (exists=%v)", state, ok)
	}
}

// TestAgentIdentityDrift_FullPathNormalized verifies that agent identities
// are canonicalized to short names across spawn, state update, and task update
// events, preventing duplicate map entries from full paths vs short names.
func TestAgentIdentityDrift_FullPathNormalized(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["agent-1"] = &beadsapi.BeadDetail{
		ID:       "agent-1",
		Type:     "agent",
		Status:   "open",
		Title:    "my-agent",
		Assignee: "gasboat/crew/my-agent",
		Fields:   map[string]string{"agent": "gasboat/crew/my-agent", "agent_state": "spawning"},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"

	// Spawn event uses full path in Assignee — should be stored under short name.
	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:       "agent-1",
		Type:     "agent",
		Title:    "my-agent",
		Assignee: "gasboat/crew/my-agent",
		Fields:   map[string]string{"agent": "gasboat/crew/my-agent"},
	})

	if _, ok := bot.agentState["my-agent"]; !ok {
		t.Error("expected agentState keyed by short name 'my-agent'")
	}
	if _, ok := bot.agentState["gasboat/crew/my-agent"]; ok {
		t.Error("agentState should not have full path key")
	}

	// State update event also uses full path — should update the same entry.
	bot.NotifyAgentState(context.Background(), BeadEvent{
		Assignee: "gasboat/crew/my-agent",
		Fields:   map[string]string{"agent": "gasboat/crew/my-agent", "agent_state": "working"},
	})

	if state := bot.agentState["my-agent"]; state != "working" {
		t.Errorf("expected agentState[my-agent]=working, got %q", state)
	}
	if len(bot.agentState) != 1 {
		t.Errorf("expected exactly 1 agentState entry, got %d (identity drift!)", len(bot.agentState))
	}

	// Task update with full path — should still find the card by short name.
	bot.NotifyAgentTaskUpdate(context.Background(), "gasboat/crew/my-agent")

	// Verify no duplicate entries were created in any map.
	if len(bot.agentSeen) != 1 {
		t.Errorf("expected exactly 1 agentSeen entry, got %d (identity drift!)", len(bot.agentSeen))
	}
}

// TestAgentIdentityDrift_KillAgentWithFullPath verifies that killAgent
// canonicalizes the agent name and correctly removes the card even when
// called with a full path identity.
func TestAgentIdentityDrift_KillAgentWithFullPath(t *testing.T) {
	daemon := newMockDaemon()
	// Mock FindAgentBead looks up by agent name — store under short name
	// because killAgent now canonicalizes before calling FindAgentBead.
	daemon.beads["kill-me"] = &beadsapi.BeadDetail{
		ID:       "agent-2",
		Type:     "agent",
		Status:   "open",
		Title:    "kill-me",
		Assignee: "gasboat/crew/kill-me",
		Fields:   map[string]string{"agent": "kill-me"},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Card stored under short name (as it would be after the spawn fix).
	bot.agentCards["kill-me"] = MessageRef{ChannelID: "C123", Timestamp: "1111.1111"}
	bot.agentState["kill-me"] = "working"
	bot.agentPending["kill-me"] = 2

	// Kill with full path identity — should compress the card, not delete it.
	err := bot.killAgent(context.Background(), "gasboat/crew/kill-me", true)
	if err != nil {
		t.Fatalf("killAgent failed: %v", err)
	}

	// Card should still exist (compressed to single line).
	if _, ok := bot.agentCards["kill-me"]; !ok {
		t.Error("agent card should be compressed, not removed")
	}
	// Pending count should be cleaned up.
	if _, ok := bot.agentPending["kill-me"]; ok {
		t.Error("agent pending should have been removed")
	}
}

// TestAgentIdentityDrift_HydrationNormalizesKeys verifies that NewBot
// canonicalizes agent identity keys when hydrating from persisted state,
// preventing drift from stale full-path keys in the state file.
func TestAgentIdentityDrift_HydrationNormalizesKeys(t *testing.T) {
	state, err := NewStateManager("")
	if err != nil {
		t.Fatalf("NewStateManager: %v", err)
	}

	// Simulate persisted state with full-path keys (pre-fix data).
	_ = state.SetAgentCard("gasboat/crew/old-agent", MessageRef{
		ChannelID: "C123",
		Timestamp: "1111.1111",
		Agent:     "gasboat/crew/old-agent",
	})
	_ = state.SetDecisionMessage("dec-1", MessageRef{
		ChannelID: "C123",
		Timestamp: "2222.2222",
		Agent:     "gasboat/crew/old-agent",
	})

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := NewBot(BotConfig{
		BotToken:      "xoxb-test",
		AppToken:      "xapp-test",
		Channel:       "C123",
		ThreadingMode: "agent",
		State:         state,
		Logger:        slog.Default(),
	})
	// Override Slack API client to use our test server.
	bot.api = slack.New("xoxb-test", slack.OptionAPIURL(slackSrv.URL+"/"))

	// Agent card should be stored under short name after hydration.
	if _, ok := bot.agentCards["old-agent"]; !ok {
		t.Error("expected agentCards keyed by short name 'old-agent' after hydration")
	}
	if _, ok := bot.agentCards["gasboat/crew/old-agent"]; ok {
		t.Error("agentCards should not have full path key after hydration")
	}

	// Pending count should use short name.
	if count := bot.agentPending["old-agent"]; count != 1 {
		t.Errorf("expected agentPending[old-agent]=1, got %d", count)
	}
	if count := bot.agentPending["gasboat/crew/old-agent"]; count != 0 {
		t.Errorf("expected agentPending[gasboat/crew/old-agent]=0, got %d", count)
	}
}
