// Package poolmanager maintains per-project pools of prewarmed agent pods
// ready for instant assignment. It reads pool configuration from the project
// cache (populated from project bead "prewarmed_pool" JSON fields) and
// reconciles each project's pool independently.
package poolmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/config"
)

// Manager maintains prewarmed agent pools across multiple projects.
// Each project with a prewarmed_pool config gets its own independent pool.
type Manager struct {
	daemon *beadsapi.Client
	cfg    *config.Config
	logger *slog.Logger
	mu     sync.Mutex
	seq    int // monotonic counter for unique agent names
}

// New creates a multi-pool Manager.
func New(daemon *beadsapi.Client, cfg *config.Config, logger *slog.Logger) *Manager {
	return &Manager{
		daemon: daemon,
		cfg:    cfg,
		logger: logger,
	}
}

// prewarmedAgent represents a prewarmed agent bead from the daemon.
type prewarmedAgent struct {
	ID        string
	AgentName string
	Project   string
	CreatedAt time.Time
}

// Reconcile performs a single reconciliation pass across all projects.
// For each project with a prewarmed_pool config:
// 1. List prewarmed agents for that project
// 2. Create new agents to reach min_pool_size
func (m *Manager) Reconcile(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Snapshot pool configs from the project cache under read lock.
	m.cfg.ProjectCacheMu.RLock()
	type projectPool struct {
		name string
		cfg  beadsapi.PrewarmedPoolConfig
	}
	var pools []projectPool
	for name, entry := range m.cfg.ProjectCache {
		if entry.PrewarmedPool != nil && entry.PrewarmedPool.Enabled {
			pools = append(pools, projectPool{name: name, cfg: *entry.PrewarmedPool})
		}
	}
	m.cfg.ProjectCacheMu.RUnlock()

	// List all prewarmed agents once (shared across projects).
	allAgents, err := m.listPrewarmedAgents(ctx)
	if err != nil {
		return fmt.Errorf("listing prewarmed agents: %w", err)
	}

	// Group by project.
	byProject := make(map[string][]prewarmedAgent)
	for _, a := range allAgents {
		byProject[a.Project] = append(byProject[a.Project], a)
	}

	// Build set of enabled project names for fast lookup.
	enabledProjects := make(map[string]bool, len(pools))
	for _, pp := range pools {
		enabledProjects[pp.name] = true
	}

	// Close prewarmed agents for projects whose pool is now disabled.
	// This handles the enabled→disabled transition: any prewarmed agents
	// lingering for a project with no active pool config get closed so
	// the reconciler deletes their pods.
	for project, agents := range byProject {
		if enabledProjects[project] {
			continue
		}
		m.logger.Info("pool disabled for project, closing prewarmed agents",
			"project", project, "count", len(agents))
		for _, a := range agents {
			if err := m.closePrewarmedAgent(ctx, a.ID); err != nil {
				m.logger.Warn("failed to close prewarmed agent",
					"bead", a.ID, "project", project, "error", err)
			}
		}
	}

	if len(pools) == 0 {
		return nil
	}

	// Reconcile each project pool: scale up to min, cap at max.
	for _, pp := range pools {
		active := byProject[pp.name]

		// Enforce max_size: close excess agents if pool was shrunk.
		if len(active) > pp.cfg.MaxSize {
			excess := len(active) - pp.cfg.MaxSize
			m.logger.Info("pool exceeds max_size, closing excess agents",
				"project", pp.name, "current", len(active),
				"max", pp.cfg.MaxSize, "closing", excess)
			// Close oldest first (FIFO order).
			sortByAge(active)
			for i := 0; i < excess; i++ {
				if err := m.closePrewarmedAgent(ctx, active[i].ID); err != nil {
					m.logger.Warn("failed to close excess agent",
						"bead", active[i].ID, "project", pp.name, "error", err)
				}
			}
			active = active[excess:]
		}

		deficit := pp.cfg.MinSize - len(active)
		if deficit <= 0 {
			continue
		}
		// Cap creation to not exceed max size.
		if len(active)+deficit > pp.cfg.MaxSize {
			deficit = pp.cfg.MaxSize - len(active)
		}
		if deficit <= 0 {
			continue
		}

		m.logger.Info("pool below minimum, creating prewarmed agents",
			"project", pp.name, "current", len(active),
			"min", pp.cfg.MinSize, "creating", deficit)

		for i := 0; i < deficit; i++ {
			if err := m.createPrewarmedAgent(ctx, pp.name, pp.cfg); err != nil {
				m.logger.Warn("failed to create prewarmed agent",
					"project", pp.name, "error", err)
				break
			}
		}
	}

	return nil
}

