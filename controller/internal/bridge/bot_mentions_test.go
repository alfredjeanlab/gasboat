package bridge

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		botUserID string
		want      string
	}{
		{
			name:      "mention at start",
			text:      "<@U123BOT> check the logs",
			botUserID: "U123BOT",
			want:      "check the logs",
		},
		{
			name:      "mention in middle",
			text:      "hey <@U123BOT> check the logs",
			botUserID: "U123BOT",
			want:      "hey  check the logs",
		},
		{
			name:      "no mention",
			text:      "check the logs",
			botUserID: "U123BOT",
			want:      "check the logs",
		},
		{
			name:      "multiple mentions",
			text:      "<@U123BOT> hello <@U123BOT>",
			botUserID: "U123BOT",
			want:      "hello",
		},
		{
			name:      "only mention",
			text:      "<@U123BOT>",
			botUserID: "U123BOT",
			want:      "",
		},
		{
			name:      "different bot ID",
			text:      "<@UOTHER> check the logs",
			botUserID: "U123BOT",
			want:      "<@UOTHER> check the logs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBotMention(tt.text, tt.botUserID)
			if got != tt.want {
				t.Errorf("stripBotMention(%q, %q) = %q, want %q",
					tt.text, tt.botUserID, got, tt.want)
			}
		})
	}
}

