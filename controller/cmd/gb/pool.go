package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var poolCmd = &cobra.Command{
	Use:     "pool",
	Short:   "Manage prewarmed agent pools",
	GroupID: "orchestration",
}

var poolProject string

// ── pool status ─────────────────────────────────────────────────────

var poolStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show prewarmed pool state per project",
	RunE:  runPoolStatus,
}

func runPoolStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	projects, err := daemon.ListProjectBeads(ctx)
	if err != nil {
		return fmt.Errorf("listing projects: %w", err)
	}

	// List all prewarmed agents.
	agents, err := daemon.ListAgentBeads(ctx)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}

	// Count prewarmed agents by project.
	prewarmedByProject := make(map[string]int)
	for _, a := range agents {
		if a.AgentState == "prewarmed" {
			prewarmedByProject[a.Project]++
		}
	}

	found := false
	for name, proj := range projects {
		if poolProject != "" && name != poolProject {
			continue
		}

		pool := proj.PrewarmedPool
		enabled := pool != nil && pool.Enabled

		if !enabled && poolProject == "" {
			// Skip disabled projects unless specifically requested.
			if prewarmedByProject[name] == 0 {
				continue
			}
		}

		found = true
		fmt.Fprintf(os.Stdout, "Project: %s\n", name)
		if enabled {
			fmt.Fprintf(os.Stdout, "  Enabled:   true\n")
			fmt.Fprintf(os.Stdout, "  Min size:  %d\n", pool.MinSize)
			fmt.Fprintf(os.Stdout, "  Max size:  %d\n", pool.MaxSize)
			fmt.Fprintf(os.Stdout, "  Role:      %s\n", pool.Role)
			fmt.Fprintf(os.Stdout, "  Mode:      %s\n", pool.Mode)
		} else {
			fmt.Fprintf(os.Stdout, "  Enabled:   false\n")
		}
		fmt.Fprintf(os.Stdout, "  Prewarmed: %d\n\n", prewarmedByProject[name])
	}

	if !found {
		if poolProject != "" {
			fmt.Fprintf(os.Stdout, "No pool configuration found for project %q\n", poolProject)
		} else {
			fmt.Fprintf(os.Stdout, "No prewarmed pools configured\n")
		}
	}

	return nil
}

// ── pool disable ────────────────────────────────────────────────────

var poolDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable prewarmed pool for a project",
	Long:  "Sets enabled=false on the project bead's prewarmed_pool field. The pool manager will close existing prewarmed agents on its next reconcile pass.",
	RunE:  runPoolDisable,
}

func runPoolDisable(cmd *cobra.Command, args []string) error {
	project := resolvePoolProject()
	if project == "" {
		return fmt.Errorf("--project is required (or set BOAT_PROJECT)")
	}

	ctx := context.Background()
	poolCfg, projectBeadID, err := getProjectPoolConfig(ctx, project)
	if err != nil {
		return err
	}

	if poolCfg == nil {
		poolCfg = &beadsapi.PrewarmedPoolConfig{}
	}
	poolCfg.Enabled = false

	return updatePoolConfig(ctx, projectBeadID, project, poolCfg)
}

// ── pool enable ─────────────────────────────────────────────────────

var poolEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable prewarmed pool for a project",
	Long:  "Sets enabled=true on the project bead's prewarmed_pool field. The pool manager will start creating prewarmed agents on its next reconcile pass.",
	RunE:  runPoolEnable,
}

func runPoolEnable(cmd *cobra.Command, args []string) error {
	project := resolvePoolProject()
	if project == "" {
		return fmt.Errorf("--project is required (or set BOAT_PROJECT)")
	}

	ctx := context.Background()
	poolCfg, projectBeadID, err := getProjectPoolConfig(ctx, project)
	if err != nil {
		return err
	}

	if poolCfg == nil {
		// Set sensible defaults for a new pool.
		poolCfg = &beadsapi.PrewarmedPoolConfig{
			MinSize: 2,
			MaxSize: 5,
			Role:    "thread",
			Mode:    "crew",
		}
	}
	poolCfg.Enabled = true

	return updatePoolConfig(ctx, projectBeadID, project, poolCfg)
}

// ── pool flush ──────────────────────────────────────────────────────

var poolFlushCmd = &cobra.Command{
	Use:   "flush",
	Short: "Close all prewarmed agents for a project immediately",
	Long:  "Closes all prewarmed agent beads for the project. The reconciler will delete their pods.",
	RunE:  runPoolFlush,
}

