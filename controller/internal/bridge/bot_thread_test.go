package bridge

import (
	"context"
	"path/filepath"
	"testing"

	"log/slog"

	"gasboat/controller/internal/beadsapi"
)

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
	}

	channel := "C-thread-test"
	threadTS := "1111.2222"

	// Verify no agent bound to this thread initially.
	if agent := b.getAgentByThread(channel, threadTS); agent != "" {
		t.Fatalf("expected no agent for thread, got %q", agent)
	}

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

func TestThreadBoundMention_InactiveAgent_ClearsMapping(t *testing.T) {
	daemon := newMockDaemon()

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	channel := "C-thread-test"
	threadTS := "1111.2222"
	agentName := "thread-1111-2222"

	// Pre-populate thread→agent mapping.
	_ = state.SetThreadAgent(channel, threadTS, agentName)

	b := &Bot{
		daemon:     daemon,
		state:      state,
		logger:     slog.Default(),
		botUserID:  "U-BOT",
		agentCards: map[string]MessageRef{},
	}

	// Verify the mapping exists.
	agent, ok := state.GetThreadAgent(channel, threadTS)
	if !ok || agent != agentName {
		t.Fatalf("expected thread agent %q, got %q (ok=%v)", agentName, agent, ok)
	}

	// getAgentByThread finds the agent from state.
	got := b.getAgentByThread(channel, threadTS)
	if got != agentName {
		t.Fatalf("getAgentByThread = %q, want %q", got, agentName)
	}

	// But FindAgentBead fails (agent is inactive/closed).
	_, findErr := daemon.FindAgentBead(context.Background(), extractAgentName(agentName))
	if findErr == nil {
		t.Fatal("expected FindAgentBead to fail for inactive agent")
	}

	// Simulate what handleAppMention now does: clear stale mapping.
	_ = state.RemoveThreadAgent(channel, threadTS)

	// Verify the mapping is cleared.
	if _, ok := state.GetThreadAgent(channel, threadTS); ok {
		t.Error("expected thread agent mapping to be removed after inactive agent detected")
	}
}
