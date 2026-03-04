package bridge

import (
	"log/slog"
	"testing"
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
