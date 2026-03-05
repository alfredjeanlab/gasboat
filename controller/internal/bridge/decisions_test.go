package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"log/slog"

	"gasboat/controller/internal/beadsapi"
)

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
	if updated[0].BeadID != "dec-1" || updated[0].Chosen != "blue" || updated[0].Rationale != "it's calming" {
		t.Errorf("unexpected update call: %+v", updated[0])
	}
}

// TestDecisions_HandleClosed_RationaleFromFetch verifies that when the SSE close
// event omits rationale (common in practice), it is fetched from the full bead
// and included in the nudge message sent to the LLM.
func TestDecisions_HandleClosed_RationaleFromFetch(t *testing.T) {
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

	daemon := newMockDaemon()
	daemon.beads["agent-x"] = &beadsapi.BeadDetail{
		ID:    "agent-x",
		Notes: "coop_url: " + coopServer.URL,
	}
	// Full bead has both chosen and rationale; SSE event only has chosen.
	daemon.beads["dec-rat"] = &beadsapi.BeadDetail{
		ID: "dec-rat",
		Fields: map[string]string{
			"chosen":    "proceed",
			"rationale": "looks good to me",
		},
	}

	d := &Decisions{
		daemon:     daemon,
		notifier:   &mockNotifier{},
		logger:     slog.Default(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		escalated:  make(map[string]time.Time),
	}

	// SSE event carries neither chosen nor rationale — both must come from the fetch.
	closedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:       "dec-rat",
		Type:     "decision",
		Assignee: "agent-x",
		Fields:   map[string]string{},
	})
	d.handleClosed(context.Background(), closedEvent)

	time.Sleep(50 * time.Millisecond)
	nudgeReceived.Lock()
	msg := nudgeMessage
	nudgeReceived.Unlock()

	want := "Decision resolved: proceed \u2014 looks good to me"
	if msg != want {
		t.Errorf("nudge message = %q, want %q", msg, want)
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

func TestDecisionQuestion(t *testing.T) {
	// "prompt" field is preferred over "question".
	if got := decisionQuestion(map[string]string{"prompt": "Deploy?"}); got != "Deploy?" {
		t.Errorf("expected prompt value, got %q", got)
	}
	// Fallback to legacy "question" field.
	if got := decisionQuestion(map[string]string{"question": "Rollback?"}); got != "Rollback?" {
		t.Errorf("expected question value, got %q", got)
	}
	// "prompt" takes precedence when both are set.
	if got := decisionQuestion(map[string]string{"prompt": "A", "question": "B"}); got != "A" {
		t.Errorf("expected prompt to take precedence, got %q", got)
	}
	// Empty fields returns empty string.
	if got := decisionQuestion(map[string]string{}); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestDecisions_HandleReportClosed_PostsReport(t *testing.T) {
	daemon := newMockDaemon()
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Daemon:   daemon,
		Notifier: notif,
		Logger:   slog.Default(),
	})

	closedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:   "report-1",
		Type: "report",
		Fields: map[string]string{
			"decision_id": "dec-42",
			"report_type": "plan",
			"content":     "Here is the implementation plan.",
		},
	})
	d.handleReportClosed(context.Background(), closedEvent)

	reports := notif.getReports()
	if len(reports) != 1 {
		t.Fatalf("expected 1 PostReport call, got %d", len(reports))
	}
	if reports[0].DecisionID != "dec-42" {
		t.Errorf("expected decision_id=dec-42, got %s", reports[0].DecisionID)
	}
	if reports[0].ReportType != "plan" {
		t.Errorf("expected report_type=plan, got %s", reports[0].ReportType)
	}
	if reports[0].Content != "Here is the implementation plan." {
		t.Errorf("expected content mismatch, got %q", reports[0].Content)
	}
}

func TestDecisions_HandleReportClosed_FetchesMissingFields(t *testing.T) {
	daemon := newMockDaemon()
	// Full bead in daemon has the content; SSE event does not.
	daemon.beads["report-2"] = &beadsapi.BeadDetail{
		ID:   "report-2",
		Type: "report",
		Fields: map[string]string{
			"decision_id": "dec-99",
			"report_type": "checklist",
			"content":     "Fetched from daemon.",
		},
	}
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Daemon:   daemon,
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// SSE event has no content or decision_id — triggers GetBead fallback.
	closedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:     "report-2",
		Type:   "report",
		Fields: map[string]string{},
	})
	d.handleReportClosed(context.Background(), closedEvent)

	reports := notif.getReports()
	if len(reports) != 1 {
		t.Fatalf("expected 1 PostReport call, got %d", len(reports))
	}
	if reports[0].DecisionID != "dec-99" {
		t.Errorf("expected decision_id=dec-99, got %s", reports[0].DecisionID)
	}
	if reports[0].Content != "Fetched from daemon." {
		t.Errorf("expected fetched content, got %q", reports[0].Content)
	}
}

func TestDecisions_HandleReportClosed_IgnoresNonReport(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Decision bead close should be ignored by handleReportClosed.
	closedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-1",
		Type: "decision",
		Fields: map[string]string{
			"chosen": "yes",
		},
	})
	d.handleReportClosed(context.Background(), closedEvent)

	if len(notif.getReports()) != 0 {
		t.Fatal("non-report bead should not trigger PostReport")
	}
}

func TestDecisions_HandleReportClosed_NoDecisionID(t *testing.T) {
	notif := &mockNotifier{}
	d := NewDecisions(DecisionsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Report with no decision_id should be skipped.
	closedEvent := marshalSSEBeadPayload(BeadEvent{
		ID:   "report-3",
		Type: "report",
		Fields: map[string]string{
			"content": "orphan report",
		},
	})
	d.handleReportClosed(context.Background(), closedEvent)

	if len(notif.getReports()) != 0 {
		t.Fatal("report without decision_id should not trigger PostReport")
	}
}

func TestDecisions_HandleCreated_ContextFieldPassedThrough(t *testing.T) {
	notif := &mockNotifier{}
	d := &Decisions{
		notifier:  notif,
		logger:    slog.Default(),
		escalated: make(map[string]time.Time),
	}

	// Decision bead with context field should pass it through to the notifier.
	decision := marshalSSEBeadPayload(BeadEvent{
		ID:       "dec-ctx-1",
		Type:     "decision",
		Title:    "Choose deployment strategy",
		Assignee: "crew-test",
		Fields: map[string]string{
			"prompt":  "Which deployment strategy?",
			"options": `["blue-green","canary"]`,
			"context": "We need to deploy the auth service update. Current system handles 10k RPM.",
		},
	})
	d.handleCreated(context.Background(), decision)

	created := notif.getCreated()
	if len(created) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(created))
	}
	if created[0].Fields["context"] != "We need to deploy the auth service update. Current system handles 10k RPM." {
		t.Errorf("expected context field to be passed through, got %q", created[0].Fields["context"])
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

