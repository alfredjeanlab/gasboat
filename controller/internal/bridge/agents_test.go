package bridge

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

// mockAgentNotifier records calls to NotifyAgentCrash, NotifyAgentSpawn, NotifyAgentState, and NotifyAgentTaskUpdate.
type mockAgentNotifier struct {
	mu           sync.Mutex
	crashes      []BeadEvent
	spawns       []BeadEvent
	stateChanges []BeadEvent
	taskUpdates  []string
}

func (m *mockAgentNotifier) NotifyAgentCrash(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.crashes = append(m.crashes, bead)
	return nil
}

func (m *mockAgentNotifier) NotifyAgentSpawn(_ context.Context, bead BeadEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spawns = append(m.spawns, bead)
}

func (m *mockAgentNotifier) NotifyAgentState(_ context.Context, bead BeadEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateChanges = append(m.stateChanges, bead)
}

func (m *mockAgentNotifier) NotifyAgentTaskUpdate(_ context.Context, agentName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskUpdates = append(m.taskUpdates, agentName)
}

func (m *mockAgentNotifier) getCrashes() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.crashes...)
}

func (m *mockAgentNotifier) getSpawns() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.spawns...)
}

func (m *mockAgentNotifier) getStateChanges() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.stateChanges...)
}

func (m *mockAgentNotifier) getTaskUpdates() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.taskUpdates...)
}

func TestAgents_HandleClosed_CrashNotification(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Non-agent bead should be ignored.
	nonAgent := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-1",
		Type: "decision",
		Fields: map[string]string{
			"agent_state": "failed",
		},
	})
	a.handleClosed(context.Background(), nonAgent)
	if len(notif.getCrashes()) != 0 {
		t.Fatal("non-agent bead should not trigger crash notification")
	}

	// Agent bead closing with agent_state=done should be ignored.
	doneAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-1",
		Type:     "agent",
		Assignee: "gasboat/crew/test-bot",
		Fields: map[string]string{
			"agent_state": "done",
		},
	})
	a.handleClosed(context.Background(), doneAgent)
	if len(notif.getCrashes()) != 0 {
		t.Fatal("agent with state=done should not trigger crash notification")
	}

	// Agent bead closing with agent_state=failed should trigger notification.
	crashedAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-2",
		Type:     "agent",
		Title:    "crew-gasboat-crew-test-bot",
		Assignee: "gasboat/crew/test-bot",
		Fields: map[string]string{
			"agent_state": "failed",
			"pod_name":    "crew-gasboat-crew-test-bot-xyz",
		},
	})
	a.handleClosed(context.Background(), crashedAgent)

	crashes := notif.getCrashes()
	if len(crashes) != 1 {
		t.Fatalf("expected 1 crash notification, got %d", len(crashes))
	}
	if crashes[0].ID != "agent-2" {
		t.Errorf("expected bead ID agent-2, got %s", crashes[0].ID)
	}
	if crashes[0].Assignee != "gasboat/crew/test-bot" {
		t.Errorf("expected assignee gasboat/crew/test-bot, got %s", crashes[0].Assignee)
	}
}

func TestAgents_HandleUpdated_PodPhaseFailed(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Agent updated with pod_phase=failed should trigger notification.
	failedPod := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-3",
		Type:     "agent",
		Assignee: "gasboat/crew/worker-1",
		Fields: map[string]string{
			"agent_state": "working",
			"pod_phase":   "failed",
			"pod_name":    "crew-gasboat-crew-worker-1-abc",
		},
	})
	a.handleUpdated(context.Background(), failedPod)

	crashes := notif.getCrashes()
	if len(crashes) != 1 {
		t.Fatalf("expected 1 crash notification, got %d", len(crashes))
	}
	if crashes[0].ID != "agent-3" {
		t.Errorf("expected bead ID agent-3, got %s", crashes[0].ID)
	}
}

