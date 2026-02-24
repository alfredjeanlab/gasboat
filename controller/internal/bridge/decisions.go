// Package bridge provides the decision lifecycle watcher.
//
// Decisions subscribes to kbeads SSE event stream for bead create/close events,
// filters for type=decision beads, and:
//   - On create: notifies an optional Notifier (e.g., Slack).
//   - On close: reads the agent field, looks up the agent's coop_url,
//     and POSTs a nudge so the idle agent wakes up and reads the result.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// BeadClient is the subset of client.Client used by the bridge package.
type BeadClient interface {
	GetBead(ctx context.Context, beadID string) (*beadsapi.BeadDetail, error)
	FindAgentBead(ctx context.Context, agentName string) (*beadsapi.BeadDetail, error)
	CloseBead(ctx context.Context, beadID string, fields map[string]string) error
	ListDecisionBeads(ctx context.Context) ([]*beadsapi.BeadDetail, error)
	ListAgentBeads(ctx context.Context) ([]beadsapi.AgentBead, error)
}

// BeadEvent is the JSON payload published on beads.bead.created / beads.bead.closed.
type BeadEvent struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Title    string            `json:"title"`
	Status   string            `json:"status"`
	Assignee string            `json:"assignee"`
	Labels   []string          `json:"labels"`
	Fields   map[string]string `json:"fields"`
	Priority int               `json:"priority"`
}

// Notifier sends decision lifecycle notifications to an external system.
type Notifier interface {
	// NotifyDecision is called when a new decision bead is created.
	NotifyDecision(ctx context.Context, bead BeadEvent) error
	// UpdateDecision is called when a decision bead is closed/resolved.
	UpdateDecision(ctx context.Context, beadID, chosen string) error
	// NotifyEscalation is called when a decision bead is escalated.
	NotifyEscalation(ctx context.Context, bead BeadEvent) error
	// DismissDecision is called when a decision bead expires (removes Slack message).
	DismissDecision(ctx context.Context, beadID string) error
}

// escalationTTL is the maximum age of entries in the escalated dedup map.
const escalationTTL = 1 * time.Hour

// Decisions watches the kbeads SSE event stream for decision bead lifecycle events.
type Decisions struct {
	daemon     BeadClient
	notifier   Notifier // nil = no notifications
	logger     *slog.Logger
	httpClient *http.Client // reused for nudge requests

	escalatedMu sync.Mutex
	escalated   map[string]time.Time // bead ID → notification time (dedup with TTL)
}

// DecisionsConfig holds configuration for the Decisions watcher.
type DecisionsConfig struct {
	Daemon   BeadClient
	Notifier Notifier
	Logger   *slog.Logger
}

// NewDecisions creates a new decision lifecycle watcher.
func NewDecisions(cfg DecisionsConfig) *Decisions {
	return &Decisions{
		daemon:     cfg.Daemon,
		notifier:   cfg.Notifier,
		logger:     cfg.Logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		escalated:  make(map[string]time.Time),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// decision bead created, closed, and updated events.
func (d *Decisions) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.created", d.handleCreated)
	stream.On("beads.bead.closed", d.handleClosed)
	stream.On("beads.bead.updated", d.handleUpdated)
	d.logger.Info("decisions watcher registered SSE handlers",
		"topics", []string{"beads.bead.created", "beads.bead.closed", "beads.bead.updated"})
}

func (d *Decisions) handleCreated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		d.logger.Debug("skipping malformed bead created event")
		return
	}
	if bead.Type != "decision" {
		return
	}

	d.logger.Info("decision bead created",
		"id", bead.ID,
		"title", bead.Title,
		"assignee", bead.Assignee)

	if d.notifier != nil {
		if err := d.notifier.NotifyDecision(ctx, *bead); err != nil {
			d.logger.Error("failed to notify decision", "id", bead.ID, "error", err)
		}
	}
}

