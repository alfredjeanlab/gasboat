// Package bridge provides the live agent activity dashboard.
//
// Dashboard posts and periodically updates a single pinned Slack message
// showing agent roster status: working, idle, and dead agents, plus pending
// decisions. Content hashing prevents redundant Slack API calls when nothing
// has changed. The dashboard message is pinned so it survives pod restarts
// (the pin is used as a recovery mechanism when local state is lost).
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// DashboardConfig holds configuration for the agent activity dashboard.
type DashboardConfig struct {
	Enabled   bool
	ChannelID string        // Target channel (falls back to bot default).
	Interval  time.Duration // Poll interval (default 15s).

	MaxWorkingShown   int // Max working agents to display (default 10).
	MaxIdleShown      int // Max idle agents to display (default 5).
	MaxDeadShown      int // Max dead agents to display (default 5).
	MaxDecisionsShown int // Max pending decisions to display (default 5).
}

// dashboardMessage tracks the posted dashboard message for later updates.
type dashboardMessage struct {
	ChannelID string
	Timestamp string
	LastHash  string
}

// dashboardMarker is embedded in every dashboard message so we can identify
// our pinned message after a pod restart when local state is lost.
const dashboardMarker = ":factory: *Agent Dashboard*"

// Dashboard manages the live agent activity dashboard: a single pinned Slack
// message that is periodically updated with the current agent roster.
type Dashboard struct {
	api    *slack.Client
	daemon *beadsapi.Client
	state  *StateManager
	logger *slog.Logger
	cfg    DashboardConfig

	mu          sync.Mutex
	msg         *dashboardMessage
	lastUpdate  time.Time
	dirty       bool
	minInterval time.Duration
}

// NewDashboard creates a Dashboard that posts and updates a roster message.
func NewDashboard(api *slack.Client, daemon *beadsapi.Client, state *StateManager, logger *slog.Logger, cfg DashboardConfig) *Dashboard {
	if cfg.ChannelID == "" {
		logger.Warn("dashboard: no channel configured, disabled")
		cfg.Enabled = false
	}
	return &Dashboard{
		api:         api,
		daemon:      daemon,
		state:       state,
		logger:      logger,
		cfg:         cfg,
		minInterval: 3 * time.Second,
	}
}

// Run starts the periodic dashboard update loop. Blocks until ctx is cancelled.
func (d *Dashboard) Run(ctx context.Context) {
	if !d.cfg.Enabled {
		return
	}
	interval := d.cfg.Interval
	if interval == 0 {
		interval = 15 * time.Second
	}

	// Jitter on startup to avoid thundering herd.
	jitter := time.Duration(rand.IntN(5000)) * time.Millisecond
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	d.initMessage(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refresh(ctx)
		}
	}
}

// MarkDirty signals that the dashboard should refresh on the next cycle.
func (d *Dashboard) MarkDirty() {
	d.mu.Lock()
	d.dirty = true
	d.mu.Unlock()
}

// RegisterHandlers registers SSE event handlers to mark the dashboard dirty
// on agent lifecycle events (create/close/update of agent beads).
func (d *Dashboard) RegisterHandlers(stream *SSEStream) {
	handler := func(_ context.Context, data []byte) {
		bead := ParseBeadEvent(data)
		if bead == nil {
			return
		}
		if bead.Type == "agent" || bead.Type == "decision" {
			d.MarkDirty()
		}
	}
	stream.On("beads.bead.created", handler)
	stream.On("beads.bead.closed", handler)
	stream.On("beads.bead.updated", handler)
	d.logger.Info("dashboard registered SSE handlers")
}

// --- Rendering ---

// buildBlocks fetches the current roster + decisions and renders Block Kit blocks.
// Returns the blocks and a content hash for change detection.
func (d *Dashboard) buildBlocks(ctx context.Context) ([]slack.Block, string, error) {
	agents, err := d.daemon.ListAgentBeads(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("listing agents: %w", err)
	}
	decisions, err := d.daemon.ListDecisionBeads(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("listing decisions: %w", err)
	}

	blocks, hash := d.renderBlocks(agents, decisions)
	return blocks, hash, nil
}

