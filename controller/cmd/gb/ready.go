package main

import (
	"context"
	"fmt"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var readyCmd = &cobra.Command{
	Use:     "ready",
	Short:   "Show beads ready to work on (open, not blocked)",
	GroupID: "session",
	RunE: func(cmd *cobra.Command, args []string) error {
		beadType, _ := cmd.Flags().GetStringSlice("type")
		assignee, _ := cmd.Flags().GetString("assignee")
		limit, _ := cmd.Flags().GetInt("limit")
		project, _ := cmd.Flags().GetString("project")
		allProjects, _ := cmd.Flags().GetBool("all-projects")

		q := beadsapi.ListBeadsQuery{
			Statuses:   []string{"open"},
			Types:      beadType,
			Kinds:      []string{"issue"},
			Limit:      limit,
			Sort:       "-created_at",
			NoOpenDeps: true,
		}
		if assignee != "" {
			q.Assignee = assignee
		}
		if !allProjects && project != "" {
			q.Labels = append(q.Labels, "project:"+project)
		}

		result, err := daemon.ListBeadsFiltered(cmd.Context(), q)
		if err != nil {
			return fmt.Errorf("listing ready beads: %w", err)
		}

		beads := result.Beads
		if jsonOutput {
			printJSON(beads)
		} else if len(beads) == 0 {
			fmt.Println("No beads ready to work on")
		} else {
			for _, b := range beads {
				fmt.Printf("  %s  %s  %s\n", b.ID, b.Title, b.Assignee)
			}
			fmt.Printf("\n%d beads (%d total)\n", len(beads), result.Total)
			fmt.Println("\nRun `kd claim <id>` before starting work on any bead.")
		}

		// Satisfy the decision gate so the stop hook doesn't block after
		// the agent checks for more work. This prevents the stop-loop where
		// persistent agents cycle between "gb ready" and blocked stops.
		satisfyGateOnReady(cmd.Context())

		return nil
	},
}

// satisfyGateOnReady satisfies the decision gate when gb ready is called,
// provided the agent has no open decision beads. This allows the stop hook
// to pass after the agent checks for more work (the normal persistent-agent
// lifecycle: finish task → gb ready → claim next or stop).
func satisfyGateOnReady(ctx context.Context) {
	agentID, err := resolveAgentIDWithFallback(ctx, "")
	if err != nil {
		return // No agent identity — can't satisfy gate.
	}

	// Only satisfy the gate if there are no open decisions. If the agent
	// has an unresolved decision, it should yield on that first.
	decisions, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"decision"},
		Statuses: []string{"open"},
		Limit:    1,
	})
	if err == nil {
		for _, d := range decisions.Beads {
			if d.Fields["requesting_agent_bead_id"] == agentID {
				return // Open decision exists — don't auto-satisfy.
			}
		}
	}

	satisfyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := daemon.SatisfyGate(satisfyCtx, agentID, "decision"); err != nil {
		return // Best-effort — don't fail the command.
	}

	// Mark how the gate was satisfied so stop-gate.sh can verify.
	markCtx, markCancel := context.WithTimeout(ctx, 5*time.Second)
	defer markCancel()
	_ = daemon.UpdateBeadFields(markCtx, agentID, map[string]string{
		"gate_satisfied_by": "ready",
	})

	// Clear the cooldown file since the gate is now satisfied.
	clearStopGateCooldown()
}

func init() {
	readyCmd.Flags().StringSliceP("type", "t", nil, "filter by type (repeatable)")
	readyCmd.Flags().String("assignee", "", "filter by assignee")
	readyCmd.Flags().Int("limit", 20, "maximum number of results")
	readyCmd.Flags().String("project", defaultProject(), "filter by project label (default: $KD_PROJECT or $BOAT_PROJECT)")
	readyCmd.Flags().Bool("all-projects", false, "show beads from all projects (disables project filter)")
}
