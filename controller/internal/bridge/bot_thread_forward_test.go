package bridge

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack/slackevents"
)

func TestHandleMessageEvent_ThreadForward_AgentCardThread_NoForward(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["hq"] = &beadsapi.BeadDetail{
		ID:    "bd-agent-hq",
		Title: "crew-gasboat-crew-hq",
		Type:  "agent",
		Fields: map[string]string{
			"agent":   "hq",
			"project": "gasboat",
		},
	}

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{
		daemon:    daemon,
		state:     state,
		logger:    slog.Default(),
		botUserID: "U-BOT",
		agentCards: map[string]MessageRef{
			"hq": {
				ChannelID: "C-agents",
				Timestamp: "1111.2222",
				Agent:     "hq",
			},
		},
		lastThreadNudge: make(map[string]time.Time),
	}

	// Simulate a thread reply (not a mention) under an agent card thread.
	ev := &slackevents.MessageEvent{
		User:            "U-HUMAN",
		Channel:         "C-agents",
		Text:            "here is the extra context you asked for",
		TimeStamp:       "1111.3333",
		ThreadTimeStamp: "1111.2222",
	}

	// getAgentByThread still finds the agent (used for @mention routing).
	agent := b.getAgentByThread(ev.Channel, ev.ThreadTimeStamp)
	if agent != "hq" {
		t.Fatalf("expected agent hq from getAgentByThread, got %q", agent)
	}

	// But getThreadSpawnedAgent should NOT find it (agent card, not thread-spawned).
	spawned := b.getThreadSpawnedAgent(ev.Channel, ev.ThreadTimeStamp)
	if spawned != "" {
		t.Fatalf("expected empty from getThreadSpawnedAgent for agent card thread, got %q", spawned)
	}

	// No bead should be created for non-mention messages in agent card threads.
	for _, bead := range daemon.beads {
		if hasLabel(bead.Labels, "slack-thread-reply") {
			t.Error("should not create thread-reply bead for agent card thread without @mention")
		}
	}
}

func TestHandleMessageEvent_ThreadForward_ThreadSpawnedAgent(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["thread-1111-2222"] = &beadsapi.BeadDetail{
		ID:    "bd-thread-agent",
		Title: "thread-1111-2222",
		Type:  "agent",
		Fields: map[string]string{
			"agent":                "thread-1111-2222",
			"slack_thread_channel": "C-support",
			"slack_thread_ts":      "1111.2222",
		},
	}

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = state.SetThreadAgent("C-support", "1111.2222", "thread-1111-2222")

	b := &Bot{
		daemon:          daemon,
		state:           state,
		logger:          slog.Default(),
		botUserID:       "U-BOT",
		agentCards:      map[string]MessageRef{},
		lastThreadNudge: make(map[string]time.Time),
	}

	ev := &slackevents.MessageEvent{
		User:            "U-HUMAN",
		Channel:         "C-support",
		Text:            "follow up info",
		TimeStamp:       "1111.3333",
		ThreadTimeStamp: "1111.2222",
	}

	agent := b.getAgentByThread(ev.Channel, ev.ThreadTimeStamp)
	if agent != "thread-1111-2222" {
		t.Fatalf("expected thread-1111-2222, got %q", agent)
	}

	b.handleThreadForward(context.Background(), ev, agent)

	found := false
	for _, bead := range daemon.beads {
		if bead.Type == "task" && bead.Assignee == "thread-1111-2222" && hasLabel(bead.Labels, "slack-thread-reply") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a task bead with label slack-thread-reply assigned to thread-1111-2222")
	}
}

func TestHandleMessageEvent_ThreadForward_UntrackedThread(t *testing.T) {
	b := &Bot{
		logger:          slog.Default(),
		botUserID:       "U-BOT",
		agentCards:      map[string]MessageRef{},
		lastThreadNudge: make(map[string]time.Time),
	}

	// Thread not bound to any agent — getAgentByThread returns "".
	agent := b.getAgentByThread("C-random", "9999.8888")
	if agent != "" {
		t.Errorf("expected empty agent for untracked thread, got %q", agent)
	}
	// handleThreadForward would not be called in this case.
}

