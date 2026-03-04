// Package bridge provides the JIRA sync-back watcher.
//
// JiraSync subscribes to kbeads SSE bead updated/closed events, filters for
// beads with the source:jira label, and syncs status changes, MR links, and
// closing comments back to the originating JIRA issue.
package bridge

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// syncTTL is the dedup window for sync-back operations.
const syncTTL = 10 * time.Minute

// JiraSync watches bead SSE events and syncs MR links and status back to JIRA.
type JiraSync struct {
	jira                   *JiraClient
	logger                 *slog.Logger
	disableTransitions     bool
	enableClaimTransition  bool   // override: allow "In Progress" on claim even when disableTransitions is true
	botAccountID           string // optional JIRA account ID for self-assignment

	mu   sync.Mutex
	seen map[string]time.Time // dedup key → last sync time
}

// JiraSyncConfig holds configuration for the JiraSync watcher.
type JiraSyncConfig struct {
	Jira                  *JiraClient
	Logger                *slog.Logger
	DisableTransitions    bool
	EnableClaimTransition bool   // override: allow "In Progress" on claim even when DisableTransitions is true
	BotAccountID          string // optional: JIRA account ID for self-assignment
}

// NewJiraSync creates a new JIRA sync-back watcher.
func NewJiraSync(cfg JiraSyncConfig) *JiraSync {
	return &JiraSync{
		jira:                  cfg.Jira,
		logger:                cfg.Logger,
		disableTransitions:    cfg.DisableTransitions,
		enableClaimTransition: cfg.EnableClaimTransition,
		botAccountID:          cfg.BotAccountID,
		seen:                  make(map[string]time.Time),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// bead updated and closed events.
func (s *JiraSync) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.updated", s.handleUpdated)
	stream.On("beads.bead.closed", s.handleClosed)
	s.logger.Info("JIRA sync watcher registered SSE handlers",
		"topics", []string{"beads.bead.updated", "beads.bead.closed"})
}

func (s *JiraSync) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	// Only sync beads that originated from JIRA.
	jiraKey := jiraKeyFromBead(*bead)
	if jiraKey == "" {
		return
	}

	// Sync agent claim — post comment, add label, transition, and optionally assign.
	if bead.Status == "in_progress" && bead.Assignee != "" {
		dedupKey := "claimed:" + bead.ID + ":" + bead.Assignee
		if !s.isDuplicate(dedupKey) {
			s.logger.Info("syncing agent claim to JIRA",
				"bead", bead.ID, "jira_key", jiraKey, "assignee", bead.Assignee)

			comment := "Gasboat agent " + bead.Assignee + " is working on this issue."
			if err := s.jira.AddComment(ctx, jiraKey, comment); err != nil {
				s.logger.Error("failed to add JIRA claim comment",
					"jira_key", jiraKey, "error", err)
			}

			if err := s.jira.AddLabels(ctx, jiraKey, []string{"gasboat"}); err != nil {
				s.logger.Error("failed to add gasboat label",
					"jira_key", jiraKey, "error", err)
			}

			if s.enableClaimTransition || !s.disableTransitions {
				if err := s.jira.TransitionIssue(ctx, jiraKey, "In Progress"); err != nil {
					s.logger.Warn("failed to transition JIRA issue to In Progress",
						"jira_key", jiraKey, "error", err)
				}
			}

			if s.botAccountID != "" {
				if err := s.jira.AssignIssue(ctx, jiraKey, s.botAccountID); err != nil {
					s.logger.Warn("failed to assign JIRA issue to bot",
						"jira_key", jiraKey, "error", err)
				}
			}
		}
	}

	// Check for MR URL field — sync it as a remote link to JIRA.
	if mrURL := bead.Fields["mr_url"]; mrURL != "" {
		dedupKey := "mr:" + bead.ID + ":" + mrURL
		if !s.isDuplicate(dedupKey) {
			s.logger.Info("syncing MR link to JIRA",
				"bead", bead.ID, "jira_key", jiraKey, "mr_url", mrURL)

			title := "Merge Request: " + bead.Title
			if err := s.jira.AddRemoteLink(ctx, jiraKey, mrURL, title); err != nil {
				s.logger.Error("failed to add JIRA remote link",
					"jira_key", jiraKey, "mr_url", mrURL, "error", err)
			} else {
				comment := "Automated MR created: " + mrURL
				if err := s.jira.AddComment(ctx, jiraKey, comment); err != nil {
					s.logger.Error("failed to add JIRA comment for MR",
						"jira_key", jiraKey, "error", err)
				}
			}
		}
	}

	// Check for mr_merged=true — transition JIRA issue to Review.
	if bead.Fields["mr_merged"] == "true" {
		dedupKey := "merged:" + bead.ID
		if !s.isDuplicate(dedupKey) {
			s.logger.Info("MR merged, transitioning JIRA to Review",
				"bead", bead.ID, "jira_key", jiraKey)

			comment := "MR merged — transitioning to Review."
			if err := s.jira.AddComment(ctx, jiraKey, comment); err != nil {
				s.logger.Error("failed to add JIRA MR merged comment",
					"jira_key", jiraKey, "error", err)
			}

			if !s.disableTransitions {
				if err := s.jira.TransitionIssue(ctx, jiraKey, "Review"); err != nil {
					s.logger.Warn("failed to transition JIRA issue to Review",
						"jira_key", jiraKey, "error", err)
				}
			}
		}
	}
}

func (s *JiraSync) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	jiraKey := jiraKeyFromBead(*bead)
	if jiraKey == "" {
		return
	}

	// Dedup: don't re-close the same bead.
	dedupKey := "close:" + bead.ID
	if s.isDuplicate(dedupKey) {
		return
	}

	s.logger.Info("syncing bead closure to JIRA",
		"bead", bead.ID, "jira_key", jiraKey)

	// Post closing comment (no transition — transitions are triggered by mr_merged).
	comment := "Task bead closed in beads system (bead " + bead.ID + ")."
	if mrURL := bead.Fields["mr_url"]; mrURL != "" {
		comment += " MR: " + mrURL
	}
	if err := s.jira.AddComment(ctx, jiraKey, comment); err != nil {
		s.logger.Error("failed to add JIRA closing comment",
			"jira_key", jiraKey, "error", err)
	}
}

// jiraKeyFromBead extracts the JIRA key from a bead's labels or fields.
func jiraKeyFromBead(bead BeadEvent) string {
	// Check fields first (set by poller).
	if key := bead.Fields["jira_key"]; key != "" {
		return key
	}
	// Fallback: check labels for jira:<key> pattern.
	for _, label := range bead.Labels {
		if strings.HasPrefix(label, "jira:") && !strings.HasPrefix(label, "jira-label:") {
			return strings.TrimPrefix(label, "jira:")
		}
	}
	return ""
}

// isDuplicate returns true if the key was seen within the syncTTL window.
// If not, records the key and returns false.
func (s *JiraSync) isDuplicate(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Periodic cleanup.
	s.cleanupLocked()

	if t, ok := s.seen[key]; ok && time.Since(t) < syncTTL {
		return true
	}
	s.seen[key] = time.Now()
	return false
}

// cleanupLocked removes stale entries from the dedup map.
// Caller must hold s.mu.
func (s *JiraSync) cleanupLocked() {
	now := time.Now()
	for key, t := range s.seen {
		if now.Sub(t) > syncTTL {
			delete(s.seen, key)
		}
	}
}
