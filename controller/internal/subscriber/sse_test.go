package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// makeAgentPayload builds a JSON SSE payload for an agent bead with the given ID.
func makeAgentPayload(beadID string) []byte {
	payload := map[string]any{
		"bead": map[string]any{
			"id":     beadID,
			"type":   "agent",
			"status": "in_progress",
			"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
		},
	}
	data, _ := json.Marshal(payload)
	return data
}

// TestSSEWatcher_ParsesAgentCreatedEvent verifies that an SSE "beads.bead.created"
// event for an agent bead is correctly translated to an AgentSpawn lifecycle event.
func TestSSEWatcher_ParsesAgentCreatedEvent(t *testing.T) {
	// Start an SSE server that sends one agent created event then closes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events/stream" {
			http.NotFound(w, r)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send an agent bead created event.
		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-abc123",
				"title":  "test agent",
				"type":   "agent",
				"status": "in_progress",
				"labels": []string{"gt:agent"},
				"fields": map[string]string{
					"project": "kbeads",
					"role":    "devops",
					"agent":   "worker-1",
					"mode":    "crew",
				},
				"assignee":   "human",
				"created_by": "human",
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:1\n")
		fmt.Fprintf(w, "event:beads.bead.created\n")
		fmt.Fprintf(w, "data:%s\n\n", data)
		flusher.Flush()

		// Give the watcher time to read, then close.
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Topics:        "beads.bead.*",
		Namespace:     "test-ns",
		CoopImage:     "test-image:latest",
		BeadsGRPCAddr: "localhost:9090",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		if event.Type != AgentSpawn {
			t.Fatalf("expected AgentSpawn, got %s", event.Type)
		}
		if event.Project != "kbeads" {
			t.Fatalf("expected project kbeads, got %s", event.Project)
		}
		if event.Role != "devops" {
			t.Fatalf("expected role devops, got %s", event.Role)
		}
		if event.AgentName != "worker-1" {
			t.Fatalf("expected agent worker-1, got %s", event.AgentName)
		}
		if event.BeadID != "kd-abc123" {
			t.Fatalf("expected bead ID kd-abc123, got %s", event.BeadID)
		}
		if event.Metadata["namespace"] != "test-ns" {
			t.Fatalf("expected namespace test-ns, got %s", event.Metadata["namespace"])
		}
		if event.Metadata["image"] != "test-image:latest" {
			t.Fatalf("expected image test-image:latest, got %s", event.Metadata["image"])
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}

	cancel()

	// Verify last event ID was tracked.
	if w.LastEventID() != "1" {
		t.Fatalf("expected last event ID 1, got %s", w.LastEventID())
	}
}

// TestSSEWatcher_ParsesAgentClosedEvent verifies that a closed event maps to AgentDone.
func TestSSEWatcher_ParsesAgentClosedEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-close1",
				"type":   "agent",
				"status": "closed",
				"fields": map[string]string{"project": "p", "role": "qa", "agent": "a1"},
			},
			"closed_by": "human",
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:42\nevent:beads.bead.closed\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		if event.Type != AgentDone {
			t.Fatalf("expected AgentDone, got %s", event.Type)
		}
		if event.BeadID != "kd-close1" {
			t.Fatalf("expected bead ID kd-close1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	cancel()
}

// TestSSEWatcher_SkipsNonAgentBeads verifies that non-agent beads are ignored.
func TestSSEWatcher_SkipsNonAgentBeads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send a task bead (not agent).
		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-task1",
				"type":   "task",
				"status": "in_progress",
				"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:1\nevent:beads.bead.created\ndata:%s\n\n", data)
		flusher.Flush()

		// Then send an agent bead.
		payload2 := map[string]any{
			"bead": map[string]any{
				"id":     "kd-agent1",
				"type":   "agent",
				"status": "in_progress",
				"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
			},
		}
		data2, _ := json.Marshal(payload2)
		fmt.Fprintf(w, "id:2\nevent:beads.bead.created\ndata:%s\n\n", data2)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		// The first event should be the agent bead, not the task bead.
		if event.BeadID != "kd-agent1" {
			t.Fatalf("expected first event to be kd-agent1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	cancel()
}

// TestSSEWatcher_AgentStopOnUpdated verifies that updated events with
// agent_state=stopping map to AgentStop.
func TestSSEWatcher_AgentStopOnUpdated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		payload := map[string]any{
			"bead": map[string]any{
				"id":          "kd-stop1",
				"type":        "agent",
				"status":      "in_progress",
				"agent_state": "stopping",
				"fields":      map[string]string{"project": "p", "role": "devops", "agent": "a1"},
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:5\nevent:beads.bead.updated\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		if event.Type != AgentStop {
			t.Fatalf("expected AgentStop, got %s", event.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	cancel()
}

// TestSSEWatcher_KeepaliveIgnored verifies that keepalive comments don't break parsing.
func TestSSEWatcher_KeepaliveIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send keepalive comments before the actual event.
		fmt.Fprintf(w, ":keepalive\n\n")
		flusher.Flush()
		fmt.Fprintf(w, ":keepalive\n\n")
		flusher.Flush()

		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-ka1",
				"type":   "agent",
				"status": "in_progress",
				"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:3\nevent:beads.bead.created\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		if event.BeadID != "kd-ka1" {
			t.Fatalf("expected kd-ka1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	cancel()
}

// TestSSEWatcher_TopicFilterInURL verifies that topic filter is passed as query param.
func TestSSEWatcher_TopicFilterInURL(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.String()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Close immediately.
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Topics:        "beads.bead.*,beads.label.*",
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	// Wait for at least one connection attempt.
	time.Sleep(500 * time.Millisecond)
	cancel()

	expected := "/v1/events/stream?topics=beads.bead.*,beads.label.*"
	if receivedPath != expected {
		t.Fatalf("expected URL %q, got %q", expected, receivedPath)
	}
}
