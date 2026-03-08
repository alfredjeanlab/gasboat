// Package bridge provides the wasteland sync-back watcher.
//
// WastelandSync subscribes to kbeads SSE bead updated/closed events, filters
// for beads with the source:wasteland label, and syncs claims and completions
// back to the wasteland commons via dolt.
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// wastelandSyncTTL is the dedup window for sync-back operations.
const wastelandSyncTTL = 10 * time.Minute

// WastelandSync watches bead SSE events and syncs claims and completions
// back to the wasteland commons.
type WastelandSync struct {
	dolt      DoltClient
	logger    *slog.Logger
	rigHandle string

	mu   sync.Mutex
	seen map[string]time.Time // dedup key → last sync time
}

// WastelandSyncConfig holds configuration for the WastelandSync watcher.
type WastelandSyncConfig struct {
	Dolt      DoltClient
	Logger    *slog.Logger
	RigHandle string
}

// NewWastelandSync creates a new wasteland sync-back watcher.
func NewWastelandSync(cfg WastelandSyncConfig) *WastelandSync {
	return &WastelandSync{
		dolt:      cfg.Dolt,
		logger:    cfg.Logger,
		rigHandle: cfg.RigHandle,
		seen:      make(map[string]time.Time),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// bead updated and closed events.
func (s *WastelandSync) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.updated", s.handleUpdated)
	stream.On("beads.bead.closed", s.handleClosed)
	s.logger.Info("wasteland sync watcher registered SSE handlers",
		"topics", []string{"beads.bead.updated", "beads.bead.closed"})
}

func (s *WastelandSync) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	// Only sync beads that originated from the wasteland.
	wantedID := wantedIDFromBead(*bead)
	if wantedID == "" {
		return
	}

	// Sync claim — when a bead moves to in_progress, claim the wanted item.
	if bead.Status == "in_progress" && bead.Assignee != "" {
		dedupKey := "wl-claimed:" + bead.ID + ":" + bead.Assignee
		if !s.isDuplicate(dedupKey) {
			s.logger.Info("syncing bead claim to wasteland",
				"bead", bead.ID, "wanted_id", wantedID, "assignee", bead.Assignee)

			if err := s.dolt.ClaimWanted(ctx, wantedID, s.rigHandle); err != nil {
				s.logger.Error("failed to claim wasteland item",
					"wanted_id", wantedID, "error", err)
			} else {
				if err := s.dolt.Push(ctx); err != nil {
					s.logger.Warn("failed to push wasteland claim",
						"wanted_id", wantedID, "error", err)
				}
			}
		}
	}
}

func (s *WastelandSync) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	wantedID := wantedIDFromBead(*bead)
	if wantedID == "" {
		return
	}

	dedupKey := "wl-close:" + bead.ID
	if s.isDuplicate(dedupKey) {
		return
	}

	s.logger.Info("syncing bead closure to wasteland",
		"bead", bead.ID, "wanted_id", wantedID)

	// Generate a completion ID and evidence summary.
	completionID := fmt.Sprintf("c-%s-%d", wantedID, time.Now().Unix())
	evidence := fmt.Sprintf("Bead %s closed. Title: %s", bead.ID, bead.Title)
	if mrURL := bead.Fields["mr_url"]; mrURL != "" {
		evidence += " MR: " + mrURL
	}

	if err := s.dolt.SubmitCompletion(ctx, completionID, wantedID, s.rigHandle, evidence); err != nil {
		s.logger.Error("failed to submit wasteland completion",
			"wanted_id", wantedID, "error", err)
		return
	}

	if err := s.dolt.Push(ctx); err != nil {
		s.logger.Warn("failed to push wasteland completion",
			"wanted_id", wantedID, "error", err)
	}
}

// isDuplicate returns true if the key was seen within the wastelandSyncTTL window.
func (s *WastelandSync) isDuplicate(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupLocked()

	if t, ok := s.seen[key]; ok && time.Since(t) < wastelandSyncTTL {
		return true
	}
	s.seen[key] = time.Now()
	return false
}

// cleanupLocked removes stale entries from the dedup map.
func (s *WastelandSync) cleanupLocked() {
	now := time.Now()
	for key, t := range s.seen {
		if now.Sub(t) > wastelandSyncTTL {
			delete(s.seen, key)
		}
	}
}