func (d *Dashboard) renderBlocks(agents []beadsapi.AgentBead, decisions []*beadsapi.BeadDetail) ([]slack.Block, string) {
	cfg := d.cfg
	if cfg.MaxWorkingShown == 0 {
		cfg.MaxWorkingShown = 10
	}
	if cfg.MaxIdleShown == 0 {
		cfg.MaxIdleShown = 5
	}
	if cfg.MaxDeadShown == 0 {
		cfg.MaxDeadShown = 5
	}
	if cfg.MaxDecisionsShown == 0 {
		cfg.MaxDecisionsShown = 5
	}

	// Build a pending-decision count per agent bead ID.
	pendingByAgent := make(map[string]int, len(decisions))
	for _, dec := range decisions {
		if agentID := dec.Fields["requesting_agent_bead_id"]; agentID != "" {
			pendingByAgent[agentID]++
		}
	}

	// Classify agents into four buckets.
	var working, starting, idle, dead []beadsapi.AgentBead
	for _, a := range agents {
		switch {
		case a.AgentState == "failed" || a.PodPhase == "failed":
			dead = append(dead, a)
		case a.AgentState == "working":
			working = append(working, a)
		case a.AgentState == "spawning":
			starting = append(starting, a)
		default:
			idle = append(idle, a)
		}
	}

	// Sort: working by project/name, others by name.
	sort.Slice(working, func(i, j int) bool {
		if working[i].Project != working[j].Project {
			return working[i].Project < working[j].Project
		}
		return working[i].AgentName < working[j].AgentName
	})
	sort.Slice(starting, func(i, j int) bool { return starting[i].AgentName < starting[j].AgentName })
	sort.Slice(idle, func(i, j int) bool { return idle[i].AgentName < idle[j].AgentName })
	sort.Slice(dead, func(i, j int) bool { return dead[i].AgentName < dead[j].AgentName })

	var blocks []slack.Block

	// Header.
	headerText := fmt.Sprintf("%s · %d agents · Updated %s",
		dashboardMarker, len(agents), time.Now().UTC().Format("15:04 UTC"))
	blocks = append(blocks, slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", headerText, false, false), nil, nil))

	// Summary counters.
	summaryText := fmt.Sprintf(":large_green_circle: %d working  ·  :white_circle: %d idle  ·  :red_circle: %d dead",
		len(working), len(idle), len(dead))
	if len(starting) > 0 {
		summaryText += fmt.Sprintf("  ·  :hourglass_flowing_sand: %d starting", len(starting))
	}
	if len(decisions) > 0 {
		summaryText += fmt.Sprintf("  ·  :large_blue_circle: %d pending decisions", len(decisions))
	}
	blocks = append(blocks, slack.NewContextBlock("",
		slack.NewTextBlockObject("mrkdwn", summaryText, false, false)))

	// Working section.
	if len(working) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Working (%d)*", len(working)), false, false)))

		shown := working
		if len(shown) > cfg.MaxWorkingShown {
			shown = shown[:cfg.MaxWorkingShown]
		}
		for _, a := range shown {
			blocks = append(blocks, dashboardAgentWorkingBlock(a))
		}
		if overflow := len(working) - cfg.MaxWorkingShown; overflow > 0 {
			blocks = append(blocks, slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_+%d more working agents_", overflow), false, false)))
		}
	}

	// Starting section.
	if len(starting) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Starting (%d)*", len(starting)), false, false)))

		shown := starting
		if len(shown) > cfg.MaxIdleShown {
			shown = shown[:cfg.MaxIdleShown]
		}
		for _, a := range shown {
			blocks = append(blocks, dashboardAgentStartingBlock(a))
		}
		if overflow := len(starting) - cfg.MaxIdleShown; overflow > 0 {
			blocks = append(blocks, slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_+%d more starting agents_", overflow), false, false)))
		}
	}

	// Idle section.
	if len(idle) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Idle (%d)*", len(idle)), false, false)))

		shown := idle
		if len(shown) > cfg.MaxIdleShown {
			shown = shown[:cfg.MaxIdleShown]
		}
		for _, a := range shown {
			blocks = append(blocks, dashboardAgentIdleBlock(a, pendingByAgent[a.ID]))
		}
		if overflow := len(idle) - cfg.MaxIdleShown; overflow > 0 {
			blocks = append(blocks, slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_+%d more idle agents_", overflow), false, false)))
		}
	}

	// Dead section.
	if len(dead) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Dead (%d)*", len(dead)), false, false)))

		shown := dead
		if len(shown) > cfg.MaxDeadShown {
			shown = shown[:cfg.MaxDeadShown]
		}
		for _, a := range shown {
			blocks = append(blocks, dashboardAgentDeadBlock(a))
		}
		if overflow := len(dead) - cfg.MaxDeadShown; overflow > 0 {
			blocks = append(blocks, slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_+%d more dead agents_", overflow), false, false)))
		}
	}

	// Pending decisions section.
	if len(decisions) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("*Pending Decisions (%d)*", len(decisions)), false, false)))

		shown := decisions
		if len(shown) > cfg.MaxDecisionsShown {
			shown = shown[:cfg.MaxDecisionsShown]
		}
		var lines []string
		for _, dec := range shown {
			question := dec.Fields["question"]
			if question == "" {
				question = dec.Title
			}
			if len(question) > 60 {
				question = question[:57] + "..."
			}
			urgency := ":white_circle:"
			for _, label := range dec.Labels {
				if label == "escalated" {
					urgency = ":rotating_light:"
					break
				}
			}
			line := fmt.Sprintf("%s `%s` %s", urgency, dec.ID, question)
			if dec.Assignee != "" {
				line = fmt.Sprintf("%s `%s` · *%s* · %s", urgency, dec.ID, extractAgentName(dec.Assignee), question)
			}
			lines = append(lines, line)
		}
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", strings.Join(lines, "\n"), false, false), nil, nil))

		if overflow := len(decisions) - cfg.MaxDecisionsShown; overflow > 0 {
			blocks = append(blocks, slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_+%d more pending decisions_", overflow), false, false)))
		}
	}

	hash := buildDashboardHash(agents, decisions)
	return blocks, hash
}