func TestAgents_Deduplication(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	crashEvent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-4",
		Type:     "agent",
		Assignee: "gasboat/crew/bot-a",
		Fields: map[string]string{
			"agent_state": "failed",
		},
	})

	// First call: should notify.
	a.handleClosed(context.Background(), crashEvent)
	// Second call (e.g., from SSE reconnect): should be deduplicated.
	a.handleClosed(context.Background(), crashEvent)
	// Third call via updated handler: still deduplicated.
	a.handleUpdated(context.Background(), crashEvent)

	crashes := notif.getCrashes()
	if len(crashes) != 1 {
		t.Fatalf("expected exactly 1 crash notification (dedup), got %d", len(crashes))
	}
}

func TestAgents_NilNotifier(t *testing.T) {
	a := NewAgents(AgentsConfig{
		Notifier: nil,
		Logger:   slog.Default(),
	})

	crashEvent := marshalSSEBeadPayload(BeadEvent{
		ID:   "agent-5",
		Type: "agent",
		Fields: map[string]string{
			"agent_state": "failed",
		},
	})

	// Should not panic even with nil notifier.
	a.handleClosed(context.Background(), crashEvent)
}

// TestAgents_HandleCreated verifies that agent bead creation fires
// NotifyAgentSpawn for agent beads and is skipped for non-agent beads.
func TestAgents_HandleCreated(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Non-agent bead should be ignored.
	nonAgent := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-10",
		Type: "decision",
	})
	a.handleCreated(context.Background(), nonAgent)
	if len(notif.getSpawns()) != 0 {
		t.Fatal("non-agent bead should not trigger spawn notification")
	}

	// Agent bead creation should trigger spawn notification.
	agentBead := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-10",
		Type:     "agent",
		Title:    "crew-gasboat-crew-builder",
		Assignee: "gasboat/crew/builder",
	})
	a.handleCreated(context.Background(), agentBead)

	spawns := notif.getSpawns()
	if len(spawns) != 1 {
		t.Fatalf("expected 1 spawn notification, got %d", len(spawns))
	}
	if spawns[0].ID != "agent-10" {
		t.Errorf("expected bead ID agent-10, got %s", spawns[0].ID)
	}
	if spawns[0].Assignee != "gasboat/crew/builder" {
		t.Errorf("expected assignee gasboat/crew/builder, got %s", spawns[0].Assignee)
	}
}

// TestAgents_HandleClosed_NormalCompletion verifies that a normally completed
// agent bead (not failed) triggers NotifyAgentState with state "done".
func TestAgents_HandleClosed_NormalCompletion(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	doneAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-11",
		Type:     "agent",
		Assignee: "gasboat/crew/finisher",
		Fields: map[string]string{
			"agent_state": "done",
		},
	})
	a.handleClosed(context.Background(), doneAgent)

	if len(notif.getCrashes()) != 0 {
		t.Error("normal completion should not trigger crash notification")
	}
	changes := notif.getStateChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 state change notification, got %d", len(changes))
	}
	if changes[0].Fields["agent_state"] != "done" {
		t.Errorf("expected agent_state=done, got %q", changes[0].Fields["agent_state"])
	}
}

// TestAgents_HandleClosed_NoState verifies that an agent bead closing without
// an explicit agent_state gets defaulted to "done".
func TestAgents_HandleClosed_NoState(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	closedAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-12",
		Type:     "agent",
		Assignee: "gasboat/crew/silent",
		Fields:   map[string]string{},
	})
	a.handleClosed(context.Background(), closedAgent)

	if len(notif.getCrashes()) != 0 {
		t.Error("normal close should not trigger crash notification")
	}
	changes := notif.getStateChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 state change notification, got %d", len(changes))
	}
	if changes[0].Fields["agent_state"] != "done" {
		t.Errorf("expected agent_state defaulted to done, got %q", changes[0].Fields["agent_state"])
	}
}

// TestAgents_HandleUpdated_TaskClaim verifies that a non-agent bead becoming
// in_progress triggers NotifyAgentTaskUpdate for the assignee.
func TestAgents_HandleUpdated_TaskClaim(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Task bead claimed by an agent should trigger NotifyAgentTaskUpdate.
	claimedTask := marshalSSEBeadPayload(BeadEvent{
		ID:       "task-1",
		Type:     "task",
		Status:   "in_progress",
		Assignee: "matt-1",
	})
	a.handleUpdated(context.Background(), claimedTask)

	updates := notif.getTaskUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 task update notification, got %d", len(updates))
	}
	if updates[0] != "matt-1" {
		t.Errorf("expected agentName=matt-1, got %s", updates[0])
	}

	// No state change or crash notifications should have fired.
	if len(notif.getCrashes()) != 0 {
		t.Error("task claim should not trigger crash notification")
	}
	if len(notif.getStateChanges()) != 0 {
		t.Error("task claim should not trigger state change notification")
	}
}

