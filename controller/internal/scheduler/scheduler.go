// Package scheduler evaluates cron-based schedule beads and spawns agents
// on matching triggers. It reads schedule beads from the daemon, evaluates
// cron expressions against wall-clock time, and calls SpawnAgent for any
// schedule whose next-fire time has passed since last_run.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// Scheduler evaluates schedule beads and spawns agents on cron triggers.
type Scheduler struct {
	daemon *beadsapi.Client
	logger *slog.Logger
	mu     sync.Mutex
}

// New creates a Scheduler.
func New(daemon *beadsapi.Client, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		daemon: daemon,
		logger: logger,
	}
}

// Schedule represents a parsed schedule bead.
type Schedule struct {
	ID          string
	Title       string
	Cron        string
	Project     string
	Role        string
	Prompt      string
	Enabled     bool
	Timezone    string
	LastRun     time.Time
	LastAgentID string
}

// Reconcile performs a single evaluation pass. For each enabled schedule bead:
//  1. Parse the cron expression
//  2. Calculate the most recent fire time before now
//  3. If that fire time is after last_run, spawn an agent and update last_run
func (s *Scheduler) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.listSchedules(ctx)
	if err != nil {
		return fmt.Errorf("listing schedules: %w", err)
	}

	if len(schedules) == 0 {
		return nil
	}

	now := time.Now()

	for _, sched := range schedules {
		if !sched.Enabled {
			continue
		}
		if sched.Cron == "" {
			s.logger.Warn("schedule has empty cron expression", "id", sched.ID, "title", sched.Title)
			continue
		}

		loc := time.UTC
		if sched.Timezone != "" {
			parsed, err := time.LoadLocation(sched.Timezone)
			if err != nil {
				s.logger.Warn("invalid timezone on schedule, using UTC",
					"id", sched.ID, "timezone", sched.Timezone, "error", err)
			} else {
				loc = parsed
			}
		}

		expr, err := ParseCron(sched.Cron)
		if err != nil {
			s.logger.Warn("invalid cron expression on schedule",
				"id", sched.ID, "cron", sched.Cron, "error", err)
			continue
		}

		nowLocal := now.In(loc)
		lastFire := expr.Prev(nowLocal)
		if lastFire.IsZero() {
			continue
		}

		// If last_run is after or equal to the most recent fire time, skip.
		if !sched.LastRun.IsZero() && !sched.LastRun.Before(lastFire) {
			continue
		}

		s.logger.Info("schedule triggered",
			"id", sched.ID, "title", sched.Title,
			"cron", sched.Cron, "last_fire", lastFire.Format(time.RFC3339))

		agentID, err := s.spawnScheduledAgent(ctx, sched)
		if err != nil {
			s.logger.Error("failed to spawn scheduled agent",
				"schedule", sched.ID, "error", err)
			continue
		}

		// Update last_run and last_agent_id on the schedule bead.
		if err := s.daemon.UpdateBeadFields(ctx, sched.ID, map[string]string{
			"last_run":      now.UTC().Format(time.RFC3339),
			"last_agent_id": agentID,
		}); err != nil {
			s.logger.Warn("failed to update schedule last_run",
				"schedule", sched.ID, "error", err)
		}
	}

	return nil
}

// RunLoop runs the scheduler periodically until the context is cancelled.
func (s *Scheduler) RunLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}

	s.logger.Info("scheduler started", "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately on start.
	if err := s.Reconcile(ctx); err != nil {
		s.logger.Warn("initial schedule reconcile failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			if err := s.Reconcile(ctx); err != nil {
				s.logger.Warn("schedule reconcile failed", "error", err)
			}
		}
	}
}

// listSchedules queries the daemon for active schedule beads.
func (s *Scheduler) listSchedules(ctx context.Context) ([]Schedule, error) {
	result, err := s.daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"schedule"},
		Statuses: []string{"open", "in_progress"},
	})
	if err != nil {
		return nil, err
	}

	var schedules []Schedule
	for _, b := range result.Beads {
		enabled := b.Fields["enabled"] == "true"
		var lastRun time.Time
		if raw := b.Fields["last_run"]; raw != "" {
			for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
				if t, err := time.Parse(layout, raw); err == nil {
					lastRun = t
					break
				}
			}
		}
		schedules = append(schedules, Schedule{
			ID:          b.ID,
			Title:       b.Title,
			Cron:        b.Fields["cron"],
			Project:     b.Fields["project"],
			Role:        b.Fields["role"],
			Prompt:      b.Fields["prompt"],
			Enabled:     enabled,
			Timezone:    b.Fields["timezone"],
			LastRun:     lastRun,
			LastAgentID: b.Fields["last_agent_id"],
		})
	}
	return schedules, nil
}

// spawnScheduledAgent creates a new agent bead for a triggered schedule.
func (s *Scheduler) spawnScheduledAgent(ctx context.Context, sched Schedule) (string, error) {
	project := sched.Project
	if project == "" {
		project = "gasboat"
	}
	role := sched.Role
	if role == "" {
		role = "crew"
	}
	prompt := sched.Prompt
	if prompt == "" {
		prompt = "Scheduled task: " + sched.Title
	}

	agentName := fmt.Sprintf("sched-%d", time.Now().Unix())

	// Create a task bead for the scheduled run.
	taskFields := map[string]string{
		"schedule_id": sched.ID,
	}
	taskFieldsJSON, err := json.Marshal(taskFields)
	if err != nil {
		return "", fmt.Errorf("marshalling task fields: %w", err)
	}
	taskID, err := s.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       fmt.Sprintf("Scheduled: %s", sched.Title),
		Type:        "task",
		Description: prompt,
		Labels:      []string{"project:" + project, "scheduled"},
		Fields:      taskFieldsJSON,
	})
	if err != nil {
		return "", fmt.Errorf("creating scheduled task bead: %w", err)
	}

	// Spawn the agent with the task pre-assigned.
	beadID, err := s.daemon.SpawnAgent(ctx, agentName, project, taskID, role, prompt)
	if err != nil {
		return "", fmt.Errorf("spawning agent for schedule %s: %w", sched.ID, err)
	}

	// Best-effort: label agent as scheduled.
	_ = s.daemon.AddLabel(ctx, beadID, "scheduled")

	s.logger.Info("spawned scheduled agent",
		"schedule", sched.ID, "agent", agentName,
		"bead", beadID, "task", taskID,
		"project", project, "role", role)

	return beadID, nil
}
