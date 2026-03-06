package bridge

import (
	"context"
	"log/slog"
	"testing"

	"github.com/slack-go/slack/slackevents"
)

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

func TestHandleThreadSpawn_ProjectFromProjectBead(t *testing.T) {
	daemon := newMockDaemon()
	// Seed a project bead with slack_channel — this is how /spawn resolves projects.
	daemon.seedProjectWithChannel("monorepo", "C-DEVOPS")

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.botUserID = "U-BOT"
	// No router overrides for C-DEVOPS — project must come from project bead.

	ev := &slackevents.AppMentionEvent{
		User:            "U-HUMAN",
		Channel:         "C-DEVOPS",
		ThreadTimeStamp: "9999.1111",
		TimeStamp:       "9999.2222",
		Text:            "<@U-BOT> help with this",
	}

	bot.handleThreadSpawn(context.Background(), ev, "help with this")

	// Find the agent bead that was created.
	agentBeads := filterAgentBeads(daemon.beads)
	if len(agentBeads) != 1 {
		t.Fatalf("expected 1 agent bead, got %d", len(agentBeads))
	}
	for _, b := range agentBeads {
		if b.Fields["project"] != "monorepo" {
			t.Errorf("expected project=monorepo from project bead slack_channel, got %q", b.Fields["project"])
		}
	}
}