// TestAgents_HandleUpdated_TaskNoAssignee verifies that a non-agent bead update
// without an assignee does not trigger NotifyAgentTaskUpdate.
func TestAgents_HandleUpdated_TaskNoAssignee(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	unassigned := marshalSSEBeadPayload(BeadEvent{
		ID:     "task-2",
		Type:   "task",
		Status: "in_progress",
		// No assignee.
	})
	a.handleUpdated(context.Background(), unassigned)

	if len(notif.getTaskUpdates()) != 0 {
		t.Error("task with no assignee should not trigger task update notification")
	}
}

// TestAgents_HandleClosed_TaskBead verifies that closing a task bead assigned to
// an agent triggers NotifyAgentTaskUpdate so the card clears the completed task.
func TestAgents_HandleClosed_TaskBead(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Task bead closed by its agent assignee should trigger a card refresh.
	closedTask := marshalSSEBeadPayload(BeadEvent{
		ID:       "task-10",
		Type:     "task",
		Status:   "closed",
		Assignee: "matt-1",
	})
	a.handleClosed(context.Background(), closedTask)

	updates := notif.getTaskUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 task update notification on close, got %d", len(updates))
	}
	if updates[0] != "matt-1" {
		t.Errorf("expected agentName=matt-1, got %s", updates[0])
	}
	// No crash or state change notifications should have fired.
	if len(notif.getCrashes()) != 0 {
		t.Error("task close should not trigger crash notification")
	}
	if len(notif.getStateChanges()) != 0 {
		t.Error("task close should not trigger state change notification")
	}
}

// TestAgents_HandleClosed_TaskBeadNoAssignee verifies that closing an unassigned
// task bead does not trigger NotifyAgentTaskUpdate.
func TestAgents_HandleClosed_TaskBeadNoAssignee(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	closedTask := marshalSSEBeadPayload(BeadEvent{
		ID:     "task-11",
		Type:   "task",
		Status: "closed",
		// No Assignee.
	})
	a.handleClosed(context.Background(), closedTask)

	if len(notif.getTaskUpdates()) != 0 {
		t.Error("unassigned task close should not trigger task update notification")
	}
}

// TestAgents_HandleUpdated_TaskClose verifies that a task bead updated with
// status=closed triggers NotifyAgentTaskUpdate (defensive coverage).
func TestAgents_HandleUpdated_TaskClose(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	closedTask := marshalSSEBeadPayload(BeadEvent{
		ID:       "task-12",
		Type:     "bug",
		Status:   "closed",
		Assignee: "builder-1",
	})
	a.handleUpdated(context.Background(), closedTask)

	updates := notif.getTaskUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 task update notification for closed status, got %d", len(updates))
	}
	if updates[0] != "builder-1" {
		t.Errorf("expected agentName=builder-1, got %s", updates[0])
	}
}

// TestAgents_HandleUpdated_TaskUnassigned verifies that when a task bead's
// assignee is cleared (unclaimed), the previous assignee's card is refreshed.
func TestAgents_HandleUpdated_TaskUnassigned(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Step 1: Task claimed by matt-1 — should trigger card refresh and track assignee.
	claimedTask := marshalSSEBeadPayload(BeadEvent{
		ID:       "task-20",
		Type:     "task",
		Status:   "in_progress",
		Assignee: "matt-1",
	})
	a.handleUpdated(context.Background(), claimedTask)
	if len(notif.getTaskUpdates()) != 1 {
		t.Fatalf("expected 1 task update after claim, got %d", len(notif.getTaskUpdates()))
	}

	// Step 2: Task unclaimed (assignee cleared) — should refresh matt-1's card.
	unclaimedTask := marshalSSEBeadPayload(BeadEvent{
		ID:     "task-20",
		Type:   "task",
		Status: "open",
		// No assignee — cleared.
	})
	a.handleUpdated(context.Background(), unclaimedTask)

	updates := notif.getTaskUpdates()
	if len(updates) != 2 {
		t.Fatalf("expected 2 task updates (claim + unclaim), got %d", len(updates))
	}
	if updates[1] != "matt-1" {
		t.Errorf("expected previous assignee matt-1 notified on unclaim, got %s", updates[1])
	}
}

