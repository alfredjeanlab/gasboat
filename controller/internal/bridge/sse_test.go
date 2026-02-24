package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestParseBeadEvent_ValidPayload(t *testing.T) {
	// Simulate a kbeads SSE payload: {"bead": {"id": "...", ...}}
	payload := map[string]any{
		"bead": map[string]any{
			"id":       "dec-1",
			"type":     "decision",
			"title":    "Pick a color",
			"status":   "open",
			"assignee": "crew-hq",
			"labels":   []string{"urgent"},
			"priority": 1,
			"fields": map[string]string{
				"question": "What color?",
				"options":  `["red","blue"]`,
			},
		},
	}
	data, _ := json.Marshal(payload)

	bead := ParseBeadEvent(data)
	if bead == nil {
		t.Fatal("expected non-nil BeadEvent")
	}
	if bead.ID != "dec-1" {
		t.Errorf("expected ID dec-1, got %s", bead.ID)
	}
	if bead.Type != "decision" {
		t.Errorf("expected type decision, got %s", bead.Type)
	}
	if bead.Title != "Pick a color" {
		t.Errorf("expected title 'Pick a color', got %s", bead.Title)
	}
	if bead.Assignee != "crew-hq" {
		t.Errorf("expected assignee crew-hq, got %s", bead.Assignee)
	}
	if bead.Priority != 1 {
		t.Errorf("expected priority 1, got %d", bead.Priority)
	}
	if bead.Fields["question"] != "What color?" {
		t.Errorf("expected question field, got %v", bead.Fields)
	}
	if len(bead.Labels) != 1 || bead.Labels[0] != "urgent" {
		t.Errorf("expected labels [urgent], got %v", bead.Labels)
	}
}

func TestParseBeadEvent_MalformedJSON(t *testing.T) {
	bead := ParseBeadEvent([]byte(`{invalid`))
	if bead != nil {
		t.Errorf("expected nil for malformed JSON, got %+v", bead)
	}
}

func TestParseBeadEvent_MissingBead(t *testing.T) {
	bead := ParseBeadEvent([]byte(`{"bead_id": "abc"}`))
	if bead != nil {
		t.Errorf("expected nil for missing bead key, got %+v", bead)
	}
}

func TestParseBeadEvent_EmptyFields(t *testing.T) {
	payload := map[string]any{
		"bead": map[string]any{
			"id":   "test-1",
			"type": "mail",
		},
	}
	data, _ := json.Marshal(payload)

	bead := ParseBeadEvent(data)
	if bead == nil {
		t.Fatal("expected non-nil BeadEvent")
	}
	if bead.ID != "test-1" {
		t.Errorf("expected ID test-1, got %s", bead.ID)
	}
	if len(bead.Fields) != 0 {
		t.Errorf("expected empty fields, got %v", bead.Fields)
	}
}

func TestSSEStream_DispatchesEvents(t *testing.T) {
	// Set up a fake SSE server.
	var mu sync.Mutex
	var receivedTopics []string
	var receivedData []string

	sseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events/stream" {
			http.NotFound(w, r)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send two events.
		fmt.Fprintf(w, "id:1\n")
		fmt.Fprintf(w, "event:beads.bead.created\n")
		fmt.Fprintf(w, "data:{\"bead\":{\"id\":\"dec-1\",\"type\":\"decision\"}}\n")
		fmt.Fprintf(w, "\n")
		flusher.Flush()

		fmt.Fprintf(w, "id:2\n")
		fmt.Fprintf(w, "event:beads.bead.closed\n")
		fmt.Fprintf(w, "data:{\"bead\":{\"id\":\"dec-1\",\"type\":\"decision\"}}\n")
		fmt.Fprintf(w, "\n")
		flusher.Flush()

		// Keep connection open briefly so client can read.
		time.Sleep(200 * time.Millisecond)
	}))
	defer sseServer.Close()

	stream := NewSSEStream(SSEStreamConfig{
		BeadsHTTPAddr: sseServer.URL,
		Topics:        []string{"beads.bead.created", "beads.bead.closed"},
		Logger:        slog.Default(),
	})

	// Register handlers for both topics.
	stream.On("beads.bead.created", func(_ context.Context, data []byte) {
		mu.Lock()
		receivedTopics = append(receivedTopics, "beads.bead.created")
		receivedData = append(receivedData, string(data))
		mu.Unlock()
	})
	stream.On("beads.bead.closed", func(_ context.Context, data []byte) {
		mu.Lock()
		receivedTopics = append(receivedTopics, "beads.bead.closed")
		receivedData = append(receivedData, string(data))
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start stream in background.
	go func() {
		_ = stream.Start(ctx)
	}()

	// Wait for events to be received.
	deadline := time.After(1500 * time.Millisecond)
	for {
		mu.Lock()
		count := len(receivedTopics)
		mu.Unlock()
		if count >= 2 {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("timed out waiting for events, got %d: %v", len(receivedTopics), receivedTopics)
			mu.Unlock()
			return
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if len(receivedTopics) != 2 {
		t.Fatalf("expected 2 events, got %d", len(receivedTopics))
	}
	if receivedTopics[0] != "beads.bead.created" {
		t.Errorf("expected first topic beads.bead.created, got %s", receivedTopics[0])
	}
	if receivedTopics[1] != "beads.bead.closed" {
		t.Errorf("expected second topic beads.bead.closed, got %s", receivedTopics[1])
	}

	// Verify the data payloads can be parsed.
	for i, d := range receivedData {
		bead := ParseBeadEvent([]byte(d))
		if bead == nil {
			t.Errorf("event %d: failed to parse bead event from data: %s", i, d)
			continue
		}
		if bead.ID != "dec-1" {
			t.Errorf("event %d: expected bead ID dec-1, got %s", i, bead.ID)
		}
	}
}