// listPrewarmedAgents returns all active agent beads with agent_state=prewarmed.
func (m *Manager) listPrewarmedAgents(ctx context.Context) ([]prewarmedAgent, error) {
	beads, err := m.daemon.ListAgentBeads(ctx)
	if err != nil {
		return nil, err
	}

	var result []prewarmedAgent
	for _, b := range beads {
		if b.AgentState != "prewarmed" {
			continue
		}
		result = append(result, prewarmedAgent{
			ID:        b.ID,
			AgentName: b.AgentName,
			Project:   b.Project,
			CreatedAt: parseTime(b.Metadata["created_at"]),
		})
	}

	return result, nil
}

// createPrewarmedAgent creates a new agent bead in prewarmed state for a project.
func (m *Manager) createPrewarmedAgent(ctx context.Context, project string, poolCfg beadsapi.PrewarmedPoolConfig) error {
	m.seq++
	agentName := fmt.Sprintf("prewarmed-%d-%d", time.Now().Unix(), m.seq)

	fields := map[string]string{
		"agent":       agentName,
		"mode":        poolCfg.Mode,
		"role":        poolCfg.Role,
		"project":     project,
		"agent_state": "prewarmed",
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshalling agent fields: %w", err)
	}

	labels := []string{"prewarmed", "project:" + project}

	beadID, err := m.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       agentName,
		Type:        "agent",
		Description: "Prewarmed agent ready for assignment",
		Fields:      json.RawMessage(fieldsJSON),
		Labels:      labels,
	})
	if err != nil {
		return fmt.Errorf("creating prewarmed agent bead: %w", err)
	}

	// Add role label for advice matching.
	_ = m.daemon.AddLabel(ctx, beadID, "role:"+poolCfg.Role)

	m.logger.Info("created prewarmed agent",
		"bead", beadID, "agent", agentName,
		"project", project, "role", poolCfg.Role)
	return nil
}

// closePrewarmedAgent closes a prewarmed agent bead so the reconciler
// deletes its pod.
func (m *Manager) closePrewarmedAgent(ctx context.Context, beadID string) error {
	status := "closed"
	if err := m.daemon.UpdateBead(ctx, beadID, beadsapi.UpdateBeadRequest{
		Status: &status,
	}); err != nil {
		return fmt.Errorf("closing prewarmed agent bead %s: %w", beadID, err)
	}
	m.logger.Info("closed prewarmed agent", "bead", beadID)
	return nil
}

// sortByAge sorts prewarmed agents by creation time (oldest first).
func sortByAge(agents []prewarmedAgent) {
	for i := 1; i < len(agents); i++ {
		for j := i; j > 0 && agents[j].CreatedAt.Before(agents[j-1].CreatedAt); j-- {
			agents[j], agents[j-1] = agents[j-1], agents[j]
		}
	}
}

// AssignRequest holds the parameters for assigning a prewarmed agent.
type AssignRequest struct {
	// Thread context for the assigned work.
	Channel     string `json:"channel"`
	ThreadTS    string `json:"thread_ts"`
	Description string `json:"description"`
	Project     string `json:"project"`
	// TaskID is an optional pre-assigned task bead ID. When set, it is written
	// to the agent bead's task_id field so the entrypoint can hydrate
	// BOAT_TASK_ID after leaving standby mode.
	TaskID string `json:"task_id,omitempty"`
}

// AssignResult is returned when a prewarmed agent is successfully assigned.
type AssignResult struct {
	BeadID    string `json:"bead_id"`
	AgentName string `json:"agent_name"`
}

// ErrPoolEmpty is returned when no prewarmed agents are available for assignment.
var ErrPoolEmpty = fmt.Errorf("no prewarmed agents available")