// TestAgents_HandleClosed_TaskBeadPreviousAssignee verifies that closing a task
// bead with empty assignee still refreshes the previous assignee's card.
func TestAgents_HandleClosed_TaskBeadPreviousAssignee(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Step 1: Task claimed by builder-2 — track the assignee.
	claimedTask := marshalSSEBeadPayload(BeadEvent{
		ID:       "task-21",
		Type:     "task",
		Status:   "in_progress",
		Assignee: "builder-2",
	})
	a.handleUpdated(context.Background(), claimedTask)

	// Step 2: Task closed with empty assignee — should still refresh builder-2's card.
	closedTask := marshalSSEBeadPayload(BeadEvent{
		ID:     "task-21",
		Type:   "task",
		Status: "closed",
		// Assignee cleared before close.
	})
	a.handleClosed(context.Background(), closedTask)

	updates := notif.getTaskUpdates()
	if len(updates) != 2 {
		t.Fatalf("expected 2 task updates (claim + close), got %d", len(updates))
	}
	if updates[1] != "builder-2" {
		t.Errorf("expected previous assignee builder-2 notified on close, got %s", updates[1])
	}
}

// TestAgents_HandleUpdated_TaskReassigned verifies that when a task is
// reassigned from one agent to another, the previous agent's card is refreshed.
func TestAgents_HandleUpdated_TaskReassigned(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Step 1: Task claimed by matt-1.
	claimedTask := marshalSSEBeadPayload(BeadEvent{
		ID:       "task-22",
		Type:     "task",
		Status:   "in_progress",
		Assignee: "matt-1",
	})
	a.handleUpdated(context.Background(), claimedTask)

	// Step 2: Task reassigned to matt-2 — should notify matt-2 (new) AND matt-1 (previous).
	reassignedTask := marshalSSEBeadPayload(BeadEvent{
		ID:       "task-22",
		Type:     "task",
		Status:   "in_progress",
		Assignee: "matt-2",
	})
	a.handleUpdated(context.Background(), reassignedTask)

	updates := notif.getTaskUpdates()
	// 3 updates: matt-1 (claim), matt-2 (new assignee), matt-1 (previous assignee refresh).
	if len(updates) != 3 {
		t.Fatalf("expected 3 task updates (claim + new assignee + previous assignee), got %d: %v", len(updates), updates)
	}
	if updates[0] != "matt-1" {
		t.Errorf("expected matt-1 notified on claim, got %s", updates[0])
	}
	if updates[1] != "matt-2" {
		t.Errorf("expected matt-2 notified on reassign, got %s", updates[1])
	}
	if updates[2] != "matt-1" {
		t.Errorf("expected matt-1 (previous assignee) notified on reassign, got %s", updates[2])
	}

	// Verify tracking was updated: unassigning should now notify matt-2, not matt-1.
	unclaimedTask := marshalSSEBeadPayload(BeadEvent{
		ID:     "task-22",
		Type:   "task",
		Status: "open",
	})
	a.handleUpdated(context.Background(), unclaimedTask)

	updates = notif.getTaskUpdates()
	if len(updates) != 4 {
		t.Fatalf("expected 4 task updates, got %d: %v", len(updates), updates)
	}
	if updates[3] != "matt-2" {
		t.Errorf("expected matt-2 (last assignee) notified on unclaim, got %s", updates[3])
	}
}

// TestAgents_HandleClosed_WorkingState verifies that an agent bead closing with
// agent_state="working" gets overridden to "done" (not left as stale "working").
func TestAgents_HandleClosed_WorkingState(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	workingAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-13",
		Type:     "agent",
		Assignee: "gasboat/crew/busy-bot",
		Fields: map[string]string{
			"agent_state": "working",
		},
	})
	a.handleClosed(context.Background(), workingAgent)

	if len(notif.getCrashes()) != 0 {
		t.Error("working agent closing should not trigger crash notification")
	}
	changes := notif.getStateChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 state change notification, got %d", len(changes))
	}
	if changes[0].Fields["agent_state"] != "done" {
		t.Errorf("expected agent_state overridden to done, got %q", changes[0].Fields["agent_state"])
	}
}

