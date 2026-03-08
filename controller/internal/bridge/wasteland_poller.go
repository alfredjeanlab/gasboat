// Package bridge provides the wasteland polling loop.
//
// WastelandPoller periodically pulls upstream changes and queries the
// wanted table for open items, creating task beads for untracked items.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// WastelandBeadClient is the subset of beadsapi.Client used by the wasteland poller.
type WastelandBeadClient interface {
	CreateBead(ctx context.Context, req beadsapi.CreateBeadRequest) (string, error)
	ListTaskBeads(ctx context.Context) ([]*beadsapi.BeadDetail, error)
	UpdateBeadFields(ctx context.Context, beadID string, fields map[string]string) error
}

// WastelandPollerConfig holds configuration for the wasteland poller.
type WastelandPollerConfig struct {
	PollInterval time.Duration
	RigHandle    string
	Logger       *slog.Logger
}

// WastelandPoller polls the wasteland wanted table and creates task beads.
type WastelandPoller struct {
	dolt   DoltClient
	daemon WastelandBeadClient
	cfg    WastelandPollerConfig

	mu      sync.Mutex
	tracked map[string]string // wanted_id → bead_id
}

// NewWastelandPoller creates a new wasteland polling loop.
func NewWastelandPoller(dolt DoltClient, daemon WastelandBeadClient, cfg WastelandPollerConfig) *WastelandPoller {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Minute
	}
	return &WastelandPoller{
		dolt:    dolt,
		daemon:  daemon,
		cfg:     cfg,
		tracked: make(map[string]string),
	}
}

// Run starts the polling loop. It runs CatchUp once, then polls at the
// configured interval until ctx is canceled.
func (p *WastelandPoller) Run(ctx context.Context) error {
	p.CatchUp(ctx)

	p.cfg.Logger.Info("wasteland poller started",
		"interval", p.cfg.PollInterval,
		"rig_handle", p.cfg.RigHandle)

	// Initial poll on startup.
	p.poll(ctx)

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// CatchUp queries the daemon for existing task beads with source:wasteland label
// and populates the tracked map to prevent duplicate creation across restarts.
func (p *WastelandPoller) CatchUp(ctx context.Context) {
	beads, err := p.daemon.ListTaskBeads(ctx)
	if err != nil {
		p.cfg.Logger.Warn("wasteland poller catch-up: failed to list task beads", "error", err)
		return
	}

	count := 0
	p.mu.Lock()
	for _, b := range beads {
		wantedID := b.Fields["wanted_id"]
		if wantedID != "" {
			p.tracked[wantedID] = b.ID
			count++
		}
	}
	p.mu.Unlock()

	p.cfg.Logger.Info("wasteland poller catch-up complete", "tracked", count)
}

// poll executes a single wasteland poll cycle.
func (p *WastelandPoller) poll(ctx context.Context) {
	// Pull upstream changes.
	if err := p.dolt.Pull(ctx); err != nil {
		p.cfg.Logger.Warn("wasteland pull failed", "error", err)
		// Continue with local data even if pull fails.
	}

	// Refresh tracked from daemon (self-heal across restarts).
	if beads, err := p.daemon.ListTaskBeads(ctx); err == nil {
		p.mu.Lock()
		for _, b := range beads {
			if wid := b.Fields["wanted_id"]; wid != "" {
				if _, exists := p.tracked[wid]; !exists {
					p.tracked[wid] = b.ID
				}
			}
		}
		p.mu.Unlock()
	}

	// Query open wanted items.
	items, err := p.dolt.QueryWanted(ctx, "open")
	if err != nil {
		p.cfg.Logger.Error("wasteland query wanted failed", "error", err)
		return
	}

	created := 0
	skipped := 0
	for _, item := range items {
		p.mu.Lock()
		_, exists := p.tracked[item.ID]
		p.mu.Unlock()

		if exists {
			skipped++
			continue
		}

		beadID, err := p.createBeadFromWanted(ctx, item)
		if err != nil {
			p.cfg.Logger.Error("failed to create bead for wanted item",
				"wanted_id", item.ID, "error", err)
			continue
		}

		p.mu.Lock()
		p.tracked[item.ID] = beadID
		p.mu.Unlock()
		created++

		p.cfg.Logger.Info("created bead for wanted item",
			"wanted_id", item.ID, "bead_id", beadID,
			"title", item.Title)
	}

	// Also check for status changes on tracked items (e.g., claimed by someone else).
	p.syncStatusChanges(ctx)

	if created > 0 || p.cfg.Logger.Enabled(ctx, slog.LevelDebug) {
		p.cfg.Logger.Info("wasteland poll complete",
			"found", len(items), "created", created, "skipped", skipped)
	}
}

// createBeadFromWanted creates a task bead from a wasteland wanted item.
func (p *WastelandPoller) createBeadFromWanted(ctx context.Context, item WastelandItem) (string, error) {
	labels := []string{
		"source:wasteland",
		"wasteland:" + item.ID,
	}
	if item.Project != "" {
		labels = append(labels, "project:"+item.Project)
	}
	for _, tag := range item.Tags {
		labels = append(labels, "wasteland-tag:"+tag)
	}

	fields := map[string]string{
		"wanted_id":        item.ID,
		"wasteland_status": item.Status,
	}
	if item.PostedBy != "" {
		fields["wasteland_posted_by"] = item.PostedBy
	}
	if item.Type != "" {
		fields["wasteland_type"] = item.Type
	}
	if item.EffortLevel != "" {
		fields["wasteland_effort"] = item.EffortLevel
	}

	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("marshal fields: %w", err)
	}

	// Map wasteland priority to bead priority (both use 0-4 scale).
	priority := item.Priority
	if priority < 0 {
		priority = 2
	}
	if priority > 4 {
		priority = 4
	}

	title := fmt.Sprintf("[%s] %s", item.ID, item.Title)

	beadID, err := p.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       title,
		Type:        "task",
		Kind:        "issue",
		Description: item.Description,
		Labels:      labels,
		Priority:    priority,
		CreatedBy:   "wl-bridge",
		Fields:      fieldsJSON,
	})
	if err != nil {
		return "", fmt.Errorf("create bead: %w", err)
	}

	return beadID, nil
}

