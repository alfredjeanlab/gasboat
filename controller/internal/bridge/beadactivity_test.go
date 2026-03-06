package bridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"log/slog"
)

// mockBeadActivityNotifier records which notification methods were called.
type mockBeadActivityNotifier struct {
	mu              sync.Mutex
	created         []BeadEvent
	claimed         []BeadEvent
	createdClaimed  []BeadEvent
	closed          []BeadEvent
}

func (m *mockBeadActivityNotifier) NotifyBeadCreated(_ context.Context, bead BeadEvent) {
	m.mu.Lock()
	m.created = append(m.created, bead)
	m.mu.Unlock()
}

func (m *mockBeadActivityNotifier) NotifyBeadClaimed(_ context.Context, bead BeadEvent) {
	m.mu.Lock()
	m.claimed = append(m.claimed, bead)
	m.mu.Unlock()
}

func (m *mockBeadActivityNotifier) NotifyBeadCreatedAndClaimed(_ context.Context, bead BeadEvent) {
	m.mu.Lock()
	m.createdClaimed = append(m.createdClaimed, bead)
	m.mu.Unlock()
}

func (m *mockBeadActivityNotifier) NotifyBeadClosed(_ context.Context, bead BeadEvent) {
	m.mu.Lock()
	m.closed = append(m.closed, bead)
	m.mu.Unlock()
}

func (m *mockBeadActivityNotifier) getCreated() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.created...)
}

func (m *mockBeadActivityNotifier) getClaimed() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.claimed...)
}

func (m *mockBeadActivityNotifier) getCreatedClaimed() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.createdClaimed...)
}

func (m *mockBeadActivityNotifier) getClosed() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.closed...)
}

func newTestBeadActivity(notifier *mockBeadActivityNotifier) *BeadActivity {
	return NewBeadActivity(BeadActivityConfig{
		Notifier: notifier,
		Logger:   slog.Default(),
	})
}

func TestBeadActivity_CreateThenClaim_MergedNotification(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	created := marshalSSEBeadPayload(BeadEvent{
		ID:        "kd-merge1",
		Type:      "task",
		Title:     "Test task",
		CreatedBy: "test-agent",
	})
	ba.handleCreated(context.Background(), created)

	// Claim within the window.
	claimed := marshalSSEBeadPayload(BeadEvent{
		ID:       "kd-merge1",
		Type:     "task",
		Title:    "Test task",
		Status:   "in_progress",
		Assignee: "test-agent",
	})
	ba.handleUpdated(context.Background(), claimed)

	// Wait for any pending timers to fire.
	time.Sleep(createClaimWindow + 500*time.Millisecond)

	if got := mock.getCreated(); len(got) != 0 {
		t.Errorf("expected 0 separate created notifications, got %d", len(got))
	}
	if got := mock.getClaimed(); len(got) != 0 {
		t.Errorf("expected 0 separate claimed notifications, got %d", len(got))
	}
	if got := mock.getCreatedClaimed(); len(got) != 1 {
		t.Fatalf("expected 1 merged created+claimed notification, got %d", len(got))
	}
	if got := mock.getCreatedClaimed()[0].ID; got != "kd-merge1" {
		t.Errorf("expected bead ID kd-merge1, got %s", got)
	}
}

func TestBeadActivity_CreateWithoutClaim_SeparateNotification(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	created := marshalSSEBeadPayload(BeadEvent{
		ID:        "kd-noclaim",
		Type:      "task",
		Title:     "Unclaimed task",
		CreatedBy: "test-agent",
	})
	ba.handleCreated(context.Background(), created)

	// Wait for the create window to expire without claiming.
	time.Sleep(createClaimWindow + 500*time.Millisecond)

	if got := mock.getCreated(); len(got) != 1 {
		t.Fatalf("expected 1 created notification after window expired, got %d", len(got))
	}
	if got := mock.getCreatedClaimed(); len(got) != 0 {
		t.Errorf("expected 0 merged notifications, got %d", len(got))
	}
}

func TestBeadActivity_ClaimWithoutCreate_SeparateNotification(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	// Claim event with no preceding create (agent claims someone else's bead).
	claimed := marshalSSEBeadPayload(BeadEvent{
		ID:       "kd-claimonly",
		Type:     "bug",
		Title:    "Existing bug",
		Status:   "in_progress",
		Assignee: "test-agent",
	})
	ba.handleUpdated(context.Background(), claimed)

	if got := mock.getClaimed(); len(got) != 1 {
		t.Fatalf("expected 1 claimed notification, got %d", len(got))
	}
	if got := mock.getCreatedClaimed(); len(got) != 0 {
		t.Errorf("expected 0 merged notifications, got %d", len(got))
	}
}

func TestBeadActivity_NonWorkItemType_Ignored(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	for _, typ := range []string{"agent", "decision", "mail", "project", "config"} {
		data := marshalSSEBeadPayload(BeadEvent{
			ID:        "kd-infra",
			Type:      typ,
			CreatedBy: "test-agent",
		})
		ba.handleCreated(context.Background(), data)
	}

	time.Sleep(createClaimWindow + 500*time.Millisecond)

	if got := mock.getCreated(); len(got) != 0 {
		t.Errorf("expected 0 notifications for non-work-item types, got %d", len(got))
	}
}

func TestBeadActivity_ClosedBead_Notifies(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	data := marshalSSEBeadPayload(BeadEvent{
		ID:       "kd-closed1",
		Type:     "task",
		Title:    "Done task",
		Assignee: "test-agent",
	})
	ba.handleClosed(context.Background(), data)

	if got := mock.getClosed(); len(got) != 1 {
		t.Fatalf("expected 1 closed notification, got %d", len(got))
	}
}

func TestBeadActivity_ClosedBead_NoAssignee_Ignored(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	data := marshalSSEBeadPayload(BeadEvent{
		ID:   "kd-closed-noassignee",
		Type: "task",
	})
	ba.handleClosed(context.Background(), data)

	if got := mock.getClosed(); len(got) != 0 {
		t.Errorf("expected 0 closed notifications for unassigned bead, got %d", len(got))
	}
}

func TestBeadActivity_DuplicateCreate_Deduped(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	data := marshalSSEBeadPayload(BeadEvent{
		ID:        "kd-dup",
		Type:      "feature",
		Title:     "Duplicate test",
		CreatedBy: "test-agent",
	})

	// Fire twice (simulates SSE reconnect replay).
	ba.handleCreated(context.Background(), data)
	ba.handleCreated(context.Background(), data)

	time.Sleep(createClaimWindow + 500*time.Millisecond)

	if got := mock.getCreated(); len(got) != 1 {
		t.Errorf("expected 1 created notification (deduped), got %d", len(got))
	}
}

func TestBeadActivity_MalformedPayload_NoPanic(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	ba.handleCreated(context.Background(), []byte("not-json"))
	ba.handleUpdated(context.Background(), []byte("not-json"))
	ba.handleClosed(context.Background(), []byte("not-json"))
	// No panic = pass.
}

func TestBeadActivity_UpdateNonClaim_Ignored(t *testing.T) {
	mock := &mockBeadActivityNotifier{}
	ba := newTestBeadActivity(mock)

	// Update event that is NOT a claim (status != in_progress).
	data := marshalSSEBeadPayload(BeadEvent{
		ID:       "kd-nonclaim",
		Type:     "task",
		Title:    "Updated task",
		Status:   "open",
		Assignee: "test-agent",
	})
	ba.handleUpdated(context.Background(), data)

	if got := mock.getClaimed(); len(got) != 0 {
		t.Errorf("expected 0 claimed notifications for non-claim update, got %d", len(got))
	}
}
