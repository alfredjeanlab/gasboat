package main

import (
	"fmt"

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
		return nil
	},
}

func init() {
	readyCmd.Flags().StringSliceP("type", "t", nil, "filter by type (repeatable)")
	readyCmd.Flags().String("assignee", "", "filter by assignee")
	readyCmd.Flags().Int("limit", 20, "maximum number of results")
	readyCmd.Flags().String("project", resolveProject(), "filter by project label (default: $KD_PROJECT or $BOAT_PROJECT)")
	readyCmd.Flags().Bool("all-projects", false, "show beads from all projects (disables project filter)")
}
