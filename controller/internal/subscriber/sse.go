// Package subscriber watches for agent lifecycle events and emits them on a
// channel. The SSEWatcher connects to the kbeads SSE endpoint instead of NATS
// JetStream.
package subscriber

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// SSEConfig holds configuration for the SSE watcher.
type SSEConfig struct {
	// BeadsHTTPAddr is the kbeads HTTP base URL (e.g., "http://localhost:8080").
	BeadsHTTPAddr string

	// Topics is the optional comma-separated topic filter for the SSE endpoint.
	// Empty means all events.
	Topics string

	// Namespace is the default K8s namespace for pod metadata.
	Namespace string

	// CoopImage is the default container image for agent pods.
	CoopImage string

	// BeadsGRPCAddr is the beads daemon gRPC address (host:port) for agent pod env vars.
	BeadsGRPCAddr string
}

// SSEWatcher subscribes to the kbeads SSE event stream and translates bead
// events into lifecycle Events. It tracks Last-Event-ID for reconnection and
// auto-reconnects with exponential backoff.
type SSEWatcher struct {
	cfg        SSEConfig
	events     chan Event
	logger     *slog.Logger
	httpClient *http.Client // reused across reconnections (long-lived, no timeout)

	mu          sync.Mutex
	lastEventID string // tracks the most recent SSE event ID for reconnection
}

// NewSSEWatcher creates a watcher backed by the kbeads SSE event stream.
func NewSSEWatcher(cfg SSEConfig, logger *slog.Logger) *SSEWatcher {
	return &SSEWatcher{
		cfg:        cfg,
		events:     make(chan Event, 64),
		logger:     logger,
		httpClient: &http.Client{Timeout: 0}, // no timeout for long-lived SSE
	}
}

// Start begins watching the SSE stream. Blocks until ctx is canceled.
// Reconnects with exponential backoff on errors.
func (w *SSEWatcher) Start(ctx context.Context) error {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			close(w.events)
			return fmt.Errorf("watcher stopped: %w", ctx.Err())
		default:
		}

		err := w.stream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				close(w.events)
				return fmt.Errorf("watcher stopped: %w", ctx.Err())
			}
			w.logger.Warn("SSE stream error, reconnecting",
				"error", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				close(w.events)
				return fmt.Errorf("watcher stopped: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = time.Second
		}
	}
}

// Events returns a read-only channel of lifecycle events.
func (w *SSEWatcher) Events() <-chan Event {
	return w.events
}

// stream connects to the SSE endpoint and reads events until disconnection.
func (w *SSEWatcher) stream(ctx context.Context) error {
	url := strings.TrimRight(w.cfg.BeadsHTTPAddr, "/") + "/v1/events/stream"
	if w.cfg.Topics != "" {
		url += "?topics=" + w.cfg.Topics
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Send Last-Event-ID for reconnection replay.
	w.mu.Lock()
	lastID := w.lastEventID
	w.mu.Unlock()
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}

	w.logger.Info("connecting to SSE event stream",
		"url", url, "last_event_id", lastID)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE endpoint returned status %d", resp.StatusCode)
	}

	w.logger.Info("SSE stream connected")

	scanner := bufio.NewScanner(resp.Body)
	// Increase scanner buffer for large events.
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var (
		eventID   string
		eventType string
		eventData string
	)

	for scanner.Scan() {
		// Check for context cancellation.
		if ctx.Err() != nil {
			return nil
		}

		line := scanner.Text()

		// Empty line = end of event.
		if line == "" {
			if eventData != "" && eventType != "" {
				w.processSSEEvent(eventID, eventType, eventData)
			}
			// Update last event ID for reconnection.
			if eventID != "" {
				w.mu.Lock()
				w.lastEventID = eventID
				w.mu.Unlock()
			}
			eventID = ""
			eventType = ""
			eventData = ""
			continue
		}

		// Comment lines (keepalive).
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Parse SSE fields.
		if strings.HasPrefix(line, "id:") {
			eventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		} else if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			eventData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("SSE stream read: %w", err)
	}

	return fmt.Errorf("SSE stream closed by server")
}

// sseBeadPayload matches the kbeads event JSON for bead lifecycle events.
// BeadCreated: {"bead": {...}}
// BeadUpdated: {"bead": {...}, "changes": {...}}
// BeadClosed:  {"bead": {...}, "closed_by": "..."}
// BeadDeleted: {"bead_id": "..."}
type sseBeadPayload struct {
	Bead     *sseBead       `json:"bead,omitempty"`
	Changes  map[string]any `json:"changes,omitempty"`
	ClosedBy string         `json:"closed_by,omitempty"`
	BeadID   string         `json:"bead_id,omitempty"` // for delete events
}

// sseBead mirrors the kbeads model.Bead fields we need for event mapping.
type sseBead struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	Labels     []string        `json:"labels"`
	Fields     json.RawMessage `json:"fields"` // raw JSON object
	AgentState string          `json:"agent_state"`
	Assignee   string          `json:"assignee"`
	CreatedBy  string          `json:"created_by"`
}