// AssignPrewarmed atomically picks a prewarmed agent from the pool and
// transitions it to "assigning" state with the given thread context.
// Returns ErrPoolEmpty if no prewarmed agents are available for the project.
func (m *Manager) AssignPrewarmed(ctx context.Context, req AssignRequest) (*AssignResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	agents, err := m.listPrewarmedAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing prewarmed agents: %w", err)
	}

	// Filter to requested project (if specified).
	var candidates []prewarmedAgent
	for _, a := range agents {
		if req.Project != "" && a.Project != req.Project {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return nil, ErrPoolEmpty
	}

	// Pick the oldest prewarmed agent (FIFO).
	pick := candidates[0]
	for _, a := range candidates[1:] {
		if !a.CreatedAt.IsZero() && (pick.CreatedAt.IsZero() || a.CreatedAt.Before(pick.CreatedAt)) {
			pick = a
		}
	}

	// Update the bead: set agent_state to "assigning" and add thread context.
	fields := map[string]string{
		"agent_state":  "assigning",
		"spawn_source": "slack-thread-pool",
	}
	if req.Channel != "" {
		fields["slack_thread_channel"] = req.Channel
	}
	if req.ThreadTS != "" {
		fields["slack_thread_ts"] = req.ThreadTS
	}
	if req.Project != "" {
		fields["project"] = req.Project
	}
	if req.TaskID != "" {
		fields["task_id"] = req.TaskID
	}
	if err := m.daemon.UpdateBeadFields(ctx, pick.ID, fields); err != nil {
		return nil, fmt.Errorf("updating prewarmed agent %s: %w", pick.ID, err)
	}

	// Update the bead description with thread context.
	if req.Description != "" {
		desc := req.Description
		if err := m.daemon.UpdateBead(ctx, pick.ID, beadsapi.UpdateBeadRequest{
			Description: &desc,
		}); err != nil {
			m.logger.Warn("failed to set description on assigned agent",
				"agent", pick.ID, "error", err)
		}
	}

	m.logger.Info("assigned prewarmed agent",
		"bead", pick.ID, "agent", pick.AgentName,
		"channel", req.Channel, "thread_ts", req.ThreadTS)

	// Nudge the agent's Claude session with the work description.
	// The coop_url is stored in the bead notes by the status reporter.
	go m.nudgeAssignedAgent(pick.ID, req.Description)

	return &AssignResult{
		BeadID:    pick.ID,
		AgentName: pick.AgentName,
	}, nil
}

// HasEnabledPools returns true if any project in the cache has a prewarmed pool enabled.
func (m *Manager) HasEnabledPools() bool {
	m.cfg.ProjectCacheMu.RLock()
	defer m.cfg.ProjectCacheMu.RUnlock()
	for _, entry := range m.cfg.ProjectCache {
		if entry.PrewarmedPool != nil && entry.PrewarmedPool.Enabled {
			return true
		}
	}
	return false
}

// RunLoop runs the multi-pool reconciler periodically until the context is cancelled.
func (m *Manager) RunLoop(ctx context.Context) {
	interval := m.cfg.PrewarmedPoolInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	m.logger.Info("multi-pool manager started", "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately on start.
	if err := m.Reconcile(ctx); err != nil {
		m.logger.Warn("initial pool reconcile failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("multi-pool manager stopped")
			return
		case <-ticker.C:
			if err := m.Reconcile(ctx); err != nil {
				m.logger.Warn("pool reconcile failed", "error", err)
			}
		}
	}
}

// nudgeAssignedAgent sends the work description to the agent's Claude session
// via the coop nudge API. This runs in a goroutine with retries because the
// coop_url may not be in the bead notes immediately after pod creation.
func (m *Manager) nudgeAssignedAgent(beadID, description string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	msg := "You have been assigned work from the prewarmed pool."
	if description != "" {
		msg += "\n\n" + description
	}

	// Retry loop: the coop_url is written to bead notes by the status reporter
	// once the pod is running. It may take a few seconds after pod start.
	for attempt := 0; attempt < 12; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}

		coopURL, err := m.getCoopURL(ctx, beadID)
		if err != nil || coopURL == "" {
			m.logger.Debug("coop_url not yet available for nudge",
				"bead", beadID, "attempt", attempt+1)
			continue
		}

		if err := m.sendNudge(ctx, coopURL, msg); err != nil {
			m.logger.Warn("nudge failed, will retry",
				"bead", beadID, "error", err, "attempt", attempt+1)
			continue
		}

		m.logger.Info("nudged assigned agent with work description", "bead", beadID)
		return
	}

	m.logger.Warn("exhausted nudge retries for assigned agent", "bead", beadID)
}

// getCoopURL extracts the coop_url from a bead's notes field.
// Notes format: "coop_url: http://10.0.0.5:8080" (one per line).
func (m *Manager) getCoopURL(ctx context.Context, beadID string) (string, error) {
	bead, err := m.daemon.GetBead(ctx, beadID)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(bead.Notes, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "coop_url: ") {
			return strings.TrimPrefix(line, "coop_url: "), nil
		}
	}
	return "", nil
}

// sendNudge posts a nudge message to a coop agent's API.
func (m *Manager) sendNudge(ctx context.Context, coopURL, message string) error {
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return fmt.Errorf("marshal nudge: %w", err)
	}

	url := strings.TrimRight(coopURL, "/") + "/api/v1/agent/nudge"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("nudge returned status %d", resp.StatusCode)
	}
	return nil
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
