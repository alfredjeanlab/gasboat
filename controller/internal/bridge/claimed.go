// Package bridge provides the claimed bead update watcher.
//
// Claimed watches the kbeads SSE event stream for bead update and closed
// events and nudges the assignee when a bead they have claimed changes. This
// helps agents notice changes to their in-progress work without polling.
//
// On update: nudge with a review prompt so the agent can react to the change.
// On close: nudge with a checkpoint reminder so the agent creates a decision
// bead before the Stop hook fires (closing without a decision leaves no
// re-entry handle for the human operator).
//
// Note: the kbeads SSE update event does not include a "updated_by" field,
// so nudges fire for all updates to claimed beads (including self-updates).
// Rate limiting (5 minutes per bead) prevents notification spam.
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// claimedNudgeTTL is the minimum interval between nudges for the same bead.
const claimedNudgeTTL = 5 * time.Minute

// skipClaimedTypes are bead types that should not trigger claimed-update nudges.
// These are system/infrastructure beads rather than actionable work.
var skipClaimedTypes = map[string]bool{
	"agent":    true,
	"decision": true,
	"mail":     true,
	"project":  true,
	"report":   true,
}

// ClaimedConfig holds configuration for the Claimed watcher.
type ClaimedConfig struct {
	Daemon BeadClient
	Logger *slog.Logger
}

// Claimed watches the kbeads SSE event stream for bead update events and
// nudges the claiming agent when a bead they own is updated.
type Claimed struct {
	daemon     BeadClient
	logger     *slog.Logger
	httpClient *http.Client

	nudgedMu sync.Mutex
	nudged   map[string]time.Time // bead ID → last nudge time
}

// NewClaimed creates a new claimed bead update watcher.
func NewClaimed(cfg ClaimedConfig) *Claimed {
	return &Claimed{
		daemon:     cfg.Daemon,
		logger:     cfg.Logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		nudged:     make(map[string]time.Time),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// bead updated and closed events.
func (c *Claimed) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.updated", c.handleUpdated)
	stream.On("beads.bead.closed", c.handleClosed)
	c.logger.Info("claimed watcher registered SSE handlers",
		"topics", []string{"beads.bead.updated", "beads.bead.closed"})
}

func (c *Claimed) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		c.logger.Debug("skipping malformed bead updated event")
		return
	}

	// Only nudge for claimed beads (assignee present).
	if bead.Assignee == "" {
		return
	}

	// Skip infrastructure/system bead types.
	if skipClaimedTypes[bead.Type] {
		return
	}

	// Rate-limit: skip if we nudged for this bead recently.
	if !c.shouldNudge(bead.ID) {
		return
	}

	c.logger.Info("claimed bead updated, nudging assignee",
		"id", bead.ID,
		"title", bead.Title,
		"assignee", bead.Assignee,
		"type", bead.Type)

	message := fmt.Sprintf("Your claimed bead %s %q was updated — run 'kd show %s' to review",
		bead.ID, bead.Title, bead.ID)
	c.nudgeAgent(ctx, *bead, message)
}

func (c *Claimed) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		c.logger.Debug("skipping malformed bead closed event")
		return
	}

	// Only nudge for claimed beads (assignee present).
	if bead.Assignee == "" {
		return
	}

	// Skip infrastructure/system bead types.
	if skipClaimedTypes[bead.Type] {
		return
	}

	// Rate-limit: skip if we nudged for this bead recently.
	if !c.shouldNudge(bead.ID) {
		return
	}

	c.logger.Info("claimed bead closed, nudging assignee for checkpoint",
		"id", bead.ID,
		"title", bead.Title,
		"assignee", bead.Assignee,
		"type", bead.Type)

	message := fmt.Sprintf("Your claimed bead %s %q was closed — work bead closed, create a decision checkpoint now",
		bead.ID, bead.Title)
	c.nudgeAgent(ctx, *bead, message)
}

// shouldNudge returns true if a nudge should be sent for the given bead ID.
// Enforces a rate limit of one nudge per bead per claimedNudgeTTL window.
// Cleans up expired entries on each call.
func (c *Claimed) shouldNudge(beadID string) bool {
	c.nudgedMu.Lock()
	defer c.nudgedMu.Unlock()

	now := time.Now()

	// Clean up expired entries.
	for id, t := range c.nudged {
		if now.Sub(t) > claimedNudgeTTL {
			delete(c.nudged, id)
		}
	}

	if last, seen := c.nudged[beadID]; seen && now.Sub(last) <= claimedNudgeTTL {
		return false
	}

	c.nudged[beadID] = now
	return true
}

// nudgeAgent looks up the agent's coop_url and POSTs a nudge with the given message.
func (c *Claimed) nudgeAgent(ctx context.Context, bead BeadEvent, message string) {
	agentBead, err := c.daemon.FindAgentBead(ctx, bead.Assignee)
	if err != nil {
		c.logger.Error("failed to get agent bead for claimed nudge",
			"agent", bead.Assignee, "bead", bead.ID, "error", err)
		return
	}

	coopURL := beadsapi.ParseNotes(agentBead.Notes)["coop_url"]
	if coopURL == "" {
		c.logger.Warn("agent bead has no coop_url, cannot nudge",
			"agent", bead.Assignee, "bead", bead.ID)
		return
	}

	if err := nudgeCoop(ctx, c.httpClient, coopURL, message); err != nil {
		c.logger.Error("failed to nudge agent for claimed bead",
			"agent", bead.Assignee, "coop_url", coopURL, "error", err)
		return
	}

	c.logger.Info("nudged agent for claimed bead",
		"agent", bead.Assignee, "bead", bead.ID)
}
