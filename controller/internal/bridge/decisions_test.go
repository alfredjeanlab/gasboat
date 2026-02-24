package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"log/slog"

	"gasboat/controller/internal/beadsapi"
)

// mockDaemon implements BeadClient for testing.
type mockDaemon struct {
	mu       sync.Mutex
	beads    map[string]*client.BeadDetail
	closed   []closeCall
	getCalls int
}

type closeCall struct {
	BeadID string
	Fields map[string]string
}

func newMockDaemon() *mockDaemon {
	return &mockDaemon{
		beads: make(map[string]*client.BeadDetail),
	}
}

func (m *mockDaemon) GetBead(_ context.Context, beadID string) (*client.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if b, ok := m.beads[beadID]; ok {
		return b, nil
	}
	return &client.BeadDetail{ID: beadID}, nil
}

func (m *mockDaemon) FindAgentBead(_ context.Context, agentName string) (*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if b, ok := m.beads[agentName]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("agent bead not found for agent %q", agentName)
}

func (m *mockDaemon) CloseBead(_ context.Context, beadID string, fields map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, closeCall{BeadID: beadID, Fields: fields})
	return nil
}

func (m *mockDaemon) ListDecisionBeads(_ context.Context) ([]*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*beadsapi.BeadDetail
	for _, b := range m.beads {
		if b.Type == "decision" {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockDaemon) ListAgentBeads(_ context.Context) ([]beadsapi.AgentBead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []beadsapi.AgentBead
	for _, b := range m.beads {
		if b.Type == "agent" {
			result = append(result, beadsapi.AgentBead{
				ID:      b.ID,
				Project: b.Fields["project"],
				Mode:    "crew",
				Role:    b.Fields["role"],
			})
		}
	}
	return result, nil
}

func (m *mockDaemon) getClosed() []closeCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]closeCall{}, m.closed...)
}

func (m *mockDaemon) getGetCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getCalls
}

// mockNotifier records calls to NotifyDecision, UpdateDecision, NotifyEscalation, and DismissDecision.
type mockNotifier struct {
	mu        sync.Mutex
	created   []BeadEvent
	updated   []updateCall
	escalated []BeadEvent
	dismissed []string
}

type updateCall struct {
	BeadID string
	Chosen string
}

func (m *mockNotifier) NotifyDecision(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, bead)
	return nil
}

func (m *mockNotifier) UpdateDecision(_ context.Context, beadID, chosen string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updated = append(m.updated, updateCall{beadID, chosen})
	return nil
}

func (m *mockNotifier) NotifyEscalation(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.escalated = append(m.escalated, bead)
	return nil
}

func (m *mockNotifier) DismissDecision(_ context.Context, beadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dismissed = append(m.dismissed, beadID)
	return nil
}

func (m *mockNotifier) getCreated() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.created...)
}

func (m *mockNotifier) getUpdated() []updateCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]updateCall{}, m.updated...)
}

func (m *mockNotifier) getEscalated() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.escalated...)
}

func (m *mockNotifier) getDismissed() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.dismissed...)
}

// marshalSSEBeadPayload wraps a BeadEvent in the kbeads SSE event format
// ({"bead": {...}}) for testing handleCreated/handleClosed which now accept
// raw SSE JSON data.
func marshalSSEBeadPayload(bead BeadEvent) []byte {
	data, _ := json.Marshal(map[string]any{"bead": bead})
	return data
}

func TestDecisions_HandleCreated(t *testing.T) {
	d := &Decisions{
		notifier:  &mockNotifier{},
		logger:    slog.Default(),
		escalated: make(map[string]time.Time),
	}

	// Non-decision bead should be ignored.
	nonDecision := marshalSSEBeadPayload(BeadEvent{
		ID:   "abc",
		Type: "agent",
	})
	d.handleCreated(context.Background(), nonDecision)

	mn := d.notifier.(*mockNotifier)
	if len(mn.getCreated()) != 0 {
		t.Fatal("non-decision bead should not trigger notification")
	}

	// Decision bead should trigger notification.
	decision := marshalSSEBeadPayload(BeadEvent{
		ID:       "dec-1",
		Type:     "decision",
		Title:    "Pick a color",
		Assignee: "crew-town-crew-hq",
		Fields: map[string]string{
			"question": "What color?",
			"options":  `["red","blue"]`,
		},
	})
	d.handleCreated(context.Background(), decision)

	created := mn.getCreated()
	if len(created) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(created))
	}
	if created[0].ID != "dec-1" {
		t.Errorf("expected bead ID dec-1, got %s", created[0].ID)
	}
	if created[0].Assignee != "crew-town-crew-hq" {
		t.Errorf("expected assignee crew-town-crew-hq, got %s", created[0].Assignee)
	}
}

