package bridge

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// mockJackNotifier records calls to jack notification methods.
type mockJackNotifier struct {
	mu       sync.Mutex
	raised   []BeadEvent
	batched  [][]BeadEvent
	lowered  []BeadEvent
	expired  []BeadEvent
}

func (m *mockJackNotifier) NotifyJackOn(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raised = append(m.raised, bead)
	return nil
}

func (m *mockJackNotifier) NotifyJackOnBatch(_ context.Context, beads []BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batched = append(m.batched, beads)
	return nil
}

func (m *mockJackNotifier) NotifyJackOff(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lowered = append(m.lowered, bead)
	return nil
}

func (m *mockJackNotifier) NotifyJackExpired(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expired = append(m.expired, bead)
	return nil
}

func (m *mockJackNotifier) getRaised() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.raised...)
}

func (m *mockJackNotifier) getLowered() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.lowered...)
}

func (m *mockJackNotifier) getExpired() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.expired...)
}

func TestJacks_HandleCreated_RaisesNotification(t *testing.T) {
	notif := &mockJackNotifier{}
	j := NewJacks(JacksConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Non-jack bead should be ignored.
	nonJack := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-1",
		Type: "decision",
	})
	j.handleCreated(context.Background(), nonJack)
	if len(notif.getRaised()) != 0 {
		t.Fatal("non-jack bead should not trigger notification")
	}

	// Jack bead should trigger notification.
	jack := marshalSSEBeadPayload(BeadEvent{
		ID:       "hq-abc123",
		Type:     "jack",
		Assignee: "gasboat/crew/ops",
		Fields: map[string]string{
			"target": "pod/bd-daemon-xyz",
			"ttl":    "30m",
			"reason": "Debug logging for NATS timeout",
		},
	})
	j.handleCreated(context.Background(), jack)

	raised := notif.getRaised()
	if len(raised) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(raised))
	}
	if raised[0].ID != "hq-abc123" {
		t.Errorf("expected jack ID hq-abc123, got %s", raised[0].ID)
	}
	if raised[0].Fields["target"] != "pod/bd-daemon-xyz" {
		t.Errorf("expected target pod/bd-daemon-xyz, got %s", raised[0].Fields["target"])
	}
}

func TestJacks_HandleClosed_LowersNotification(t *testing.T) {
	notif := &mockJackNotifier{}
	j := NewJacks(JacksConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	jack := marshalSSEBeadPayload(BeadEvent{
		ID:       "hq-def456",
		Type:     "jack",
		Assignee: "gasboat/crew/ops",
		Fields: map[string]string{
			"target": "deployment/api",
			"reason": "Fix verified, reverting",
		},
	})
	j.handleClosed(context.Background(), jack)

	lowered := notif.getLowered()
	if len(lowered) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(lowered))
	}
	if lowered[0].ID != "hq-def456" {
		t.Errorf("expected jack ID hq-def456, got %s", lowered[0].ID)
	}
}

func TestJacks_HandleClosed_Dedup(t *testing.T) {
	notif := &mockJackNotifier{}
	j := NewJacks(JacksConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	jack := marshalSSEBeadPayload(BeadEvent{
		ID:   "hq-dedup1",
		Type: "jack",
		Fields: map[string]string{
			"target": "pod/test",
		},
	})

	// First call: should notify.
	j.handleClosed(context.Background(), jack)
	// Second call: should be deduplicated.
	j.handleClosed(context.Background(), jack)

	lowered := notif.getLowered()
	if len(lowered) != 1 {
		t.Fatalf("expected exactly 1 notification (dedup), got %d", len(lowered))
	}
}

func TestJacks_HandleUpdated_Expired(t *testing.T) {
	notif := &mockJackNotifier{}
	j := NewJacks(JacksConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Jack without expired label should be ignored.
	normalUpdate := marshalSSEBeadPayload(BeadEvent{
		ID:   "hq-normal",
		Type: "jack",
		Fields: map[string]string{
			"target": "pod/test",
		},
	})
	j.handleUpdated(context.Background(), normalUpdate)
	if len(notif.getExpired()) != 0 {
		t.Fatal("jack without expired label should not trigger notification")
	}

	// Jack with expired label should trigger notification.
	expiredJack := marshalSSEBeadPayload(BeadEvent{
		ID:     "hq-expired1",
		Type:   "jack",
		Labels: []string{"expired"},
		Fields: map[string]string{
			"target": "pod/bd-daemon-abc",
			"reason": "Debug logging",
		},
	})
	j.handleUpdated(context.Background(), expiredJack)

	expired := notif.getExpired()
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired notification, got %d", len(expired))
	}
	if expired[0].ID != "hq-expired1" {
		t.Errorf("expected jack ID hq-expired1, got %s", expired[0].ID)
	}
}

func TestJacks_HandleUpdated_ExpiredDedup(t *testing.T) {
	notif := &mockJackNotifier{}
	j := NewJacks(JacksConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	expiredJack := marshalSSEBeadPayload(BeadEvent{
		ID:     "hq-expired2",
		Type:   "jack",
		Labels: []string{"expired"},
		Fields: map[string]string{
			"target": "pod/test",
		},
	})

	// First call: should notify.
	j.handleUpdated(context.Background(), expiredJack)
	// Second call within 6h: should be deduplicated.
	j.handleUpdated(context.Background(), expiredJack)

	expired := notif.getExpired()
	if len(expired) != 1 {
		t.Fatalf("expected exactly 1 expired notification (dedup), got %d", len(expired))
	}
}

func TestJacks_NilNotifier(t *testing.T) {
	j := NewJacks(JacksConfig{
		Notifier: nil,
		Logger:   slog.Default(),
	})

	jack := marshalSSEBeadPayload(BeadEvent{
		ID:   "hq-nil",
		Type: "jack",
		Fields: map[string]string{
			"target": "pod/test",
		},
	})

	// Should not panic with nil notifier.
	j.handleCreated(context.Background(), jack)
	j.handleClosed(context.Background(), jack)

	expiredJack := marshalSSEBeadPayload(BeadEvent{
		ID:     "hq-nil2",
		Type:   "jack",
		Labels: []string{"expired"},
		Fields: map[string]string{
			"target": "pod/test",
		},
	})
	j.handleUpdated(context.Background(), expiredJack)
}