func dashboardAgentWorkingBlock(a beadsapi.AgentBead) slack.Block {
	line := fmt.Sprintf(":large_green_circle: *%s*", a.AgentName)
	if a.Project != "" {
		line += fmt.Sprintf(" · %s", a.Project)
	}
	if a.Role != "" {
		line += fmt.Sprintf(" (%s/%s)", a.Mode, a.Role)
	}
	return slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", line, false, false), nil, nil)
}

func dashboardAgentStartingBlock(a beadsapi.AgentBead) slack.Block {
	line := fmt.Sprintf(":hourglass_flowing_sand: *%s*", a.AgentName)
	if a.Project != "" {
		line += fmt.Sprintf(" · %s", a.Project)
	}
	return slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", line, false, false), nil, nil)
}

func dashboardAgentIdleBlock(a beadsapi.AgentBead, pendingCount int) slack.Block {
	var indicator, suffix string
	if pendingCount > 0 {
		indicator = ":large_blue_circle:"
		suffix = fmt.Sprintf(" · %d pending", pendingCount)
	} else {
		indicator = ":white_circle:"
	}
	line := fmt.Sprintf("%s *%s*", indicator, a.AgentName)
	if a.Project != "" {
		line += fmt.Sprintf(" · %s", a.Project)
	}
	line += suffix
	return slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", line, false, false), nil, nil)
}

func dashboardAgentDeadBlock(a beadsapi.AgentBead) slack.Block {
	line := fmt.Sprintf(":red_circle: *%s*", a.AgentName)
	if a.Project != "" {
		line += fmt.Sprintf(" · %s", a.Project)
	}
	state := a.AgentState
	if state == "" {
		state = a.PodPhase
	}
	line += fmt.Sprintf(" · %s", state)
	return slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", line, false, false), nil, nil)
}

