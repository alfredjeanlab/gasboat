package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack/slackevents"
)

func TestHandleChatForward_SkipsBotMention(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	router := NewRouter(RouterConfig{
		Overrides: map[string]string{
			"gasboat/crew/hq": "C-agents",
		},
	})

	b := newTestBot(daemon, slackSrv)
	b.botUserID = "U-BOT"
	b.router = router

	// Message containing a bot mention should be skipped (handled by handleAppMention).
	ev := &slackevents.MessageEvent{
		User:      "U-HUMAN",
		Channel:   "C-agents",
		Text:      "<@U-BOT> check the logs",
		TimeStamp: "1234.5678",
	}

	b.handleChatForward(context.Background(), ev)

	// No beads should have been created (skipped due to bot mention).
	if calls := daemon.getGetCalls(); calls != 0 {
		t.Errorf("expected 0 daemon calls for bot-mention message, got %d", calls)
	}
}

func TestHandleChatForward_NudgesAgent(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	// Set up a mock coop server that accepts nudges.
	nudgeReceived := false
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nudgeReceived = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"delivered":true}`)
	}))
	defer coopServer.Close()

	// Seed an agent bead with coop_url.
	daemon.beads["hq"] = &beadsapi.BeadDetail{
		ID:     "bd-agent-hq",
		Title:  "hq",
		Type:   "agent",
		Fields: map[string]string{"agent": "hq", "project": "gasboat", "role": "crew"},
		Notes:  "coop_url: " + coopServer.URL,
	}

	router := NewRouter(RouterConfig{
		Overrides: map[string]string{
			"gasboat/crew/hq": "C-agents",
		},
	})

	b := newTestBot(daemon, slackSrv)
	b.botUserID = "U-BOT"
	b.router = router

	// Regular message (no bot mention) in a mapped channel.
	ev := &slackevents.MessageEvent{
		User:      "U-HUMAN",
		Channel:   "C-agents",
		Text:      "please check the deployment status",
		TimeStamp: "1234.5678",
	}

	b.handleChatForward(context.Background(), ev)

	// Agent should have been nudged.
	if !nudgeReceived {
		t.Error("expected agent to be nudged for chat-forwarded message")
	}
}

func TestHandleThreadSpawn_WithRouter(t *testing.T) {
	daemon := newMockDaemon()

	router := NewRouter(RouterConfig{
		Overrides: map[string]string{
			"gasboat/crew/hq": "C-agents",
		},
	})

	b := &Bot{
		daemon:     daemon,
		state:      nil, // no state persistence in this test
		logger:     slog.Default(),
		botUserID:  "U-BOT",
		router:     router,
		agentCards: map[string]MessageRef{},
	}

	// Verify project inference from router.
	mapped := router.GetAgentByChannel("C-agents")
	if mapped != "gasboat/crew/hq" {
		t.Fatalf("expected gasboat/crew/hq, got %q", mapped)
	}

	project := projectFromAgentIdentity(mapped)
	if project != "gasboat" {
		t.Errorf("project = %q, want gasboat", project)
	}

	// For unmapped channel, project should be empty.
	mapped = router.GetAgentByChannel("C-random")
	if mapped != "" {
		t.Errorf("expected empty agent for unmapped channel, got %q", mapped)
	}
	_ = b // used
}