// processSSEEvent parses an SSE event and emits a lifecycle Event if relevant.
func (w *SSEWatcher) processSSEEvent(id, topic, data string) {
	// Only care about bead lifecycle events.
	if !strings.HasPrefix(topic, "beads.bead.") {
		return
	}

	var payload sseBeadPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		w.logger.Debug("skipping malformed SSE event",
			"id", id, "topic", topic, "error", err)
		return
	}

	// Delete events have no bead — just bead_id.
	action := subjectAction(topic)
	if action == "deleted" {
		w.logger.Debug("SSE delete event", "id", id, "bead_id", payload.BeadID)
		// We cannot map delete events without full bead info (no type/fields).
		// The old NATS watcher had the same issue — it relied on the full bead payload.
		// For deletes we still need to check if it was an agent bead.
		// Since we only get bead_id, we emit a kill event and let the handler
		// deal with it by bead ID alone.
		return
	}

	if payload.Bead == nil {
		w.logger.Debug("skipping SSE event with no bead payload",
			"id", id, "topic", topic)
		return
	}

	if payload.Bead.Type != "agent" {
		return
	}

	// Convert to the internal beadData format the mapBeadEvent expects.
	fields := beadsapi.ParseFieldsJSON(payload.Bead.Fields)
	bd := beadData{
		ID:         payload.Bead.ID,
		Title:      payload.Bead.Title,
		Type:       payload.Bead.Type,
		Status:     payload.Bead.Status,
		Labels:     payload.Bead.Labels,
		Fields:     fields,
		AgentState: fields["agent_state"],
		Assignee:   payload.Bead.Assignee,
		CreatedBy:  payload.Bead.CreatedBy,
	}

	// If agent_state comes from the bead status rather than fields, use it directly.
	if payload.Bead.AgentState != "" {
		bd.AgentState = payload.Bead.AgentState
	}

	ep := beadEventPayload{
		Bead:    bd,
		Changes: payload.Changes,
	}

	event, ok := w.mapBeadEvent(action, ep)
	if !ok {
		return
	}

	w.logger.Info("emitting lifecycle event from SSE",
		"type", event.Type, "project", event.Project,
		"role", event.Role, "agent", event.AgentName,
		"bead", event.BeadID, "sse_id", id)

	select {
	case w.events <- event:
	default:
		w.logger.Warn("event channel full, dropping event",
			"type", event.Type, "bead", event.BeadID)
	}
}

// mapBeadEvent maps a daemon bead event to a subscriber Event.
// Same logic as NATSWatcher.mapBeadEvent.
func (w *SSEWatcher) mapBeadEvent(action string, payload beadEventPayload) (Event, bool) {
	switch action {
	case "created":
		return w.buildEvent(AgentSpawn, payload.Bead)
	case "updated":
		// Check if agent_state changed to stopping.
		if payload.Bead.AgentState == "stopping" {
			return w.buildEvent(AgentStop, payload.Bead)
		}
		// Check if status changed to in_progress (re-spawn).
		if payload.Bead.Status == "in_progress" {
			if _, ok := payload.Changes["status"]; ok {
				return w.buildEvent(AgentSpawn, payload.Bead)
			}
		}
		return w.buildEvent(AgentUpdate, payload.Bead)
	case "closed":
		return w.buildEvent(AgentDone, payload.Bead)
	case "deleted":
		return w.buildEvent(AgentKill, payload.Bead)
	default:
		return Event{}, false
	}
}

// buildEvent constructs a lifecycle Event from a bead payload.
// Same logic as NATSWatcher.buildEvent.
func (w *SSEWatcher) buildEvent(eventType EventType, bead beadData) (Event, bool) {
	project := bead.Fields["project"]
	mode := bead.Fields["mode"]
	role := bead.Fields["role"]
	name := bead.Fields["agent"]
	if mode == "" {
		mode = "crew"
	}

	if role == "" || name == "" {
		w.logger.Debug("skipping event with incomplete agent info",
			"action", eventType, "bead_id", bead.ID, "title", bead.Title)
		return Event{}, false
	}

	meta := map[string]string{
		"namespace": w.cfg.Namespace,
	}
	if w.cfg.CoopImage != "" {
		meta["image"] = w.cfg.CoopImage
	}
	if w.cfg.BeadsGRPCAddr != "" {
		meta["beads_grpc_addr"] = w.cfg.BeadsGRPCAddr
	}

	return Event{
		Type:      eventType,
		Project:   project,
		Mode:      mode,
		Role:      role,
		AgentName: name,
		BeadID:    bead.ID,
		Metadata:  meta,
	}, true
}

// LastEventID returns the most recently seen SSE event ID. Thread-safe.
func (w *SSEWatcher) LastEventID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastEventID
}

// SetLastEventID sets the last event ID for reconnection. Thread-safe.
// Useful for restoring state after a process restart.
func (w *SSEWatcher) SetLastEventID(id string) {
	w.mu.Lock()
	w.lastEventID = id
	w.mu.Unlock()
}

// lastEventIDUint returns the last event ID as a uint64 (0 if not set or invalid).
func (w *SSEWatcher) lastEventIDUint() uint64 {
	w.mu.Lock()
	id := w.lastEventID
	w.mu.Unlock()
	if id == "" {
		return 0
	}
	n, _ := strconv.ParseUint(id, 10, 64)
	return n
}