func (d *Decisions) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		d.logger.Debug("skipping malformed bead closed event")
		return
	}
	if bead.Type != "decision" {
		return
	}

	chosen := bead.Fields["chosen"]

	// SSE close events may not include close-time fields (chosen, rationale).
	// Fetch the full bead if chosen is missing.
	if chosen == "" {
		if detail, err := d.daemon.GetBead(ctx, bead.ID); err == nil {
			chosen = detail.Fields["chosen"]
			if bead.Assignee == "" {
				bead.Assignee = detail.Assignee
			}
		}
	}

	d.logger.Info("decision bead closed",
		"id", bead.ID,
		"chosen", chosen,
		"assignee", bead.Assignee)

	// Detect expiry: system:timeout closures dismiss the Slack message.
	rationale := bead.Fields["rationale"]
	if chosen == "_expired" || chosen == "dismissed" {
		if d.notifier != nil {
			if err := d.notifier.DismissDecision(ctx, bead.ID); err != nil {
				d.logger.Error("failed to dismiss expired decision", "id", bead.ID, "error", err)
			}
		}
		d.logger.Info("decision expired/dismissed, Slack message removed",
			"id", bead.ID, "chosen", chosen, "rationale", rationale)
		// Still nudge the agent even on expiry so it knows the gate is closed.
		d.nudgeAgent(ctx, *bead)
		return
	}

	// Notify external system (e.g., update Slack message).
	if d.notifier != nil {
		if err := d.notifier.UpdateDecision(ctx, bead.ID, chosen); err != nil {
			d.logger.Error("failed to update decision notification", "id", bead.ID, "error", err)
		}
	}

	// Nudge the agent via coop so it wakes up and reads the decision result.
	d.nudgeAgent(ctx, *bead)
}

func (d *Decisions) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}
	if bead.Type != "decision" {
		return
	}

	// Detect escalation: decision updated with "escalated" label.
	isEscalated := false
	for _, label := range bead.Labels {
		if label == "escalated" {
			isEscalated = true
			break
		}
	}
	if !isEscalated {
		return
	}

	// Deduplicate escalation notifications (with periodic TTL cleanup).
	d.escalatedMu.Lock()
	d.cleanupEscalatedLocked()
	if _, seen := d.escalated[bead.ID]; seen {
		d.escalatedMu.Unlock()
		return
	}
	d.escalated[bead.ID] = time.Now()
	d.escalatedMu.Unlock()

	d.logger.Info("decision escalated",
		"id", bead.ID,
		"title", bead.Title,
		"priority", bead.Priority,
		"assignee", bead.Assignee)

	if d.notifier != nil {
		if err := d.notifier.NotifyEscalation(ctx, *bead); err != nil {
			d.logger.Error("failed to notify escalation", "id", bead.ID, "error", err)
		}
	}
}

// cleanupEscalatedLocked removes entries older than escalationTTL.
// Caller must hold d.escalatedMu.
func (d *Decisions) cleanupEscalatedLocked() {
	now := time.Now()
	for id, t := range d.escalated {
		if now.Sub(t) > escalationTTL {
			delete(d.escalated, id)
		}
	}
}

// nudgeAgent looks up the agent's coop_url from the agent bead and POSTs a nudge.
func (d *Decisions) nudgeAgent(ctx context.Context, bead BeadEvent) {
	agentName := bead.Assignee
	if agentName == "" {
		d.logger.Warn("decision bead has no assignee, cannot nudge", "id", bead.ID)
		return
	}

	// Look up the agent bead to get the coop_url.
	agentBead, err := d.daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		d.logger.Error("failed to get agent bead for nudge",
			"agent", agentName, "decision", bead.ID, "error", err)
		return
	}

	coopURL := beadsapi.ParseNotes(agentBead.Notes)["coop_url"]
	if coopURL == "" {
		d.logger.Warn("agent bead has no coop_url, cannot nudge",
			"agent", agentName, "decision", bead.ID)
		return
	}

	chosen := bead.Fields["chosen"]
	rationale := bead.Fields["rationale"]
	message := fmt.Sprintf("Decision resolved: %s", chosen)
	if rationale != "" {
		message += fmt.Sprintf(" — %s", rationale)
	}

	if err := nudgeCoop(ctx, d.httpClient, coopURL, message); err != nil {
		d.logger.Error("failed to nudge agent",
			"agent", agentName, "coop_url", coopURL, "error", err)
		return
	}

	d.logger.Info("nudged agent after decision resolved",
		"agent", agentName, "decision", bead.ID, "chosen", chosen)
}

// nudgeCoop POSTs a nudge message to a coop agent endpoint.
func nudgeCoop(ctx context.Context, client *http.Client, coopURL, message string) error {
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return fmt.Errorf("marshal nudge body: %w", err)
	}

	url := coopURL + "/api/v1/agent/nudge"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create nudge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("nudge request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("nudge returned status %d", resp.StatusCode)
	}
	return nil
}