// buildDashboardHash produces a content hash for change detection.
func buildDashboardHash(agents []beadsapi.AgentBead, decisions []*beadsapi.BeadDetail) string {
	// Build pending-decision count per agent for hash inclusion.
	pendingByAgent := make(map[string]int, len(decisions))
	for _, dec := range decisions {
		if agentID := dec.Fields["requesting_agent_bead_id"]; agentID != "" {
			pendingByAgent[agentID]++
		}
	}

	var parts []string
	for _, a := range agents {
		var state string
		switch {
		case a.AgentState == "failed" || a.PodPhase == "failed":
			state = "dead"
		case a.AgentState == "working":
			state = "working"
		case a.AgentState == "spawning":
			state = "starting"
		case pendingByAgent[a.ID] > 0:
			state = fmt.Sprintf("decision:%d", pendingByAgent[a.ID])
		default:
			state = "idle"
		}
		parts = append(parts, fmt.Sprintf("%s:%s:%s", a.AgentName, state, a.Project))
	}
	sort.Strings(parts)
	for _, d := range decisions {
		parts = append(parts, fmt.Sprintf("dec:%s", d.ID))
	}
	return strings.Join(parts, "|")
}

// --- Message Lifecycle ---

// initMessage restores a persisted dashboard message, finds a pinned one, or posts new.
func (d *Dashboard) initMessage(ctx context.Context) {
	// Try restore from state file.
	if d.state != nil {
		if ref, ok := d.state.GetDashboard(); ok && ref.ChannelID != "" && ref.Timestamp != "" {
			blocks, hash, err := d.buildBlocks(ctx)
			if err != nil {
				d.logger.Error("dashboard: init build blocks failed", "error", err)
			} else {
				_, _, _, err = d.api.UpdateMessageContext(ctx, ref.ChannelID, ref.Timestamp,
					slack.MsgOptionBlocks(blocks...))
				if err == nil {
					d.mu.Lock()
					d.msg = &dashboardMessage{ChannelID: ref.ChannelID, Timestamp: ref.Timestamp, LastHash: hash}
					d.lastUpdate = time.Now()
					d.mu.Unlock()
					d.logger.Info("dashboard: restored from state", "channel", ref.ChannelID, "ts", ref.Timestamp)
					return
				}
				d.logger.Warn("dashboard: persisted message gone, checking pins", "error", err)
			}
		}
	}

	// Look for a pinned dashboard message.
	if ts := d.findPinnedDashboard(); ts != "" {
		blocks, hash, err := d.buildBlocks(ctx)
		if err != nil {
			d.logger.Error("dashboard: init build blocks (pin recovery) failed", "error", err)
		} else {
			_, _, _, err = d.api.UpdateMessageContext(ctx, d.cfg.ChannelID, ts,
				slack.MsgOptionBlocks(blocks...))
			if err == nil {
				d.mu.Lock()
				d.msg = &dashboardMessage{ChannelID: d.cfg.ChannelID, Timestamp: ts, LastHash: hash}
				d.lastUpdate = time.Now()
				d.mu.Unlock()
				if d.state != nil {
					_ = d.state.SetDashboard(DashboardRef{ChannelID: d.cfg.ChannelID, Timestamp: ts, LastHash: hash})
				}
				d.logger.Info("dashboard: recovered pinned message", "channel", d.cfg.ChannelID, "ts", ts)
				return
			}
			d.logger.Warn("dashboard: pinned message update failed, posting new", "error", err)
		}
	}

	d.postNew(ctx)
}

func (d *Dashboard) findPinnedDashboard() string {
	items, _, err := d.api.ListPins(d.cfg.ChannelID)
	if err != nil {
		d.logger.Warn("dashboard: list pins failed", "error", err)
		return ""
	}
	for _, item := range items {
		if item.Message == nil {
			continue
		}
		if strings.Contains(item.Message.Text, dashboardMarker) {
			d.logger.Debug("dashboard: found pinned message", "ts", item.Message.Timestamp)
			return item.Message.Timestamp
		}
	}
	return ""
}