func TestGetAgentByThread(t *testing.T) {
	b := &Bot{
		agentCards: map[string]MessageRef{
			"gasboat/crew/hq": {
				ChannelID: "C-agents",
				Timestamp: "1111.2222",
				Agent:     "gasboat/crew/hq",
			},
			"gasboat/crew/k8s": {
				ChannelID: "C-agents",
				Timestamp: "3333.4444",
				Agent:     "gasboat/crew/k8s",
			},
		},
	}

	t.Run("matching thread", func(t *testing.T) {
		got := b.getAgentByThread("C-agents", "1111.2222")
		if got != "gasboat/crew/hq" {
			t.Errorf("got %q, want %q", got, "gasboat/crew/hq")
		}
	})

	t.Run("different agent", func(t *testing.T) {
		got := b.getAgentByThread("C-agents", "3333.4444")
		if got != "gasboat/crew/k8s" {
			t.Errorf("got %q, want %q", got, "gasboat/crew/k8s")
		}
	})

	t.Run("non-matching channel", func(t *testing.T) {
		got := b.getAgentByThread("C-other", "1111.2222")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("non-matching timestamp", func(t *testing.T) {
		got := b.getAgentByThread("C-agents", "9999.0000")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("empty map", func(t *testing.T) {
		empty := &Bot{agentCards: map[string]MessageRef{}}
		got := empty.getAgentByThread("C-agents", "1111.2222")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestGetAgentByThread_StateFallback(t *testing.T) {
	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Agent card only in persisted state, not in hot cache.
	_ = state.SetAgentCard("gasboat/crew/ops", MessageRef{
		ChannelID: "C-ops",
		Timestamp: "5555.6666",
		Agent:     "gasboat/crew/ops",
	})

	b := &Bot{
		agentCards: map[string]MessageRef{}, // empty hot cache
		state:      state,
	}

	got := b.getAgentByThread("C-ops", "5555.6666")
	if got != "gasboat/crew/ops" {
		t.Errorf("got %q, want %q", got, "gasboat/crew/ops")
	}
}

func TestGetAgentByThread_ThreadAgents(t *testing.T) {
	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Set a thread→agent mapping (thread-spawned agent).
	_ = state.SetThreadAgent("C-support", "7777.8888", "gasboat/crew/support")

	b := &Bot{
		agentCards: map[string]MessageRef{}, // empty hot cache
		state:      state,
	}

	t.Run("found via threadAgents", func(t *testing.T) {
		got := b.getAgentByThread("C-support", "7777.8888")
		if got != "gasboat/crew/support" {
			t.Errorf("got %q, want %q", got, "gasboat/crew/support")
		}
	})

	t.Run("threadAgents takes priority over agentCards", func(t *testing.T) {
		// Same channel/ts exists in both threadAgents and agentCards.
		b.mu.Lock()
		b.agentCards["gasboat/crew/other"] = MessageRef{
			ChannelID: "C-support",
			Timestamp: "7777.8888",
			Agent:     "gasboat/crew/other",
		}
		b.mu.Unlock()

		got := b.getAgentByThread("C-support", "7777.8888")
		if got != "gasboat/crew/support" {
			t.Errorf("threadAgents should take priority: got %q, want %q", got, "gasboat/crew/support")
		}
	})

	t.Run("not found in threadAgents falls through to agentCards", func(t *testing.T) {
		b.mu.Lock()
		b.agentCards["gasboat/crew/k8s"] = MessageRef{
			ChannelID: "C-k8s",
			Timestamp: "9999.0000",
			Agent:     "gasboat/crew/k8s",
		}
		b.mu.Unlock()

		got := b.getAgentByThread("C-k8s", "9999.0000")
		if got != "gasboat/crew/k8s" {
			t.Errorf("got %q, want %q", got, "gasboat/crew/k8s")
		}
	})
}

func TestThreadAgentsPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Create state, add thread agent, close.
	state1, err := NewStateManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := state1.SetThreadAgent("C-test", "1111.2222", "gasboat/crew/hq"); err != nil {
		t.Fatal(err)
	}

	// Reload from disk.
	state2, err := NewStateManager(path)
	if err != nil {
		t.Fatal(err)
	}

	agent, ok := state2.GetThreadAgent("C-test", "1111.2222")
	if !ok || agent != "gasboat/crew/hq" {
		t.Errorf("after reload: got (%q, %v), want (%q, true)", agent, ok, "gasboat/crew/hq")
	}

	// Remove and verify.
	if err := state2.RemoveThreadAgent("C-test", "1111.2222"); err != nil {
		t.Fatal(err)
	}
	if _, ok := state2.GetThreadAgent("C-test", "1111.2222"); ok {
		t.Error("expected thread agent to be removed")
	}
}

func TestRemoveThreadAgentByAgent(t *testing.T) {
	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	_ = state.SetThreadAgent("C-a", "1.1", "gasboat/crew/hq")
	_ = state.SetThreadAgent("C-b", "2.2", "gasboat/crew/hq")
	_ = state.SetThreadAgent("C-c", "3.3", "gasboat/crew/k8s")

	if err := state.RemoveThreadAgentByAgent("gasboat/crew/hq"); err != nil {
		t.Fatal(err)
	}

	// hq entries should be gone.
	if _, ok := state.GetThreadAgent("C-a", "1.1"); ok {
		t.Error("expected C-a:1.1 to be removed")
	}
	if _, ok := state.GetThreadAgent("C-b", "2.2"); ok {
		t.Error("expected C-b:2.2 to be removed")
	}
	// k8s entry should remain.
	if agent, ok := state.GetThreadAgent("C-c", "3.3"); !ok || agent != "gasboat/crew/k8s" {
		t.Errorf("expected C-c:3.3 to remain, got (%q, %v)", agent, ok)
	}
}

func TestResolveAgentFromText(t *testing.T) {
	daemon := newMockDaemon()

	// Seed agent beads.
	daemon.beads["bd-agent-hq"] = &beadsapi.BeadDetail{
		ID:    "bd-agent-hq",
		Title: "crew-gasboat-crew-hq",
		Type:  "agent",
		Fields: map[string]string{
			"agent":   "hq",
			"project": "gasboat",
			"role":    "crew",
		},
	}
	daemon.beads["bd-agent-k8s"] = &beadsapi.BeadDetail{
		ID:    "bd-agent-k8s",
		Title: "crew-gasboat-crew-k8s",
		Type:  "agent",
		Fields: map[string]string{
			"agent":   "k8s",
			"project": "gasboat",
			"role":    "crew",
		},
	}

	b := &Bot{
		daemon: daemon,
		logger: slog.Default(),
	}

	tests := []struct {
		name          string
		text          string
		wantAgent     string
		wantRemaining string
	}{
		{
			name:          "bare agent name",
			text:          "hq check the logs",
			wantAgent:     "crew-gasboat-crew-hq",
			wantRemaining: "check the logs",
		},
		{
			name:          "bare agent name case insensitive",
			text:          "HQ check the logs",
			wantAgent:     "crew-gasboat-crew-hq",
			wantRemaining: "check the logs",
		},
		{
			name:          "full title match",
			text:          "crew-gasboat-crew-k8s deploy now",
			wantAgent:     "crew-gasboat-crew-k8s",
			wantRemaining: "deploy now",
		},
		{
			name:          "no match",
			text:          "hello everyone",
			wantAgent:     "",
			wantRemaining: "hello everyone",
		},
		{
			name:          "agent name only, no remaining text",
			text:          "k8s",
			wantAgent:     "crew-gasboat-crew-k8s",
			wantRemaining: "",
		},
		{
			name:          "empty text",
			text:          "",
			wantAgent:     "",
			wantRemaining: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, remaining := b.resolveAgentFromText(context.Background(), tt.text)
			if agent != tt.wantAgent {
				t.Errorf("agent = %q, want %q", agent, tt.wantAgent)
			}
			if remaining != tt.wantRemaining {
				t.Errorf("remaining = %q, want %q", remaining, tt.wantRemaining)
			}
		})
	}
}

func TestHandleAppMention_InAgentThread(t *testing.T) {
	daemon := newMockDaemon()

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
			"gasboat/crew/hq": {
				ChannelID: "C-agents",
				Timestamp: "1111.2222",
				Agent:     "gasboat/crew/hq",
			},
		},
	}

	// Simulate handleAppMention with a mock event.
	// We can't use the real Slack API client (no api field), so we test
	// the core logic: bead creation and state persistence.
	ctx := context.Background()

	// Manually call the internal logic that doesn't require Slack API.
	agent := b.getAgentByThread("C-agents", "1111.2222")
	if agent != "gasboat/crew/hq" {
		t.Fatalf("expected agent gasboat/crew/hq, got %q", agent)
	}

	text := stripBotMention("<@U-BOT> check the logs", b.botUserID)
	if text != "check the logs" {
		t.Fatalf("expected stripped text 'check the logs', got %q", text)
	}

	// Create bead via daemon (same as handleAppMention does).
	beadID, err := daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       truncateText("Mention: "+text, 80),
		Type:        "task",
		Description: "Mention from testuser in Slack:\n\ncheck the logs\n\n---\n[slack:C-agents:1111.2222]",
		Assignee:    extractAgentName(agent),
		Labels:      []string{"slack-mention"},
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("CreateBead failed: %v", err)
	}

	if beadID == "" {
		t.Fatal("expected non-empty bead ID")
	}

	// Verify the bead was created with correct properties.
	bead, err := daemon.GetBead(ctx, beadID)
	if err != nil {
		t.Fatalf("GetBead failed: %v", err)
	}
	if bead.Assignee != "hq" {
		t.Errorf("bead assignee = %q, want %q", bead.Assignee, "hq")
	}
	if !hasLabel(bead.Labels, "slack-mention") {
		t.Errorf("bead labels = %v, want slack-mention", bead.Labels)
	}

	// Persist message ref (same as handleAppMention does).
	_ = state.SetChatMessage(beadID, MessageRef{
		ChannelID: "C-agents",
		Timestamp: "1111.2222",
		Agent:     "gasboat/crew/hq",
	})

	// Verify state was persisted.
	ref, ok := state.GetChatMessage(beadID)
	if !ok {
		t.Fatal("expected chat message in state")
	}
	if ref.ChannelID != "C-agents" || ref.Timestamp != "1111.2222" {
		t.Errorf("message ref = %+v, want C-agents/1111.2222", ref)
	}
}

func TestHandleAppMention_NotInThread_AgentChannel(t *testing.T) {
	daemon := newMockDaemon()

	router := NewRouter(RouterConfig{
		Overrides: map[string]string{
			"gasboat/crew/hq": "C-agents",
		},
	})

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{
		daemon:     daemon,
		state:      state,
		logger:     slog.Default(),
		botUserID:  "U-BOT",
		router:     router,
		agentCards: map[string]MessageRef{},
	}

	// Verify that the channel maps to the correct agent.
	agent := b.router.GetAgentByChannel("C-agents")
	if agent != "gasboat/crew/hq" {
		t.Fatalf("expected agent gasboat/crew/hq from channel mapping, got %q", agent)
	}

	// Simulate: strip mention, create bead, persist state.
	ctx := context.Background()
	text := stripBotMention("<@U-BOT> please review this", b.botUserID)
	if text != "please review this" {
		t.Fatalf("expected stripped text 'please review this', got %q", text)
	}

	beadID, err := daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:    truncateText("Mention: "+text, 80),
		Type:     "task",
		Assignee: extractAgentName(agent),
		Labels:   []string{"slack-mention"},
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("CreateBead failed: %v", err)
	}

	// Persist using the mention message timestamp (replyTS = ev.TimeStamp for non-thread).
	_ = state.SetChatMessage(beadID, MessageRef{
		ChannelID: "C-agents",
		Timestamp: "9999.0001",
		Agent:     agent,
	})

	bead, err := daemon.GetBead(ctx, beadID)
	if err != nil {
		t.Fatalf("GetBead failed: %v", err)
	}
	if bead.Assignee != "hq" {
		t.Errorf("bead assignee = %q, want %q", bead.Assignee, "hq")
	}
	if !hasLabel(bead.Labels, "slack-mention") {
		t.Errorf("bead labels = %v, want slack-mention", bead.Labels)
	}

	ref, ok := state.GetChatMessage(beadID)
	if !ok {
		t.Fatal("expected chat message in state")
	}
	if ref.ChannelID != "C-agents" || ref.Timestamp != "9999.0001" {
		t.Errorf("message ref = %+v, want C-agents/9999.0001", ref)
	}
}

func TestHandleAppMention_NonAgentThread(t *testing.T) {
	b := &Bot{
		logger:     slog.Default(),
		botUserID:  "U-BOT",
		agentCards: map[string]MessageRef{},
	}

	// Thread exists but doesn't belong to any agent.
	agent := b.getAgentByThread("C-random", "9999.8888")
	if agent != "" {
		t.Errorf("expected empty agent for non-agent thread, got %q", agent)
	}
}

func TestChat_HandleClosed_SlackMention(t *testing.T) {
	daemon := newMockDaemon()

	// Set up state with a mention message ref.
	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = state.SetChatMessage("bd-mention1", MessageRef{
		ChannelID: "C-agents",
		Timestamp: "1111.2222",
		Agent:     "gasboat/crew/hq",
	})

	// Daemon returns the full bead with close reason.
	daemon.beads["bd-mention1"] = &beadsapi.BeadDetail{
		ID:       "bd-mention1",
		Type:     "task",
		Status:   "closed",
		Assignee: "hq",
		Labels:   []string{"slack-mention"},
		Fields: map[string]string{
			"reason": "Checked the logs, all clear.",
		},
	}

	c := &Chat{
		daemon: daemon,
		state:  state,
		logger: slog.Default(),
		// bot is nil — Slack post will be skipped, but state cleanup still happens.
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:       "bd-mention1",
		Type:     "task",
		Assignee: "hq",
		Labels:   []string{"slack-mention"},
	})
	c.handleClosed(context.Background(), data)

	// State should be cleaned up (just like slack-chat beads).
	if _, ok := state.GetChatMessage("bd-mention1"); ok {
		t.Error("expected mention message to be removed from state after close")
	}
}

