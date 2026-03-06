// Package bridge provides the agent lifecycle watcher.
//
// Agents subscribes to kbeads SSE event stream for agent bead lifecycle events
// and posts Slack crash notifications when an agent fails. It deduplicates
// notifications per agent bead ID to avoid spam on SSE reconnect or repeated
// update events.
package bridge

import (
	"context"
	"log/slog"
	"sync"
)

// AgentNotifier posts agent lifecycle notifications to Slack.
type AgentNotifier interface {
	NotifyAgentCrash(ctx context.Context, bead BeadEvent) error
	// NotifyAgentSpawn is called when an agent bead is first created.
	// Implementations should post an initial status card / message.
	NotifyAgentSpawn(ctx context.Context, bead BeadEvent)
	// NotifyAgentState is called whenever an agent bead's agent_state changes.
	// Implementations should update any live status display (e.g. agent card).
	NotifyAgentState(ctx context.Context, bead BeadEvent)
	// NotifyAgentTaskUpdate is called when a non-agent bead assigned to an agent
	// changes to in_progress (i.e., the agent claimed a task). Implementations
	// should refresh the agent's live status display to show the new task.
	NotifyAgentTaskUpdate(ctx context.Context, agentName string)
}

// AgentsConfig holds configuration for the Agents watcher.
type AgentsConfig struct {
	Notifier AgentNotifier // nil = no notifications
	Logger   *slog.Logger
}

// Agents watches the kbeads SSE event stream for agent bead lifecycle events.
type Agents struct {
	notifier AgentNotifier
	logger   *slog.Logger

	mu             sync.Mutex
	seen           map[string]bool   // bead ID → already notified (dedup)
	taskAssignees  map[string]string // task bead ID → last known assignee
}

// NewAgents creates a new agent lifecycle watcher.
func NewAgents(cfg AgentsConfig) *Agents {
	return &Agents{
		notifier:      cfg.Notifier,
		logger:        cfg.Logger,
		seen:          make(map[string]bool),
		taskAssignees: make(map[string]string),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// agent bead created, closed, and updated events.
func (a *Agents) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.created", a.handleCreated)
	stream.On("beads.bead.closed", a.handleClosed)
	stream.On("beads.bead.updated", a.handleUpdated)
	a.logger.Info("agents watcher registered SSE handlers",
		"topics", []string{"beads.bead.created", "beads.bead.closed", "beads.bead.updated"})
}

func (a *Agents) handleCreated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}
	if bead.Type != "agent" {
		return
	}

	a.logger.Info("agent bead created",
		"id", bead.ID, "assignee", bead.Assignee, "title", bead.Title)

	if a.notifier != nil {
		a.notifier.NotifyAgentSpawn(ctx, *bead)
	}
}

func (a *Agents) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}
	if bead.Type != "agent" {
		// For task beads, refresh the agent's card so the completed task is cleared.
		// When the assignee is empty (e.g. unassigned before close), use the
		// last-known assignee so their card still gets refreshed.
		assignee := bead.Assignee
		if assignee == "" {
			a.mu.Lock()
			assignee = a.taskAssignees[bead.ID]
			a.mu.Unlock()
		}
		// Clean up tracking entry — the bead is closed.
		a.mu.Lock()
		delete(a.taskAssignees, bead.ID)
		a.mu.Unlock()

		if assignee != "" && a.notifier != nil {
			a.notifier.NotifyAgentTaskUpdate(ctx, assignee)
		}
		return
	}

	// An agent bead closing with agent_state=failed or pod_phase=failed is a crash.
	agentState := bead.Fields["agent_state"]
	podPhase := bead.Fields["pod_phase"]
	if agentState == "failed" || podPhase == "failed" {
		a.notifyCrash(ctx, *bead)
		// Ensure agent_state is set so the card update below shows "failed".
		if agentState == "" {
			bead.Fields["agent_state"] = "failed"
			agentState = "failed"
		}
	}

	// Update the card so it shows current state (done/failed) with the Clear button.
	if a.notifier != nil {
		if agentState != "done" && agentState != "failed" {
			bead.Fields["agent_state"] = "done"
		}
		a.notifier.NotifyAgentState(ctx, *bead)
	}
}

