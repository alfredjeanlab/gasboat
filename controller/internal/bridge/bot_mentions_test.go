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

	ctx := context.Background()

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

	// Persist message ref.
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

func TestHandleAppMention_ThreadInAgentChannel_RoutesToAgent(t *testing.T) {
	// Bug: mentioning @gasboat in a thread within an agent's break-out channel
	// would spawn a new thread runner instead of routing to the channel's agent.
	daemon := newMockDaemon()

	// Seed an active agent bead so FindAgentBead("hq") succeeds.
	// The mock looks up by map key, so use "hq" (the short name).
	daemon.beads["hq"] = &beadsapi.BeadDetail{
		ID:    "bd-agent-hq",
		Title: "crew-gasboat-crew-hq",
		Type:  "agent",
		Fields: map[string]string{
			"agent":   "hq",
			"project": "gasboat",
			"role":    "crew",
		},
	}

	// Set up a router with the agent's break-out channel.
	router := NewRouter(RouterConfig{
		Overrides: map[string]string{
			"gasboat/crew/hq": "C-agent-hq",
		},
	})

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	b := newTestBot(daemon, slackSrv)
	b.state = state
	b.router = router
	b.botUserID = "U-BOT"
	b.lastThreadNudge = make(map[string]time.Time)

	// Mention @gasboat in a thread within the agent's channel,
	// but the thread is NOT the agent card thread (it's a random thread).
	b.handleAppMention(context.Background(), &slackevents.AppMentionEvent{
		User:            "U-USER",
		Text:            "<@U-BOT> check the logs",
		TimeStamp:       "2222.3333",
		ThreadTimeStamp: "1111.0000", // some thread that isn't the agent's card
		Channel:         "C-agent-hq",
	})

	// Verify that a mention bead was created for the existing agent (hq),
	// NOT an agent bead for a new thread runner.
	agentBeads := filterAgentBeads(daemon.beads)
	for _, ab := range agentBeads {
		if ab.ID != "bd-agent-hq" {
			t.Errorf("unexpected new agent bead created: %s (%s) — should route to existing agent", ab.ID, ab.Title)
		}
	}

	// Should have created a task bead (mention tracking) assigned to hq.
	taskBeads := filterBeadsByType(daemon.beads, "task")
	if len(taskBeads) == 0 {
		t.Fatal("expected a mention tracking bead to be created")
	}
	for _, tb := range taskBeads {
		if tb.Assignee != "hq" {
			t.Errorf("mention bead assignee = %q, want %q", tb.Assignee, "hq")
		}
	}
}

func TestChat_HandleClosed_SlackMention(t *testing.T) {
	daemon := newMockDaemon()

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
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:       "bd-mention1",
		Type:     "task",
		Assignee: "hq",
		Labels:   []string{"slack-mention"},
	})
	c.handleClosed(context.Background(), data)

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

func TestParseProjectOverride(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantProject   string
		wantRemaining string
	}{
		{
			name:          "project: syntax",
			text:          "project:gasboat fix the helm chart",
			wantProject:   "gasboat",
			wantRemaining: "fix the helm chart",
		},
		{
			name:          "--project syntax",
			text:          "--project gasboat fix the helm chart",
			wantProject:   "gasboat",
			wantRemaining: "fix the helm chart",
		},
		{
			name:          "project: no remaining text",
			text:          "project:monorepo",
			wantProject:   "monorepo",
			wantRemaining: "",
		},
		{
			name:          "--project no remaining text",
			text:          "--project monorepo",
			wantProject:   "monorepo",
			wantRemaining: "",
		},
		{
			name:          "no override",
			text:          "fix the helm chart",
			wantProject:   "",
			wantRemaining: "fix the helm chart",
		},
		{
			name:          "empty text",
			text:          "",
			wantProject:   "",
			wantRemaining: "",
		},
		{
			name:          "project: with empty value",
			text:          "project: something",
			wantProject:   "",
			wantRemaining: "project: something",
		},
		{
			name:          "--project with no value",
			text:          "--project",
			wantProject:   "",
			wantRemaining: "--project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project, remaining := parseProjectOverride(tt.text)
			if project != tt.wantProject {
				t.Errorf("project = %q, want %q", project, tt.wantProject)
			}
			if remaining != tt.wantRemaining {
				t.Errorf("remaining = %q, want %q", remaining, tt.wantRemaining)
			}
		})
	}
}

func TestParseListenFlag(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantListen    bool
		wantRemaining string
	}{
		{
			name:          "no flag",
			text:          "fix the helm chart",
			wantListen:    false,
			wantRemaining: "fix the helm chart",
		},
		{
			name:          "listen at start",
			text:          "--listen fix the helm chart",
			wantListen:    true,
			wantRemaining: "fix the helm chart",
		},
		{
			name:          "listen at end",
			text:          "fix the helm chart --listen",
			wantListen:    true,
			wantRemaining: "fix the helm chart",
		},
		{
			name:          "listen in middle",
			text:          "fix --listen the helm chart",
			wantListen:    true,
			wantRemaining: "fix the helm chart",
		},
		{
			name:          "listen only",
			text:          "--listen",
			wantListen:    true,
			wantRemaining: "",
		},
		{
			name:          "empty text",
			text:          "",
			wantListen:    false,
			wantRemaining: "",
		},
		{
			name:          "listen with project",
			text:          "--listen project:gasboat fix things",
			wantListen:    true,
			wantRemaining: "project:gasboat fix things",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listen, remaining := parseListenFlag(tt.text)
			if listen != tt.wantListen {
				t.Errorf("listen = %v, want %v", listen, tt.wantListen)
			}
			if remaining != tt.wantRemaining {
				t.Errorf("remaining = %q, want %q", remaining, tt.wantRemaining)
			}
		})
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