// TestAgents_HandleUpdated_StateChange verifies that non-crash state changes
// (e.g. spawning→working) trigger NotifyAgentState, not NotifyAgentCrash.
func TestAgents_HandleUpdated_StateChange(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	workingAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-6",
		Type:     "agent",
		Assignee: "gasboat/crew/runner",
		Fields: map[string]string{
			"agent_state": "working",
			"pod_phase":   "running",
		},
	})
	a.handleUpdated(context.Background(), workingAgent)

	if len(notif.getCrashes()) != 0 {
		t.Error("working state should not trigger crash notification")
	}
	changes := notif.getStateChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 state change notification, got %d", len(changes))
	}
	if changes[0].Fields["agent_state"] != "working" {
		t.Errorf("expected agent_state=working, got %q", changes[0].Fields["agent_state"])
	}
}

func TestExtractImageTag(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"full reference", "ghcr.io/org/agent:v2026.58.3", "v2026.58.3"},
		{"latest tag", "ghcr.io/org/agent:latest", "latest"},
		{"bare tag", "v2026.58.3", "v2026.58.3"},
		{"empty", "", ""},
		{"no tag", "ghcr.io/org/agent", "ghcr.io/org/agent"},
		{"port in registry", "registry:5000/img:v1", "v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractImageTag(tc.input); got != tc.want {
				t.Errorf("extractImageTag(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestBuildAgentCardBlocks_ImageTagInContext(t *testing.T) {
	blocks := buildAgentCardBlocks(
		"gasboat/crew/test-bot",
		0,        // pending
		"working", // state
		"",       // taskTitle
		time.Time{}, // seen (zero)
		"",       // coopmuxURL
		"",       // podName
		"v2026.58.3", // imageTag
	)

	// The context block (index 1) should contain the image tag.
	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(blocks))
	}

	// Render the context block text to check for the image tag.
	// Context blocks contain TextBlockObject elements.
	contextBlock := blocks[1]
	rendered := contextBlock.BlockType()
	if rendered != "context" {
		t.Fatalf("expected context block at index 1, got %q", rendered)
	}

	// Verify the image tag appears in the agent card context.
	// We check all text elements in the context block.
	found := false
	for _, elem := range contextBlock.(*slack.ContextBlock).ContextElements.Elements {
		if textObj, ok := elem.(*slack.TextBlockObject); ok {
			if strings.Contains(textObj.Text, "v2026.58.3") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("expected image tag v2026.58.3 in context block text")
	}
}

func TestBuildAgentCardBlocks_NoImageTag(t *testing.T) {
	blocks := buildAgentCardBlocks(
		"gasboat/crew/test-bot",
		0,
		"working",
		"",
		time.Time{},
		"",
		"",
		"", // no imageTag
	)

	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(blocks))
	}

	contextBlock := blocks[1].(*slack.ContextBlock)
	for _, elem := range contextBlock.ContextElements.Elements {
		if textObj, ok := elem.(*slack.TextBlockObject); ok {
			// The context should still have the bead ID and thread line, but no image tag separator after "Decisions thread below".
			if strings.Contains(textObj.Text, "Decisions thread below ·") && !strings.Contains(textObj.Text, "Decisions thread below · _") {
				// If there's a dot after "Decisions thread below" and it's not the seen timestamp,
				// then something is wrong — an empty tag might have been appended.
				t.Error("unexpected separator after 'Decisions thread below' when imageTag is empty")
			}
		}
	}
}