func (d *Dashboard) postNew(ctx context.Context) {
	// Clean up old message.
	d.mu.Lock()
	oldMsg := d.msg
	d.mu.Unlock()
	d.cleanupOldMessage(oldMsg)

	blocks, hash, err := d.buildBlocks(ctx)
	if err != nil {
		d.logger.Error("dashboard: build blocks failed", "error", err)
		return
	}

	chID, ts, err := d.api.PostMessageContext(ctx, d.cfg.ChannelID, slack.MsgOptionBlocks(blocks...))
	if err != nil {
		d.logger.Error("dashboard: post message failed", "error", err)
		return
	}

	// Pin the message.
	if pinErr := d.api.AddPin(chID, slack.NewRefToMessage(chID, ts)); pinErr != nil {
		if !strings.Contains(pinErr.Error(), "already_pinned") {
			d.logger.Warn("dashboard: pin failed", "error", pinErr)
		}
	}

	d.mu.Lock()
	d.msg = &dashboardMessage{ChannelID: chID, Timestamp: ts, LastHash: hash}
	d.lastUpdate = time.Now()
	d.mu.Unlock()

	if d.state != nil {
		_ = d.state.SetDashboard(DashboardRef{ChannelID: chID, Timestamp: ts, LastHash: hash})
	}

	d.logger.Info("dashboard: posted new message", "channel", chID, "ts", ts)
}

func (d *Dashboard) cleanupOldMessage(old *dashboardMessage) {
	if old == nil || old.ChannelID == "" || old.Timestamp == "" {
		return
	}
	ref := slack.NewRefToMessage(old.ChannelID, old.Timestamp)
	if err := d.api.RemovePin(old.ChannelID, ref); err != nil {
		if !strings.Contains(err.Error(), "no_pin") {
			d.logger.Warn("dashboard: unpin old message failed", "error", err)
		}
	}
	if _, _, err := d.api.DeleteMessageContext(context.Background(), old.ChannelID, old.Timestamp); err != nil {
		if !strings.Contains(err.Error(), "message_not_found") {
			d.logger.Warn("dashboard: delete old message failed", "error", err)
		}
	}
}

// refresh fetches the current roster and updates the dashboard message if changed.
func (d *Dashboard) refresh(ctx context.Context) {
	d.mu.Lock()
	msg := d.msg
	dirty := d.dirty
	lastUpdate := d.lastUpdate
	d.dirty = false
	d.mu.Unlock()

	if msg == nil {
		d.postNew(ctx)
		return
	}

	// Rate limit: minimum 3s between updates.
	if time.Since(lastUpdate) < d.minInterval {
		if dirty {
			d.mu.Lock()
			d.dirty = true
			d.mu.Unlock()
		}
		return
	}

	blocks, hash, err := d.buildBlocks(ctx)
	if err != nil {
		d.logger.Error("dashboard: refresh build blocks failed", "error", err)
		return
	}

	// Skip if unchanged and updated recently (force refresh every 5 min for timestamp).
	if hash == msg.LastHash && time.Since(lastUpdate) < 5*time.Minute {
		return
	}

	_, _, _, err = d.api.UpdateMessageContext(ctx, msg.ChannelID, msg.Timestamp,
		slack.MsgOptionBlocks(blocks...))
	if err != nil {
		if rateLimitErr, ok := err.(*slack.RateLimitedError); ok {
			d.logger.Warn("dashboard: rate limited", "retry_after", rateLimitErr.RetryAfter)
			d.mu.Lock()
			d.dirty = true
			d.mu.Unlock()
			return
		}
		d.logger.Error("dashboard: update failed", "error", err)
		if strings.Contains(err.Error(), "message_not_found") {
			d.mu.Lock()
			d.msg = nil
			d.mu.Unlock()
		}
		return
	}

	d.mu.Lock()
	d.msg.LastHash = hash
	d.lastUpdate = time.Now()
	d.mu.Unlock()

	if d.state != nil {
		_ = d.state.SetDashboard(DashboardRef{ChannelID: msg.ChannelID, Timestamp: msg.Timestamp, LastHash: hash})
	}
}
