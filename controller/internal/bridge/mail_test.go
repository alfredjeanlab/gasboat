package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"
)

func TestMail_HandleCreated_NonMail_Ignored(t *testing.T) {
	m := &Mail{
		logger: slog.Default(),
	}

	// Non-mail bead should be ignored (no panic, no action).
	nonMail := marshalSSEBeadPayload(BeadEvent{
		ID:   "abc",
		Type: "agent",
	})
	m.handleCreated(context.Background(), nonMail)
	// No panic = pass.
}

func TestMail_HandleCreated_InterruptLabel_Nudges(t *testing.T) {
	var nudgeMu sync.Mutex
	var nudgeMessage string
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/nudge" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			nudgeMu.Lock()
			nudgeMessage = body["message"]
			nudgeMu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer coopServer.Close()

	daemon := newMockDaemon()
	daemon.beads["crew-proj-devops-builder"] = &beadsapi.BeadDetail{
		ID:    "crew-proj-devops-builder",
		Notes: "coop_url: " + coopServer.URL,
	}

	m := &Mail{
		daemon:     daemon,
		logger:     slog.Default(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	event := marshalSSEBeadPayload(BeadEvent{
		ID:       "mail-1",
		Type:     "mail",
		Title:    "Task complete",
		Assignee: "crew-proj-devops-builder",
		Labels:   []string{"from:myproject/reviewer", "delivery:interrupt"},
		Priority: 2, // Normal priority, but delivery:interrupt overrides.
	})
	m.handleCreated(context.Background(), event)

	time.Sleep(50 * time.Millisecond)
	nudgeMu.Lock()
	msg := nudgeMessage
	nudgeMu.Unlock()

	if msg == "" {
		t.Fatal("expected coop nudge for delivery:interrupt mail, got none")
	}
	if msg != "New mail from myproject/reviewer: Task complete \u2014 run 'kd show mail-1' to read" {
		t.Errorf("unexpected nudge message: %s", msg)
	}
}

func TestMail_HandleCreated_HighPriority_Nudges(t *testing.T) {
	var nudgeMu sync.Mutex
	var nudgeMessage string
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/nudge" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			nudgeMu.Lock()
			nudgeMessage = body["message"]
			nudgeMu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer coopServer.Close()

	daemon := newMockDaemon()
	daemon.beads["crew-proj-devops-builder"] = &beadsapi.BeadDetail{
		ID:    "crew-proj-devops-builder",
		Notes: "coop_url: " + coopServer.URL,
	}

	m := &Mail{
		daemon:     daemon,
		logger:     slog.Default(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	// Priority 1 (high) should nudge even without interrupt label.
	event := marshalSSEBeadPayload(BeadEvent{
		ID:       "mail-2",
		Type:     "mail",
		Title:    "Urgent task",
		Assignee: "crew-proj-devops-builder",
		Labels:   []string{"from:myproject/lead", "delivery:queue"},
		Priority: 1,
	})
	m.handleCreated(context.Background(), event)

	time.Sleep(50 * time.Millisecond)
	nudgeMu.Lock()
	msg := nudgeMessage
	nudgeMu.Unlock()

	if msg == "" {
		t.Fatal("expected coop nudge for high-priority mail, got none")
	}
}

func TestMail_HandleCreated_QueueDelivery_NormalPriority_NoNudge(t *testing.T) {
	// Daemon should NOT be called for queue delivery + normal priority.
	daemon := newMockDaemon()

	m := &Mail{
		daemon: daemon,
		logger: slog.Default(),
	}

	event := marshalSSEBeadPayload(BeadEvent{
		ID:       "mail-3",
		Type:     "mail",
		Title:    "FYI update",
		Assignee: "crew-proj-devops-builder",
		Labels:   []string{"from:myproject/ops", "delivery:queue"},
		Priority: 2, // Normal priority.
	})
	m.handleCreated(context.Background(), event)

	if daemon.getGetCalls() != 0 {
		t.Fatalf("expected no daemon GetBead calls for queue+normal priority, got %d", daemon.getGetCalls())
	}
}

func TestMail_HandleCreated_NoAssignee_NoNudge(t *testing.T) {
	// Missing assignee should log warning but not panic.
	m := &Mail{
		logger: slog.Default(),
	}

	event := marshalSSEBeadPayload(BeadEvent{
		ID:       "mail-4",
		Type:     "mail",
		Title:    "Orphaned mail",
		Labels:   []string{"from:someone", "delivery:interrupt"},
		Priority: 0, // Critical but no assignee.
	})
	m.handleCreated(context.Background(), event)
	// No panic = pass.
}