func TestDecisions_HandleClosed_NudgesCoop(t *testing.T) {
	// Set up a fake coop server that records nudge calls.
	var nudgeReceived sync.Mutex
	var nudgeMessage string
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/nudge" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			nudgeReceived.Lock()
			nudgeMessage = body["message"]
			nudgeReceived.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer coopServer.Close()

	// Set up a mock daemon that returns the agent bead with coop_url in Notes.
	daemon := newMockDaemon()
	daemon.beads["crew-town-crew-hq"] = &beadsapi.BeadDetail{
		ID:    "crew-town-crew-hq",
		Notes: "coop_url: " + coopServer.URL,
	}
	notif := &mockNotifier{}

	d := &Decisions{
		daemon:     daemon,
		notifier:   notif,
		logger:     slog.Default(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		escalated:  make(map[string]time.Time),
	}

	closedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:       "dec-1",
		Type:     "decision",
		Assignee: "crew-town-crew-hq",
		Fields: map[string]string{
			"chosen":    "blue",
			"rationale": "it's calming",
		},
	})
	d.handleClosed(context.Background(), closedEvent)

	// Verify nudge was sent to coop.
	time.Sleep(50 * time.Millisecond) // give async processing a moment
	nudgeReceived.Lock()
	msg := nudgeMessage
	nudgeReceived.Unlock()

	if msg == "" {
		t.Fatal("expected coop nudge, got none")
	}
	if msg != "Decision resolved: blue \u2014 it's calming" {
		t.Errorf("unexpected nudge message: %s", msg)
	}

	// Verify notifier.UpdateDecision was called.
	updated := notif.getUpdated()
	if len(updated) != 1 {
		t.Fatalf("expected 1 update call, got %d", len(updated))
	}
	if updated[0].BeadID != "dec-1" || updated[0].Chosen != "blue" {
		t.Errorf("unexpected update call: %+v", updated[0])
	}
}

func TestDecisions_HandleClosed_NoAssignee(t *testing.T) {
	d := &Decisions{
		notifier:  &mockNotifier{},
		logger:    slog.Default(),
		escalated: make(map[string]time.Time),
	}

	// Decision closed without assignee — should log warning but not panic.
	closedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-2",
		Type: "decision",
		Fields: map[string]string{
			"chosen": "yes",
		},
	})
	d.handleClosed(context.Background(), closedEvent)
	// No panic = pass.
}

func TestDecisions_HandleClosed_Expiry(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Decision closed with chosen=_expired should dismiss the Slack message.
	expiredEvent := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-3",
		Type: "decision",
		Fields: map[string]string{
			"chosen":    "_expired",
			"rationale": "Decision expired after 30m with no response",
		},
	})
	d.handleClosed(context.Background(), expiredEvent)

	dismissed := notif.getDismissed()
	if len(dismissed) != 1 {
		t.Fatalf("expected 1 dismiss call, got %d", len(dismissed))
	}
	if dismissed[0] != "dec-3" {
		t.Errorf("expected dismissed bead dec-3, got %s", dismissed[0])
	}

	// UpdateDecision should NOT be called for expired decisions.
	if len(notif.getUpdated()) != 0 {
		t.Error("UpdateDecision should not be called for expired decisions")
	}
}

func TestDecisions_HandleClosed_Dismissed(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Decision closed with chosen=dismissed should dismiss the Slack message.
	dismissedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-4",
		Type: "decision",
		Fields: map[string]string{
			"chosen": "dismissed",
		},
	})
	d.handleClosed(context.Background(), dismissedEvent)

	dismissed := notif.getDismissed()
	if len(dismissed) != 1 {
		t.Fatalf("expected 1 dismiss call, got %d", len(dismissed))
	}
}