func runPoolFlush(cmd *cobra.Command, args []string) error {
	project := resolvePoolProject()
	if project == "" {
		return fmt.Errorf("--project is required (or set BOAT_PROJECT)")
	}

	ctx := context.Background()
	agents, err := daemon.ListAgentBeads(ctx)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}

	closed := 0
	for _, a := range agents {
		if a.AgentState != "prewarmed" || a.Project != project {
			continue
		}
		status := "closed"
		if err := daemon.UpdateBead(ctx, a.ID, beadsapi.UpdateBeadRequest{
			Status: &status,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to close %s: %v\n", a.ID, err)
			continue
		}
		closed++
	}

	fmt.Fprintf(os.Stdout, "Closed %d prewarmed agent(s) for project %q\n", closed, project)
	return nil
}

// ── pool assign ─────────────────────────────────────────────────────

var poolAssignTaskID string

var poolAssignCmd = &cobra.Command{
	Use:   "assign",
	Short: "Assign a task to a prewarmed agent from the pool",
	Long: `Assigns work to an idle prewarmed agent. The pool manager picks the oldest
available agent (FIFO) and transitions it from prewarmed to assigning.

The agent's entrypoint detects the state change, exits standby, and starts
Claude with the assigned work context.`,
	RunE: runPoolAssign,
}

func runPoolAssign(cmd *cobra.Command, args []string) error {
	project := resolvePoolProject()
	if project == "" {
		return fmt.Errorf("--project is required (or set BOAT_PROJECT)")
	}

	ctx := context.Background()

	agents, err := daemon.ListAgentBeads(ctx)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}

	// Find a prewarmed agent for the project.
	var pick *beadsapi.AgentBead
	for i, a := range agents {
		if a.AgentState == "prewarmed" && a.Project == project {
			pick = &agents[i]
			break
		}
	}
	if pick == nil {
		return fmt.Errorf("no prewarmed agents available for project %q", project)
	}

	// Transition agent_state to assigning and set task_id if provided.
	fields := map[string]string{
		"agent_state":  "assigning",
		"spawn_source": "pool-assign-cli",
	}
	if poolAssignTaskID != "" {
		fields["task_id"] = poolAssignTaskID
	}
	if err := daemon.UpdateBeadFields(ctx, pick.ID, fields); err != nil {
		return fmt.Errorf("updating agent bead: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Assigned agent %s (%s) for project %q\n", pick.AgentName, pick.ID, project)
	if poolAssignTaskID != "" {
		fmt.Fprintf(os.Stdout, "  Task: %s\n", poolAssignTaskID)
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────

func resolvePoolProject() string {
	if poolProject != "" {
		return poolProject
	}
	return defaultGBProject()
}

// getProjectPoolConfig finds the project bead and returns its current pool config.
func getProjectPoolConfig(ctx context.Context, project string) (*beadsapi.PrewarmedPoolConfig, string, error) {
	projects, err := daemon.ListProjectBeads(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("listing projects: %w", err)
	}

	// Find the project bead ID. We need to query by project name.
	result, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"project"},
		Statuses: []string{"open", "in_progress", "blocked", "deferred"},
		Limit:    50,
	})
	if err != nil {
		return nil, "", fmt.Errorf("listing project beads: %w", err)
	}

	for _, b := range result.Beads {
		name := strings.TrimPrefix(b.Title, "Project: ")
		if name == project {
			info := projects[project]
			return info.PrewarmedPool, b.ID, nil
		}
	}

	return nil, "", fmt.Errorf("project %q not found", project)
}

// updatePoolConfig writes the prewarmed_pool JSON field on the project bead.
func updatePoolConfig(ctx context.Context, beadID, project string, cfg *beadsapi.PrewarmedPoolConfig) error {
	poolJSON, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshalling pool config: %w", err)
	}

	if err := daemon.UpdateBeadFields(ctx, beadID, map[string]string{
		"prewarmed_pool": string(poolJSON),
	}); err != nil {
		return fmt.Errorf("updating project bead: %w", err)
	}

	state := "disabled"
	if cfg.Enabled {
		state = "enabled"
	}
	fmt.Fprintf(os.Stdout, "Pool %s for project %q (min=%d, max=%d, role=%s, mode=%s)\n",
		state, project, cfg.MinSize, cfg.MaxSize, cfg.Role, cfg.Mode)
	return nil
}

func init() {
	poolCmd.PersistentFlags().StringVar(&poolProject, "project", "", "project name (default: from BOAT_PROJECT)")

	poolAssignCmd.Flags().StringVar(&poolAssignTaskID, "task", "", "task bead ID to pre-assign (optional)")

	poolCmd.AddCommand(poolStatusCmd)
	poolCmd.AddCommand(poolDisableCmd)
	poolCmd.AddCommand(poolEnableCmd)
	poolCmd.AddCommand(poolFlushCmd)
	poolCmd.AddCommand(poolAssignCmd)
}
