// Package bridge provides the jack lifecycle watcher.
//
// Jacks subscribes to kbeads SSE event stream for bead create/close/update events,
// filters for type=jack beads, and notifies Slack with:
//   - Jack raised: wrench emoji + target + TTL + reason
//   - Jack lowered: checkmark + target + reason
//   - Jack expired: warning + time past TTL + suggested action
//   - Batch summary: >10 jacks/minute collapsed into single message
package bridge

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// JackNotifier sends jack lifecycle notifications to an external system.
type JackNotifier interface {
	// NotifyJackOn is called when a new jack bead is created.
	NotifyJackOn(ctx context.Context, bead BeadEvent) error
	// NotifyJackOnBatch is called when >10 jacks arrive within 1 minute.
	NotifyJackOnBatch(ctx context.Context, beads []BeadEvent) error
	// NotifyJackOff is called when a jack bead is closed (lowered).
	NotifyJackOff(ctx context.Context, bead BeadEvent) error
	// NotifyJackExpired is called when a jack bead expires past TTL.
	NotifyJackExpired(ctx context.Context, bead BeadEvent) error
}

// Jacks watches the kbeads SSE event stream for jack bead lifecycle events.
type Jacks struct {
	notifier JackNotifier
	logger   *slog.Logger

	mu          sync.Mutex
	offSeen     map[string]bool      // jack ID → already notified (dedup)
	expiredSeen map[string]time.Time // jack ID → last notified time (6h window)

	batchMu    sync.Mutex
	batch      []BeadEvent
	batchTimer *time.Timer
	batchCtx   context.Context
}

// JacksConfig holds configuration for the Jacks watcher.
type JacksConfig struct {
	Notifier JackNotifier
	Logger   *slog.Logger
}

// NewJacks creates a new jack lifecycle watcher.
func NewJacks(cfg JacksConfig) *Jacks {
	return &Jacks{
		notifier:    cfg.Notifier,
		logger:      cfg.Logger,
		offSeen:     make(map[string]bool),
		expiredSeen: make(map[string]time.Time),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// jack bead created, closed, and updated events.
func (j *Jacks) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.created", j.handleCreated)
	stream.On("beads.bead.closed", j.handleClosed)
	stream.On("beads.bead.updated", j.handleUpdated)
	j.logger.Info("jacks watcher registered SSE handlers",
		"topics", []string{"beads.bead.created", "beads.bead.closed", "beads.bead.updated"})
}

// batchWindow is the duration to accumulate jack-on events before flushing.
const batchWindow = 1 * time.Minute

// batchThreshold is the number of individual notifications before batching.
const batchThreshold = 10

func (j *Jacks) handleCreated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil || bead.Type != "jack" {
		return
	}

	j.logger.Info("jack raised",
		"id", bead.ID,
		"target", bead.Fields["target"],
		"ttl", bead.Fields["ttl"],
		"agent", bead.Assignee)

	if j.notifier == nil {
		return
	}

	j.batchMu.Lock()
	j.batch = append(j.batch, *bead)
	batchLen := len(j.batch)

	// Send individual notifications for first N.
	if batchLen <= batchThreshold {
		j.batchMu.Unlock()
		if err := j.notifier.NotifyJackOn(ctx, *bead); err != nil {
			j.logger.Error("failed to notify jack on", "id", bead.ID, "error", err)
		}
		return
	}

	// Start batch timer on first overflow.
	if j.batchTimer == nil {
		j.batchCtx = ctx
		j.batchTimer = time.AfterFunc(batchWindow, j.flushBatch)
	}
	j.batchMu.Unlock()
}

// flushBatch sends a batch summary for overflow jacks.
func (j *Jacks) flushBatch() {
	j.batchMu.Lock()
	batch := j.batch
	j.batch = nil
	j.batchTimer = nil
	ctx := j.batchCtx
	j.batchMu.Unlock()

	if len(batch) <= batchThreshold {
		return // All were sent individually
	}

	// The first batchThreshold were already notified individually.
	overflow := batch[batchThreshold:]
	if j.notifier != nil {
		if err := j.notifier.NotifyJackOnBatch(ctx, overflow); err != nil {
			j.logger.Error("failed to notify jack batch", "count", len(overflow), "error", err)
		}
	}
}

func (j *Jacks) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil || bead.Type != "jack" {
		return
	}

	j.logger.Info("jack lowered",
		"id", bead.ID,
		"target", bead.Fields["target"],
		"agent", bead.Assignee)

	// Dedup: only notify once per jack close.
	j.mu.Lock()
	if j.offSeen[bead.ID] {
		j.mu.Unlock()
		return
	}
	j.offSeen[bead.ID] = true
	j.mu.Unlock()

	if j.notifier != nil {
		if err := j.notifier.NotifyJackOff(ctx, *bead); err != nil {
			j.logger.Error("failed to notify jack off", "id", bead.ID, "error", err)
		}
	}
}

func (j *Jacks) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil || bead.Type != "jack" {
		return
	}

	// Detect expiry: jack updated with "expired" label or past TTL.
	isExpired := false
	for _, label := range bead.Labels {
		if label == "expired" {
			isExpired = true
			break
		}
	}
	if !isExpired {
		return
	}

	j.logger.Info("jack expired",
		"id", bead.ID,
		"target", bead.Fields["target"],
		"agent", bead.Assignee)

	// Dedup: only notify once per 6 hours per jack.
	j.mu.Lock()
	if lastNotified, ok := j.expiredSeen[bead.ID]; ok {
		if time.Since(lastNotified) < 6*time.Hour {
			j.mu.Unlock()
			return
		}
	}
	j.expiredSeen[bead.ID] = time.Now()
	j.mu.Unlock()

	if j.notifier != nil {
		if err := j.notifier.NotifyJackExpired(ctx, *bead); err != nil {
			j.logger.Error("failed to notify jack expired", "id", bead.ID, "error", err)
		}
	}
}