// syncStatusChanges checks tracked items for status changes (e.g., claimed
// by someone else in the wasteland) and updates bead fields accordingly.
func (p *WastelandPoller) syncStatusChanges(ctx context.Context) {
	// Query all non-open items to detect status changes.
	for _, status := range []string{"claimed", "in_review"} {
		items, err := p.dolt.QueryWanted(ctx, status)
		if err != nil {
			continue
		}

		p.mu.Lock()
		trackedSnapshot := make(map[string]string, len(p.tracked))
		for k, v := range p.tracked {
			trackedSnapshot[k] = v
		}
		p.mu.Unlock()

		for _, item := range items {
			beadID, ok := trackedSnapshot[item.ID]
			if !ok {
				continue
			}
			fields := map[string]string{
				"wasteland_status": item.Status,
			}
			if item.ClaimedBy != "" {
				fields["wasteland_claimed_by"] = item.ClaimedBy
			}
			if err := p.daemon.UpdateBeadFields(ctx, beadID, fields); err != nil {
				p.cfg.Logger.Warn("failed to sync wasteland status change",
					"wanted_id", item.ID, "bead_id", beadID, "error", err)
			}
		}
	}
}

// TrackedCount returns the number of tracked wasteland items.
func (p *WastelandPoller) TrackedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tracked)
}

// IsTracked returns true if the wanted ID is already tracked.
func (p *WastelandPoller) IsTracked(wantedID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.tracked[wantedID]
	return ok
}

// wantedIDFromBead extracts the wanted ID from a bead's fields or labels.
func wantedIDFromBead(bead BeadEvent) string {
	if wid := bead.Fields["wanted_id"]; wid != "" {
		return wid
	}
	for _, label := range bead.Labels {
		if strings.HasPrefix(label, "wasteland:") {
			return strings.TrimPrefix(label, "wasteland:")
		}
	}
	return ""
}
