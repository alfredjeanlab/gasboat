package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var newsCmd = &cobra.Command{
	Use:   "news",
	Short: "Show in-progress work by others",
	Long: `Show what other agents are actively working on. Use this before starting
work to avoid conflicts and get situational awareness.

Shows:
- In-progress beads by other agents (potential conflicts)
- Recently closed beads (context on recent progress)

Issues by the current actor are excluded unless --all is specified.`,
	GroupID: "orchestration",
	RunE:    runNews,
}

func init() {
	newsCmd.Flags().Bool("all", false, "include your own activity")
	newsCmd.Flags().String("window", "2h", "lookback window for recently closed")
	newsCmd.Flags().IntP("limit", "n", 50, "maximum beads per section")
	newsCmd.Flags().String("project", defaultProject(), "filter by project label (default: $KD_PROJECT or $BOAT_PROJECT)")
	newsCmd.Flags().Bool("all-projects", false, "show activity from all projects (disables project filter)")
}

func runNews(cmd *cobra.Command, args []string) error {
	showAll, _ := cmd.Flags().GetBool("all")
	windowStr, _ := cmd.Flags().GetString("window")
	limit, _ := cmd.Flags().GetInt("limit")
	project, _ := cmd.Flags().GetString("project")
	allProjects, _ := cmd.Flags().GetBool("all-projects")

	window, err := time.ParseDuration(windowStr)
	if err != nil {
		return fmt.Errorf("invalid --window duration %q: %w", windowStr, err)
	}

	ctx := cmd.Context()

	var projectLabels []string
	if !allProjects && project != "" {
		projectLabels = []string{"project:" + project}
	}

	ipResult, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Statuses: []string{"in_progress"},
		Kinds:    []string{"issue"},
		Labels:   projectLabels,
		Limit:    limit,
	})
	if err != nil {
		return fmt.Errorf("fetching in-progress: %w", err)
	}

	closedResult, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Statuses: []string{"closed"},
		Kinds:    []string{"issue"},
		Labels:   projectLabels,
		Sort:     "-updated_at",
		Limit:    limit,
	})
	if err != nil {
		return fmt.Errorf("fetching recently closed: %w", err)
	}

	ipBeads := ipResult.Beads
	closedBeads := filterRecentlyClosed(closedResult.Beads, window)
	if !showAll && actor != "" && actor != "unknown" {
		ipBeads = filterOutAssignee(ipBeads, actor)
		closedBeads = filterOutAssignee(closedBeads, actor)
	}


	if len(ipBeads) == 0 && len(closedBeads) == 0 {
		fmt.Fprintf(os.Stdout, "\nNo recent activity (last %s)\n\n", windowStr)
		return nil
	}

	if len(ipBeads) > 0 {
		fmt.Fprintf(os.Stdout, "\nIn-progress by others (%d):\n\n", len(ipBeads))
		for _, b := range ipBeads {
			printNewsBead(b)
		}
	}

	if len(closedBeads) > 0 {
		if len(ipBeads) > 0 {
			fmt.Fprintln(os.Stdout)
		}
		fmt.Fprintf(os.Stdout, "Closed in last %s (%d):\n\n", windowStr, len(closedBeads))
		for _, b := range closedBeads {
			printNewsBead(b)
		}
	}

	fmt.Fprintln(os.Stdout)
	return nil
}

func printNewsBead(b *beadsapi.BeadDetail) {
	assignee := "unassigned"
	if b.Assignee != "" {
		assignee = "@" + b.Assignee
	}

	typeStr := ""
	if b.Type != "" {
		typeStr = fmt.Sprintf("[%s] ", b.Type)
	}

	title := b.Title
	if len(title) > 72 {
		title = title[:71] + "…"
	}

	fmt.Fprintf(os.Stdout, "  %s %s— %s  %s\n", b.ID, typeStr, title, assignee)
}

func filterOutAssignee(beads []*beadsapi.BeadDetail, actorName string) []*beadsapi.BeadDetail {
	actorLower := strings.ToLower(actorName)
	var filtered []*beadsapi.BeadDetail
	for _, b := range beads {
		assigneeLower := strings.ToLower(b.Assignee)
		if assigneeLower == actorLower {
			continue
		}
		if strings.HasSuffix(assigneeLower, "/"+actorLower) {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

func filterRecentlyClosed(beads []*beadsapi.BeadDetail, window time.Duration) []*beadsapi.BeadDetail {
	cutoff := time.Now().Add(-window)
	var filtered []*beadsapi.BeadDetail
	for _, b := range beads {
		if b.UpdatedAt.IsZero() || b.UpdatedAt.After(cutoff) {
			filtered = append(filtered, b)
		}
	}
	return filtered
}