func TestBuildWrapUpAgentCardBlocks_Done(t *testing.T) {
	wrapupJSON := `{"accomplishments":"Closed 3 bugs","blockers":"API key pending"}`
	blocks := buildWrapUpAgentCardBlocks("gasboat/crew/test-bot", "done", wrapupJSON)

	// Should have 3 blocks: header, wrapup section, action (Clear button).
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}

	// First block: header with agent name and state.
	header := blocks[0].(*slack.SectionBlock)
	if !strings.Contains(header.Text.Text, "test-bot") {
		t.Errorf("expected agent name in header, got: %s", header.Text.Text)
	}
	if !strings.Contains(header.Text.Text, ":white_check_mark:") {
		t.Errorf("expected done indicator in header, got: %s", header.Text.Text)
	}
	if !strings.Contains(header.Text.Text, "done") {
		t.Errorf("expected 'done' state in header, got: %s", header.Text.Text)
	}

	// Second block: wrapup content.
	wrapup := blocks[1].(*slack.SectionBlock)
	if !strings.Contains(wrapup.Text.Text, "Accomplishments") {
		t.Errorf("expected accomplishments in wrapup block, got: %s", wrapup.Text.Text)
	}
	if !strings.Contains(wrapup.Text.Text, "Blockers") {
		t.Errorf("expected blockers in wrapup block, got: %s", wrapup.Text.Text)
	}

	// Third block: action with Clear button.
	if blocks[2].BlockType() != "actions" {
		t.Errorf("expected actions block at index 2, got %q", blocks[2].BlockType())
	}
}

func TestBuildWrapUpAgentCardBlocks_Failed(t *testing.T) {
	wrapupJSON := `{"accomplishments":"Partial work done"}`
	blocks := buildWrapUpAgentCardBlocks("test-bot", "failed", wrapupJSON)

	header := blocks[0].(*slack.SectionBlock)
	if !strings.Contains(header.Text.Text, ":x:") {
		t.Errorf("expected failed indicator in header, got: %s", header.Text.Text)
	}
}

func TestBuildWrapUpAgentCardBlocks_EmptyWrapUp(t *testing.T) {
	blocks := buildWrapUpAgentCardBlocks("test-bot", "done", `{}`)

	// Empty wrapup: header + action only (no wrapup section block).
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks for empty wrapup, got %d", len(blocks))
	}
}

func TestFormatWrapUpSlack_FullWrapUp(t *testing.T) {
	wrapupJSON := `{"accomplishments":"Closed 3 bugs","blockers":"API key pending","handoff_notes":"Check PR #42","beads_closed":["kd-1","kd-2"],"pull_requests":["https://github.com/org/repo/pull/42"]}`
	result := formatWrapUpSlack(wrapupJSON)

	if !strings.Contains(result, "*Accomplishments:* Closed 3 bugs") {
		t.Errorf("expected accomplishments in output, got: %s", result)
	}
	if !strings.Contains(result, "*Blockers:* API key pending") {
		t.Errorf("expected blockers in output, got: %s", result)
	}
	if !strings.Contains(result, "*Handoff:* Check PR #42") {
		t.Errorf("expected handoff notes in output, got: %s", result)
	}
	if !strings.Contains(result, "*Beads closed:* kd-1, kd-2") {
		t.Errorf("expected beads closed in output, got: %s", result)
	}
	if !strings.Contains(result, "*PRs:* https://github.com/org/repo/pull/42") {
		t.Errorf("expected pull requests in output, got: %s", result)
	}
}

func TestFormatWrapUpSlack_AccomplishmentsOnly(t *testing.T) {
	wrapupJSON := `{"accomplishments":"Fixed login flow"}`
	result := formatWrapUpSlack(wrapupJSON)

	if !strings.Contains(result, "*Accomplishments:* Fixed login flow") {
		t.Errorf("expected accomplishments in output, got: %s", result)
	}
	if strings.Contains(result, "Blockers") {
		t.Errorf("should not contain blockers when empty, got: %s", result)
	}
}

func TestFormatWrapUpSlack_InvalidJSON(t *testing.T) {
	result := formatWrapUpSlack("not valid json")
	if !strings.Contains(result, "not valid json") {
		t.Errorf("expected raw text fallback for invalid JSON, got: %s", result)
	}
}

func TestFormatWrapUpSlack_EmptyWrapUp(t *testing.T) {
	result := formatWrapUpSlack(`{}`)
	if result != "" {
		t.Errorf("expected empty string for empty wrapup, got: %q", result)
	}
}