func (a *Agents) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	if bead.Type != "agent" {
		// For task beads claimed by an agent, refresh the agent's card so it
		// reflects the newly claimed task without waiting for a pod phase change.
		// Also refresh on close in case kbeads emits an update before the closed event.
		if bead.Assignee != "" && a.notifier != nil &&
			(bead.Status == "in_progress" || bead.Status == "closed") {
			a.notifier.NotifyAgentTaskUpdate(ctx, bead.Assignee)
		}

		// When assignee is cleared (unassigned), refresh the previous assignee's
		// card so it no longer shows the stale task title.
		if bead.Assignee == "" && a.notifier != nil {
			a.mu.Lock()
			prev := a.taskAssignees[bead.ID]
			delete(a.taskAssignees, bead.ID)
			a.mu.Unlock()
			if prev != "" {
				a.notifier.NotifyAgentTaskUpdate(ctx, prev)
			}
		}

		// Track the current assignee for future unassignment detection.
		// When reassigned directly (A→B without clearing), also notify the
		// previous assignee so their card drops the stale task title.
		if bead.Assignee != "" {
			a.mu.Lock()
			prev := a.taskAssignees[bead.ID]
			a.taskAssignees[bead.ID] = bead.Assignee
			a.mu.Unlock()
			if prev != "" && prev != bead.Assignee && a.notifier != nil {
				a.notifier.NotifyAgentTaskUpdate(ctx, prev)
			}
		}
		return
	}

	agentState := bead.Fields["agent_state"]
	podPhase := bead.Fields["pod_phase"]

	// Notify crash on agent_state=failed or pod_phase=failed.
	if agentState == "failed" || podPhase == "failed" {
		a.notifyCrash(ctx, *bead)
		// Ensure agent_state is set so the card update below shows "failed".
		if agentState == "" {
			bead.Fields["agent_state"] = "failed"
			agentState = "failed"
		}
	}

	// Notify rate limit so operators know.
	if agentState == "rate_limited" {
		a.notifyRateLimited(ctx, *bead)
	}

	// For any state change, notify so the agent card can be refreshed.
	if agentState != "" && a.notifier != nil {
		a.notifier.NotifyAgentState(ctx, *bead)
	}
}

func (a *Agents) notifyRateLimited(ctx context.Context, bead BeadEvent) {
	// Deduplicate: only notify once per agent bead.
	key := bead.ID + ":rate_limited"
	a.mu.Lock()
	if a.seen[key] {
		a.mu.Unlock()
		return
	}
	a.seen[key] = true
	a.mu.Unlock()

	agent := bead.Assignee
	if agent == "" {
		if n := bead.Fields["agent"]; n != "" {
			agent = n
		}
	}

	a.logger.Warn("agent rate-limited",
		"id", bead.ID,
		"agent", agent,
		"agent_state", bead.Fields["agent_state"])

	if a.notifier != nil {
		a.notifier.NotifyAgentState(ctx, bead)
	}
}

func (a *Agents) notifyCrash(ctx context.Context, bead BeadEvent) {
	// Deduplicate: only notify once per agent bead.
	a.mu.Lock()
	if a.seen[bead.ID] {
		a.mu.Unlock()
		return
	}
	a.seen[bead.ID] = true
	a.mu.Unlock()

	a.logger.Info("agent crash detected",
		"id", bead.ID,
		"title", bead.Title,
		"assignee", bead.Assignee,
		"agent_state", bead.Fields["agent_state"],
		"pod_phase", bead.Fields["pod_phase"])

	if a.notifier != nil {
		if err := a.notifier.NotifyAgentCrash(ctx, bead); err != nil {
			a.logger.Error("failed to notify agent crash",
				"id", bead.ID, "error", err)
		}
	}
}
