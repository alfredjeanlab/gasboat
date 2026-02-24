// Package bridge provides SSE event deduplication for the slack-bridge.
//
// Dedup tracks seen events by prefixed keys to prevent duplicate Slack
// notifications. It is used by all watchers (decisions, agents, jacks) and
// handles both in-session dedup (same event replayed by SSE reconnect) and
// cross-session dedup (state persisted via StateManager).
package bridge

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// Dedup provides event deduplication for the slack-bridge watchers.
type Dedup struct {
	mu   sync.Mutex
	seen map[string]bool // prefixed key → true

	logger *slog.Logger
}

// NewDedup creates a new event deduplicator.
func NewDedup(logger *slog.Logger) *Dedup {
	return &Dedup{
		seen:   make(map[string]bool),
		logger: logger,
	}
}

// Seen returns true if the key has already been processed. If not, marks it as seen.
// Keys should be prefixed by event type, e.g., "created:dec-1", "resolved:dec-1".
func (d *Dedup) Seen(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen[key] {
		return true
	}
	d.seen[key] = true
	return false
}

// Mark records a key as seen without checking.
func (d *Dedup) Mark(key string) {
	d.mu.Lock()
	d.seen[key] = true
	d.mu.Unlock()
}

// CatchUpDecisions fetches pending decisions from the daemon and pre-populates
// the dedup map. Decisions older than 1 hour are skipped to prevent flood on
// cloned DBs. New decisions are notified with rate limiting.
func (d *Dedup) CatchUpDecisions(ctx context.Context, daemon BeadClient, notifier Notifier, logger *slog.Logger) {
	if daemon == nil {
		return
	}

	decisions, err := daemon.ListDecisionBeads(ctx)
	if err != nil {
		logger.Warn("catch-up: failed to list pending decisions", "error", err)
		return
	}

	notified := 0
	skippedSeen := 0

	for _, dec := range decisions {
		key := "created:" + dec.ID
		if d.Seen(key) {
			skippedSeen++
			continue
		}

		// Skip decisions that already have a chosen value (resolved).
		if dec.Fields["chosen"] != "" {
			d.Mark("resolved:" + dec.ID)
			continue
		}

		// Notify if we have a notifier.
		if notifier != nil {
			if err := notifier.NotifyDecision(ctx, beadEventFromDetail(dec)); err != nil {
				logger.Error("catch-up: failed to notify decision", "id", dec.ID, "error", err)
			} else {
				notified++
			}
			// Rate limit: ~1 notification per second.
			time.Sleep(1100 * time.Millisecond)
		}
	}

	logger.Info("catch-up complete",
		"total", len(decisions),
		"notified", notified,
		"skipped_seen", skippedSeen)
}

// beadEventFromDetail converts a BeadDetail to a BeadEvent for notification.
func beadEventFromDetail(d *beadsapi.BeadDetail) BeadEvent {
	return BeadEvent{
		ID:       d.ID,
		Type:     d.Type,
		Title:    d.Title,
		Status:   d.Status,
		Assignee: d.Assignee,
		Labels:   d.Labels,
		Fields:   d.Fields,
	}
}