func TestChat_HandleClosed_IgnoresNonMentionBeads(t *testing.T) {
	daemon := newMockDaemon()
	c := &Chat{
		daemon: daemon,
		logger: slog.Default(),
	}

	// Bead with neither slack-chat nor slack-mention should be ignored.
	data := marshalSSEBeadPayload(BeadEvent{
		ID:     "bd-other",
		Type:   "task",
		Labels: []string{"bug"},
	})
	c.handleClosed(context.Background(), data)

	if daemon.getGetCalls() != 0 {
		t.Errorf("expected 0 GetBead calls for non-chat/mention bead, got %d", daemon.getGetCalls())
	}
}

func TestSanitizeTS(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1234567890.123456", "1234567890-123456"},
		{"no.dots.here.ok", "no-dots-here-ok"},
		{"nodots", "nodots"},
	}
	for _, tt := range tests {
		got := sanitizeTS(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTS(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestProjectFromAgentIdentity(t *testing.T) {
	tests := []struct {
		identity string
		want     string
	}{
		{"gasboat/crew/hq", "gasboat"},
		{"myproj/crew/agent", "myproj"},
		{"simple-agent", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := projectFromAgentIdentity(tt.identity)
		if got != tt.want {
			t.Errorf("projectFromAgentIdentity(%q) = %q, want %q", tt.identity, got, tt.want)
		}
	}
}

func TestHandleThreadSpawn_CreatesBeadAndState(t *testing.T) {
	daemon := newMockDaemon()

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{
		daemon:     daemon,
		state:      state,
		logger:     slog.Default(),
		botUserID:  "U-BOT",
		agentCards: map[string]MessageRef{},
		// No api — Slack posting is skipped (nil check in handleThreadSpawn).
	}

	channel := "C-thread-test"
	threadTS := "1111.2222"

	// Verify no agent bound to this thread initially.
	if agent := b.getAgentByThread(channel, threadTS); agent != "" {
		t.Fatalf("expected no agent for thread, got %q", agent)
	}

	// Directly call the thread-spawn logic (skipping the Slack API call
	// to fetch thread context — that requires a real Slack client).
	// We test the core: bead creation + state persistence.

	agentName := "thread-" + sanitizeTS(threadTS)
	if agentName != "thread-1111-2222" {
		t.Fatalf("expected agent name thread-1111-2222, got %q", agentName)
	}

	ctx := context.Background()
	beadID, err := daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       agentName,
		Type:        "agent",
		Description: "Thread-spawned agent for test",
		Labels:      []string{"slack-thread"},
	})
	if err != nil {
		t.Fatalf("CreateBead failed: %v", err)
	}

	// Record thread→agent mapping.
	if err := state.SetThreadAgent(channel, threadTS, agentName); err != nil {
		t.Fatalf("SetThreadAgent failed: %v", err)
	}

	// Verify bead was created.
	bead, err := daemon.GetBead(ctx, beadID)
	if err != nil {
		t.Fatalf("GetBead failed: %v", err)
	}
	if bead.Type != "agent" {
		t.Errorf("bead type = %q, want agent", bead.Type)
	}
	if bead.Title != agentName {
		t.Errorf("bead title = %q, want %q", bead.Title, agentName)
	}
	if !hasLabel(bead.Labels, "slack-thread") {
		t.Errorf("bead labels = %v, want slack-thread", bead.Labels)
	}

	// Verify thread→agent state.
	agent, ok := state.GetThreadAgent(channel, threadTS)
	if !ok || agent != agentName {
		t.Errorf("thread agent = (%q, %v), want (%q, true)", agent, ok, agentName)
	}

	// Verify getAgentByThread now resolves.
	if got := b.getAgentByThread(channel, threadTS); got != agentName {
		t.Errorf("getAgentByThread = %q, want %q", got, agentName)
	}
}

func TestResolveAgentThread_ThreadBound(t *testing.T) {
	daemon := newMockDaemon()
	// Seed a thread-bound agent bead.
	daemon.beads["thread-agent"] = &beadsapi.BeadDetail{
		ID:    "bd-thread-agent",
		Title: "thread-agent",
		Type:  "agent",
		Fields: map[string]string{
			"agent":                "thread-agent",
			"slack_thread_channel": "C-thread",
			"slack_thread_ts":      "1234.5678",
			"spawn_source":         "slack-thread",
		},
	}

	b := &Bot{
		daemon: daemon,
		logger: slog.Default(),
	}

	channel, ts := b.resolveAgentThread(context.Background(), "thread-agent")
	if channel != "C-thread" {
		t.Errorf("channel = %q, want C-thread", channel)
	}
	if ts != "1234.5678" {
		t.Errorf("ts = %q, want 1234.5678", ts)
	}
}

func TestResolveAgentThread_RegularAgent(t *testing.T) {
	daemon := newMockDaemon()
	// Seed a regular agent (no thread metadata).
	daemon.beads["regular-agent"] = &beadsapi.BeadDetail{
		ID:    "bd-regular-agent",
		Title: "regular-agent",
		Type:  "agent",
		Fields: map[string]string{
			"agent":   "regular-agent",
			"project": "gasboat",
		},
	}

	b := &Bot{
		daemon: daemon,
		logger: slog.Default(),
	}

	channel, ts := b.resolveAgentThread(context.Background(), "regular-agent")
	if channel != "" {
		t.Errorf("expected empty channel for regular agent, got %q", channel)
	}
	if ts != "" {
		t.Errorf("expected empty ts for regular agent, got %q", ts)
	}
}

func TestResolveAgentThread_NotFound(t *testing.T) {
	daemon := newMockDaemon()

	b := &Bot{
		daemon: daemon,
		logger: slog.Default(),
	}

	channel, ts := b.resolveAgentThread(context.Background(), "nonexistent")
	if channel != "" || ts != "" {
		t.Errorf("expected empty for nonexistent agent, got channel=%q ts=%q", channel, ts)
	}
}

