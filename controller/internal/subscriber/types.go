// Package subscriber watches for agent lifecycle events and emits them on a
// channel. The controller's main loop reads these events and translates them
// to K8s pod operations.
package subscriber

import (
	"context"
	"strings"
)

// EventType identifies the kind of beads lifecycle event.
type EventType string

const (
	// AgentSpawn means a new agent needs a pod (agent bead created).
	AgentSpawn EventType = "agent_spawn"

	// AgentDone means an agent completed its work (bot done, bead closed).
	AgentDone EventType = "agent_done"

	// AgentStuck means an agent is unresponsive (escalation).
	AgentStuck EventType = "agent_stuck"

	// AgentKill means an agent should be terminated (lifecycle shutdown).
	AgentKill EventType = "agent_kill"

	// AgentUpdate means agent bead metadata was changed (e.g., sidecar profile).
	AgentUpdate EventType = "agent_update"

	// AgentStop means an agent should be gracefully stopped (agent_state=stopping).
	AgentStop EventType = "agent_stop"
)

// Event represents a beads lifecycle event that requires a pod operation.
type Event struct {
	Type      EventType
	Project   string
	Mode      string
	Role      string // functional role from role bead (e.g., "devops", "qa")
	AgentName string
	BeadID    string            // The bead that triggered this event
	Metadata  map[string]string // Additional context from beads
}

// Watcher subscribes to BD Daemon lifecycle events and emits them on a channel.
type Watcher interface {
	// Start begins watching for beads events. Blocks until ctx is canceled.
	Start(ctx context.Context) error

	// Events returns a read-only channel of lifecycle events.
	Events() <-chan Event
}

// beadEventPayload is the event structure published by the beads daemon.
// The daemon publishes to subjects like beads.bead.created, beads.bead.updated, etc.
type beadEventPayload struct {
	Bead    beadData       `json:"bead"`
	Changes map[string]any `json:"changes,omitempty"`
}

// beadData mirrors the bead fields from the daemon's event payload.
type beadData struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Type       string            `json:"type"`
	Status     string            `json:"status"`
	Labels     []string          `json:"labels"`
	Fields     map[string]string `json:"fields"`
	AgentState string            `json:"agent_state"`
	Assignee   string            `json:"assignee"`
	CreatedBy  string            `json:"created_by"`
}

// subjectAction extracts the action from a NATS-style dotted subject.
// e.g., "beads.bead.created" -> "created"
func subjectAction(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}