func TestDecisions_HandleUpdated_Escalation(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Non-decision bead should be ignored.
	nonDecision := marshalSSEBeadPayload(BeadEvent{
		ID:     "agent-1",
		Type:   "agent",
		Labels: []string{"escalated"},
	})
	d.handleUpdated(context.Background(), nonDecision)
	if len(notif.getEscalated()) != 0 {
		t.Fatal("non-decision bead should not trigger escalation")
	}

	// Decision without escalation marker should be ignored.
	normalUpdate := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-5",
		Type: "decision",
		Fields: map[string]string{
			"question": "Deploy to prod?",
		},
	})
	d.handleUpdated(context.Background(), normalUpdate)
	if len(notif.getEscalated()) != 0 {
		t.Fatal("decision without escalation should not trigger notification")
	}

	// Decision with "escalated" label should trigger notification.
	escalatedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:       "dec-6",
		Type:     "decision",
		Labels:   []string{"escalated"},
		Assignee: "gasboat/crew/test-bot",
		Fields: map[string]string{
			"question": "Deploy to prod?",
		},
	})
	d.handleUpdated(context.Background(), escalatedEvent)

	escalated := notif.getEscalated()
	if len(escalated) != 1 {
		t.Fatalf("expected 1 escalation, got %d", len(escalated))
	}
	if escalated[0].ID != "dec-6" {
		t.Errorf("expected bead ID dec-6, got %s", escalated[0].ID)
	}
}

func TestDecisions_HandleUpdated_EscalationByLabel(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Decision with "escalated" label should trigger escalation.
	criticalEvent := marshalSSEBeadPayload(BeadEvent{
		ID:     "dec-7",
		Type:   "decision",
		Labels: []string{"escalated", "urgent"},
		Fields: map[string]string{
			"question": "System down — deploy hotfix?",
		},
	})
	d.handleUpdated(context.Background(), criticalEvent)

	escalated := notif.getEscalated()
	if len(escalated) != 1 {
		t.Fatalf("expected 1 escalation, got %d", len(escalated))
	}
}

func TestDecisions_HandleUpdated_EscalationDedup(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	escalatedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:     "dec-8",
		Type:   "decision",
		Labels: []string{"escalated"},
		Fields: map[string]string{
			"question": "Approve?",
		},
	})

	// First call: should notify.
	d.handleUpdated(context.Background(), escalatedEvent)
	// Second call: should be deduplicated.
	d.handleUpdated(context.Background(), escalatedEvent)

	escalated := notif.getEscalated()
	if len(escalated) != 1 {
		t.Fatalf("expected exactly 1 escalation (dedup), got %d", len(escalated))
	}
}

func TestMockDaemon_ListDecisionBeads(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["dec-10"] = &beadsapi.BeadDetail{
		ID:   "dec-10",
		Type: "decision",
		Fields: map[string]string{
			"question": "Deploy?",
		},
	}
	daemon.beads["agent-1"] = &beadsapi.BeadDetail{
		ID:   "agent-1",
		Type: "agent",
	}
	daemon.beads["dec-11"] = &beadsapi.BeadDetail{
		ID:       "dec-11",
		Type:     "decision",
		Assignee: "test-bot",
		Labels:   []string{"escalated"},
		Fields: map[string]string{
			"question": "Rollback?",
		},
	}

	decisions, err := daemon.ListDecisionBeads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}

	// Verify only decision beads returned.
	ids := map[string]bool{}
	for _, d := range decisions {
		ids[d.ID] = true
		if d.Type != "decision" {
			t.Errorf("expected type=decision, got %s", d.Type)
		}
	}
	if !ids["dec-10"] || !ids["dec-11"] {
		t.Errorf("expected dec-10 and dec-11, got %v", ids)
	}
}

func TestMockDaemon_ListAgentBeads(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["agent-a"] = &beadsapi.BeadDetail{
		ID:   "agent-a",
		Type: "agent",
		Fields: map[string]string{
			"role":    "crew",
			"project": "gasboat",
		},
	}
	daemon.beads["dec-1"] = &beadsapi.BeadDetail{
		ID:   "dec-1",
		Type: "decision",
	}

	agents, err := daemon.ListAgentBeads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].ID != "agent-a" {
		t.Errorf("expected agent-a, got %s", agents[0].ID)
	}
	if agents[0].Project != "gasboat" {
		t.Errorf("expected project=gasboat, got %s", agents[0].Project)
	}
}

func TestMockDaemon_ListDecisionBeads_Empty(t *testing.T) {
	daemon := newMockDaemon()

	decisions, err := daemon.ListDecisionBeads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("expected 0 decisions, got %d", len(decisions))
	}
}
