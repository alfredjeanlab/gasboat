package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"gasboat/controller/internal/advice"
	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var adviceCmd = &cobra.Command{
	Use:     "advice",
	Short:   "Manage advice beads",
	Long:    "Advice beads are persistent guidance delivered to agents based on label matching.",
	GroupID: "orchestration",
}

// ── advice add ────────────────────────────────────────────────────────

var adviceAddCmd = &cobra.Command{
	Use:   "add <text>",
	Short: "Create an advice bead",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		text := args[0]

		title, _ := cmd.Flags().GetString("title")
		description, _ := cmd.Flags().GetString("description")
		priority, _ := cmd.Flags().GetInt("priority")
		labels, _ := cmd.Flags().GetStringSlice("label")

		project, _ := cmd.Flags().GetString("project")
		rig, _ := cmd.Flags().GetString("rig")
		role, _ := cmd.Flags().GetString("role")
		agent, _ := cmd.Flags().GetString("agent")

		// Collect shorthand targeting labels, then AND-group them by default.
		// Multiple targeting labels (e.g. --role=cleanup --project=gasboat) mean
		// "match agents that satisfy ALL of these", not any one of them.
		var targeting []string
		if project != "" {
			targeting = append(targeting, "project:"+project)
		} else if rig != "" {
			targeting = append(targeting, "project:"+rig) // --rig is deprecated alias for --project
		}
		if role != "" {
			targeting = append(targeting, "role:"+role)
		}
		if agent != "" {
			targeting = append(targeting, "agent:"+agent)
		}
		if len(targeting) > 1 {
			for _, l := range targeting {
				labels = append(labels, "g0:"+l)
			}
		} else {
			labels = append(labels, targeting...)
		}

		if !advice.HasTargetingLabel(labels) {
			labels = append(labels, "global")
		}

		fields := make(map[string]any)
		hookCmd, _ := cmd.Flags().GetString("hook-command")
		hookTrigger, _ := cmd.Flags().GetString("hook-trigger")
		if hookCmd != "" {
			fields["hook_command"] = hookCmd
		}
		if hookTrigger != "" {
			fields["hook_trigger"] = hookTrigger
		}

		var fieldsJSON json.RawMessage
		if len(fields) > 0 {
			b, err := json.Marshal(fields)
			if err != nil {
				return fmt.Errorf("encoding fields: %w", err)
			}
			fieldsJSON = b
		}

		if title == "" {
			title = text
		}
		if description == "" {
			description = text
		}

		id, err := daemon.CreateBead(cmd.Context(), beadsapi.CreateBeadRequest{
			Title:       title,
			Description: description,
			Type:        "advice",
			Priority:    priority,
			Labels:      labels,
			CreatedBy:   actor,
			Fields:      fieldsJSON,
		})
		if err != nil {
			return fmt.Errorf("creating advice: %w", err)
		}

		if jsonOutput {
			printJSON(map[string]string{"id": id})
		} else {
			fmt.Printf("Created advice %s: %s\n", id, title)
		}
		return nil
	},
}

// ── advice list ────────────────────────────────────────────────────────

var adviceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List advice beads",
	RunE: func(cmd *cobra.Command, args []string) error {
		labels, _ := cmd.Flags().GetStringSlice("label")
		forAgent, _ := cmd.Flags().GetString("for")
		if forAgent != "" {
			labels = append(labels, forAgent)
		}

		result, err := daemon.ListBeadsFiltered(cmd.Context(), beadsapi.ListBeadsQuery{
			Types:    []string{"advice"},
			Labels:   labels,
			Statuses: []string{"open", "in_progress"},
			Limit:    100,
		})
		if err != nil {
			return fmt.Errorf("listing advice: %w", err)
		}

		if jsonOutput {
			printJSON(result.Beads)
		} else {
			printAdviceList(result.Beads, result.Total)
		}
		return nil
	},
}

// ── advice show ────────────────────────────────────────────────────────

var adviceShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show details of an advice bead",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bead, err := daemon.GetBead(cmd.Context(), args[0])
		if err != nil {
			return fmt.Errorf("getting advice %s: %w", args[0], err)
		}

		if jsonOutput {
			printJSON(bead)
			return nil
		}

		fmt.Printf("ID:          %s\n", bead.ID)
		fmt.Printf("Title:       %s\n", bead.Title)
		fmt.Printf("Status:      %s\n", bead.Status)
		if len(bead.Labels) > 0 {
			fmt.Printf("Labels:      %s\n", strings.Join(bead.Labels, ", "))
		}
		if v := bead.Fields["hook_command"]; v != "" {
			fmt.Printf("Hook Cmd:    %s\n", v)
		}
		if v := bead.Fields["hook_trigger"]; v != "" {
			fmt.Printf("Hook When:   %s\n", v)
		}
		return nil
	},
}

// ── advice remove ────────────────────────────────────────────────────────

var adviceRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove an advice bead",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]

		hard, _ := cmd.Flags().GetBool("hard")
		if hard {
			if err := daemon.DeleteBead(cmd.Context(), id); err != nil {
				return fmt.Errorf("deleting advice %s: %w", id, err)
			}
			fmt.Printf("Deleted advice %s\n", id)
		} else {
			if err := daemon.CloseBead(cmd.Context(), id, nil); err != nil {
				return fmt.Errorf("closing advice %s: %w", id, err)
			}
			fmt.Printf("Closed advice %s\n", id)
		}
		return nil
	},
}

// ── helpers ────────────────────────────────────────────────────────────

func printAdviceList(beads []*beadsapi.BeadDetail, total int) {
	if len(beads) == 0 {
		fmt.Println("No advice beads found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tLABELS")
	for _, b := range beads {
		title := b.Title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		lbls := strings.Join(b.Labels, ", ")
		fmt.Fprintf(w, "%s\t%s\t%s\n", b.ID, title, lbls)
	}
	w.Flush()
	fmt.Printf("\n%d advice beads (%d total)\n", len(beads), total)
}

func init() {
	adviceCmd.AddCommand(adviceAddCmd)
	adviceCmd.AddCommand(adviceListCmd)
	adviceCmd.AddCommand(adviceShowCmd)
	adviceCmd.AddCommand(adviceRemoveCmd)

	adviceAddCmd.Flags().StringP("title", "t", "", "override title")
	adviceAddCmd.Flags().StringP("description", "d", "", "detailed description")
	adviceAddCmd.Flags().IntP("priority", "p", 2, "priority (0-4)")
	adviceAddCmd.Flags().StringSliceP("label", "l", nil, "labels (repeatable)")
	adviceAddCmd.Flags().String("project", "", "shorthand for --label project:<value>")
	adviceAddCmd.Flags().String("rig", "", "deprecated: use --project instead")
	adviceAddCmd.Flags().String("role", "", "shorthand for --label role:<value>")
	adviceAddCmd.Flags().String("agent", "", "shorthand for --label agent:<value>")
	adviceAddCmd.Flags().Lookup("rig").Hidden = true
	adviceAddCmd.Flags().String("hook-command", "", "shell command to execute")
	adviceAddCmd.Flags().String("hook-trigger", "", "when to run")

	adviceListCmd.Flags().StringSliceP("label", "l", nil, "filter by label")
	adviceListCmd.Flags().String("for", "", "match advice for agent context")

	adviceRemoveCmd.Flags().Bool("hard", false, "permanently delete instead of closing")
}
