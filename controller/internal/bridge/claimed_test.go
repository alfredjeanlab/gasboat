package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"
	"log/slog"
)

func TestClaimed_HandleUpdated_NonActionable_Ignored(t *testing.T) {
	c := &Claimed{
		logger: slog.Default(),
		nudged: make(map[string]time.Time),
	}

	for _, typ := range []string{"agent", "decision", "mail", "project", "report"} {
		data := marshalSSEBeadPayload(BeadEvent{
			ID:       "bd-1",
			Type:     typ,
			Assignee: "some-agent",
		})
		c.handleUpdated(context.Background(), data)
	}
	// No panic = pass; daemon was not called.
}

func TestClaimed_HandleUpdated_NoAssignee_Ignored(t *testing.T) {
	daemon := newMockDaemon()
	c := &Claimed{
		daemon: daemon,
		logger: slog.Default(),
		nudged: make(map[string]time.Time),
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:   "bd-2",
		Type: "task",
		// No assignee.
	})
	c.handleUpdated(context.Background(), data)

	if daemon.getGetCalls() != 0 {
		t.Fatalf("expected no daemon calls for unassigned bead, got %d", daemon.getGetCalls())
	}
}

func TestClaimed_HandleUpdated_ClaimedTask_Nudges(t *testing.T) {
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
	daemon.beads["my-agent"] = &beadsapi.BeadDetail{
		ID:    "my-agent",
		Notes: "coop_url: " + coopServer.URL,
	}

	c := &Claimed{
		daemon:     daemon,
		logger:     slog.Default(),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		nudged:     make(map[string]time.Time),
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:       "kd-abc",
		Type:     "task",
		Title:    "Fix the bug",
		Assignee: "my-agent",
	})
	c.handleUpdated(context.Background(), data)

	time.Sleep(50 * time.Millisecond)
	nudgeMu.Lock()
	msg := nudgeMessage
	nudgeMu.Unlock()

	if msg == "" {
		t.Fatal("expected coop nudge for claimed task update, got none")
	}
	want := `Your claimed bead kd-abc "Fix the bug" was updated — run 'kd show kd-abc' to review`
	if msg != want {
		t.Errorf("unexpected nudge message:\n  got:  %s\n  want: %s", msg, want)
	}
}

func TestClaimed_HandleUpdated_RateLimit_SecondNudgeSuppressed(t *testing.T) {
	nudgeCount := 0
	var mu sync.Mutex

	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/nudge" {
			mu.Lock()
			nudgeCount++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer coopServer.Close()

	daemon := newMockDaemon()
	daemon.beads["my-agent"] = &beadsapi.BeadDetail{
		ID:    "my-agent",
		Notes: "coop_url: " + coopServer.URL,
	}

	c := &Claimed{
		daemon:     daemon,
		logger:     slog.Default(),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		nudged:     make(map[string]time.Time),
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:       "kd-ratelimit",
		Type:     "task",
		Title:    "Rate-limited bead",
		Assignee: "my-agent",
	})

	// First update: nudge fires.
	c.handleUpdated(context.Background(), data)
	time.Sleep(50 * time.Millisecond)

	// Second update within TTL: nudge should be suppressed.
	c.handleUpdated(context.Background(), data)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := nudgeCount
	mu.Unlock()

	if count != 1 {
		t.Errorf("expected 1 nudge (rate limited), got %d", count)
	}
}

func TestClaimed_HandleUpdated_DifferentBeads_BothNudge(t *testing.T) {
	nudgeCount := 0
	var mu sync.Mutex

	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/nudge" {
			mu.Lock()
			nudgeCount++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer coopServer.Close()

	daemon := newMockDaemon()
	daemon.beads["my-agent"] = &beadsapi.BeadDetail{
		ID:    "my-agent",
		Notes: "coop_url: " + coopServer.URL,
	}

	c := &Claimed{
		daemon:     daemon,
		logger:     slog.Default(),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		nudged:     make(map[string]time.Time),
	}

	// Two different beads — each should get its own nudge.
	for _, id := range []string{"kd-bead-1", "kd-bead-2"} {
		data := marshalSSEBeadPayload(BeadEvent{
			ID:       id,
			Type:     "feature",
			Title:    "Bead " + id,
			Assignee: "my-agent",
		})
		c.handleUpdated(context.Background(), data)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := nudgeCount
	mu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 nudges for 2 different beads, got %d", count)
	}
}

func TestClaimed_HandleUpdated_MalformedPayload_NoAction(t *testing.T) {
	c := &Claimed{
		logger: slog.Default(),
		nudged: make(map[string]time.Time),
	}
	c.handleUpdated(context.Background(), []byte("not-json"))
	// No panic = pass.
}

func TestClaimed_HandleClosed_NonActionable_Ignored(t *testing.T) {
	c := &Claimed{
		logger: slog.Default(),
		nudged: make(map[string]time.Time),
	}

	for _, typ := range []string{"agent", "decision", "mail", "project", "report"} {
		data := marshalSSEBeadPayload(BeadEvent{
			ID:       "bd-1",
			Type:     typ,
			Assignee: "some-agent",
		})
		c.handleClosed(context.Background(), data)
	}
	// No panic = pass; daemon was not called.
}

func TestClaimed_HandleClosed_NoAssignee_Ignored(t *testing.T) {
	daemon := newMockDaemon()
	c := &Claimed{
		daemon: daemon,
		logger: slog.Default(),
		nudged: make(map[string]time.Time),
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:   "bd-closed-no-assignee",
		Type: "task",
		// No assignee.
	})
	c.handleClosed(context.Background(), data)

	if daemon.getGetCalls() != 0 {
		t.Fatalf("expected no daemon calls for unassigned closed bead, got %d", daemon.getGetCalls())
	}
}

func TestClaimed_HandleClosed_ClaimedTask_NudgesCheckpoint(t *testing.T) {
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
	daemon.beads["my-agent"] = &beadsapi.BeadDetail{
		ID:    "my-agent",
		Notes: "coop_url: " + coopServer.URL,
	}

	c := &Claimed{
		daemon:     daemon,
		logger:     slog.Default(),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		nudged:     make(map[string]time.Time),
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:       "kd-xyz",
		Type:     "task",
		Title:    "Finished work",
		Assignee: "my-agent",
	})
	c.handleClosed(context.Background(), data)

	time.Sleep(50 * time.Millisecond)
	nudgeMu.Lock()
	msg := nudgeMessage
	nudgeMu.Unlock()

	if msg == "" {
		t.Fatal("expected coop nudge for claimed task closure, got none")
	}
	want := `Your claimed bead kd-xyz "Finished work" was closed — work bead closed, create a decision checkpoint now`
	if msg != want {
		t.Errorf("unexpected nudge message:\n  got:  %s\n  want: %s", msg, want)
	}
}

func TestClaimed_HandleClosed_MalformedPayload_NoAction(t *testing.T) {
	c := &Claimed{
		logger: slog.Default(),
		nudged: make(map[string]time.Time),
	}
	c.handleClosed(context.Background(), []byte("not-json"))
	// No panic = pass.
}