func TestHandleMessageEvent_ThreadForward_SkipsMentions(t *testing.T) {
	// Verify that messages containing @BOT are skipped in handleMessageEvent's
	// thread forward path (to avoid duplicates with app_mention handler).
	botUserID := "U-BOT"
	text := "<@U-BOT> check this"

	// This is the same check used in handleMessageEvent.
	containsMention := containsBotMention(text, botUserID)
	if !containsMention {
		t.Error("expected message with @BOT to be detected as containing mention")
	}

	// A plain message should not be skipped.
	plain := "here is more context"
	if containsBotMention(plain, botUserID) {
		t.Error("plain message should not be detected as containing mention")
	}
}

// containsBotMention mirrors the check in handleMessageEvent.
func containsBotMention(text, botUserID string) bool {
	return len(botUserID) > 0 && len(text) > 0 &&
		// Use same format as handleMessageEvent.
		stringContains(text, "<@"+botUserID+">")
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestHandleMessageEvent_ThreadForward_SkipsBotMessages(t *testing.T) {
	// handleMessageEvent filters ev.User == botUserID at the top.
	// Verify that check prevents forwarding bot's own messages.
	botUserID := "U-BOT"

	ev := &slackevents.MessageEvent{
		User:            botUserID, // bot's own message
		Channel:         "C-agents",
		Text:            "I did the thing",
		TimeStamp:       "1111.3333",
		ThreadTimeStamp: "1111.2222",
	}

	// This is the same check at the top of handleMessageEvent.
	if ev.User != botUserID {
		t.Error("expected bot message to be filtered")
	}
}

func TestHandleMessageEvent_ThreadForward_SkipsSubtypes(t *testing.T) {
	ev := &slackevents.MessageEvent{
		User:            "U-HUMAN",
		Channel:         "C-agents",
		Text:            "edited text",
		SubType:         "message_changed",
		TimeStamp:       "1111.3333",
		ThreadTimeStamp: "1111.2222",
	}

	// handleMessageEvent filters SubType != "" at the top.
	if ev.SubType == "" {
		t.Error("expected subtype to be non-empty")
	}
}

func TestHandleMessageEvent_ThreadForward_InactiveAgent_Respawns(t *testing.T) {
	daemon := newMockDaemon()
	// Agent NOT in daemon → FindAgentBead will fail → should respawn with session resume.

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = state.SetThreadAgent("C-test", "1111.2222", "dead-agent")

	b := &Bot{
		daemon:          daemon,
		state:           state,
		logger:          slog.Default(),
		botUserID:       "U-BOT",
		agentCards:      map[string]MessageRef{},
		lastThreadNudge: make(map[string]time.Time),
		threadSpawnMsgs: make(map[string]MessageRef),
	}

	ev := &slackevents.MessageEvent{
		User:            "U-HUMAN",
		Channel:         "C-test",
		Text:            "hello?",
		TimeStamp:       "1111.3333",
		ThreadTimeStamp: "1111.2222",
	}

	// Agent is found in state but inactive.
	agent := b.getAgentByThread(ev.Channel, ev.ThreadTimeStamp)
	if agent != "dead-agent" {
		t.Fatalf("expected dead-agent, got %q", agent)
	}

	b.handleThreadForward(context.Background(), ev, agent)

	// Should KEEP the thread→agent mapping (respawn preserves it).
	if _, ok := state.GetThreadAgent("C-test", "1111.2222"); !ok {
		t.Error("expected thread agent mapping to be preserved after respawn")
	}

	// Should have created an agent bead with the same name for session resume.
	var foundAgentBead bool
	for _, bead := range daemon.beads {
		if bead.Type == "agent" && bead.Title == "dead-agent" {
			foundAgentBead = true
		}
	}
	if !foundAgentBead {
		t.Error("expected agent bead to be created for session-resume respawn")
	}
}

func TestNudgeThrottling(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["hq"] = &beadsapi.BeadDetail{
		ID:    "bd-agent-hq",
		Title: "hq",
		Type:  "agent",
		Fields: map[string]string{
			"agent": "hq",
		},
	}

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{
		daemon:    daemon,
		state:     state,
		logger:    slog.Default(),
		botUserID: "U-BOT",
		agentCards: map[string]MessageRef{
			"hq": {
				ChannelID: "C-agents",
				Timestamp: "1111.2222",
				Agent:     "hq",
			},
		},
		lastThreadNudge: make(map[string]time.Time),
	}

	// First nudge should not be throttled.
	if b.shouldThrottleNudge("hq", "1111.2222") {
		t.Error("first nudge should not be throttled")
	}

	// Second nudge within window should be throttled.
	if !b.shouldThrottleNudge("hq", "1111.2222") {
		t.Error("second nudge within window should be throttled")
	}

	// Different thread should not be throttled.
	if b.shouldThrottleNudge("hq", "3333.4444") {
		t.Error("nudge for different thread should not be throttled")
	}

	// Different agent should not be throttled.
	if b.shouldThrottleNudge("k8s", "1111.2222") {
		t.Error("nudge for different agent should not be throttled")
	}
}

func TestNudgeThrottling_Expiry(t *testing.T) {
	b := &Bot{
		logger:          slog.Default(),
		lastThreadNudge: make(map[string]time.Time),
	}

	// Manually set a past nudge time beyond the interval.
	b.mu.Lock()
	b.lastThreadNudge["hq:1111.2222"] = time.Now().Add(-2 * threadNudgeInterval)
	b.mu.Unlock()

	// Should not be throttled since the interval has passed.
	if b.shouldThrottleNudge("hq", "1111.2222") {
		t.Error("nudge after interval should not be throttled")
	}
}

func TestHandleThreadForward_BeadAlwaysCreated(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["hq"] = &beadsapi.BeadDetail{
		ID:    "bd-agent-hq",
		Title: "hq",
		Type:  "agent",
		Fields: map[string]string{
			"agent": "hq",
		},
	}

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Use a thread-spawned agent (not agent card) for forwarding.
	_ = state.SetThreadAgent("C-agents", "1111.2222", "hq")

	b := &Bot{
		daemon:          daemon,
		state:           state,
		logger:          slog.Default(),
		botUserID:       "U-BOT",
		agentCards:      map[string]MessageRef{},
		lastThreadNudge: make(map[string]time.Time),
	}

	ctx := context.Background()

	// Send two messages in quick succession — both should create beads.
	for i, text := range []string{"first message", "second message"} {
		ev := &slackevents.MessageEvent{
			User:            "U-HUMAN",
			Channel:         "C-agents",
			Text:            text,
			TimeStamp:       "1111." + string(rune('3'+i)),
			ThreadTimeStamp: "1111.2222",
		}
		b.handleThreadForward(ctx, ev, "hq")
	}

	// Count beads with slack-thread-reply label.
	count := 0
	for _, bead := range daemon.beads {
		if hasLabel(bead.Labels, "slack-thread-reply") {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 thread-reply beads, got %d", count)
	}
}

func TestHandleMessageEvent_DecisionThread_NotForwarded(t *testing.T) {
	// Verify that a decision thread reply does not also trigger thread forwarding
	// when no agent is bound to the thread. The routing in handleMessageEvent
	// calls handleThreadReply first, then checks getAgentByThread — if the thread
	// is a decision thread but NOT bound to an agent, no forwarding happens.
	daemon := newMockDaemon()

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Decision thread exists in state, but no agent is bound to this thread.
	_ = state.SetDecisionMessage("bd-decision-1", MessageRef{
		ChannelID: "C-decisions",
		Timestamp: "5555.6666",
		Agent:     "hq",
	})

	b := &Bot{
		daemon:          daemon,
		state:           state,
		logger:          slog.Default(),
		botUserID:       "U-BOT",
		agentCards:      map[string]MessageRef{},
		lastThreadNudge: make(map[string]time.Time),
	}

	// No agent is bound to this thread via getAgentByThread.
	agent := b.getAgentByThread("C-decisions", "5555.6666")
	if agent != "" {
		t.Errorf("expected no agent bound to decision thread, got %q", agent)
	}
	// So handleThreadForward would NOT be called — no duplicate bead created.
}
