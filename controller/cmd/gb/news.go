package main

import (
	"encoding/json"
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
- Recently resolved decisions (what was decided and by whom)
- Recent artifacts/reports (deliverables submitted for decisions)

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

	// Fetch recently resolved decisions.
	decResult, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"decision"},
		Statuses: []string{"closed"},
		Sort:     "-updated_at",
		Limit:    limit,
	})
	if err != nil {
		return fmt.Errorf("fetching decisions: %w", err)
	}

	// Fetch recent artifacts/reports.
	reportResult, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"report"},
		Statuses: []string{"closed"},
		Sort:     "-updated_at",
		Limit:    limit,
	})
	if err != nil {
		return fmt.Errorf("fetching reports: %w", err)
	}

	ipBeads := ipResult.Beads
	closedBeads := filterRecentlyClosed(closedResult.Beads, window)
	decisions := filterRecentlyClosed(decResult.Beads, window)
	reports := filterRecentlyClosed(reportResult.Beads, window)
	if !showAll && actor != "" && actor != "unknown" {
		ipBeads = filterOutAssignee(ipBeads, actor)
		closedBeads = filterOutAssignee(closedBeads, actor)
	}

	hasContent := len(ipBeads) > 0 || len(closedBeads) > 0 || len(decisions) > 0 || len(reports) > 0
	if !hasContent {
		fmt.Fprintf(os.Stdout, "\nNo recent activity (last %s)\n\n", windowStr)
		return nil
	}

	printed := false
	if len(ipBeads) > 0 {
		fmt.Fprintf(os.Stdout, "\nIn-progress by others (%d):\n\n", len(ipBeads))
		for _, b := range ipBeads {
			printNewsBead(b)
		}
		printed = true
	}

	if len(closedBeads) > 0 {
		if printed {
			fmt.Fprintln(os.Stdout)
		}
		fmt.Fprintf(os.Stdout, "Closed in last %s (%d):\n\n", windowStr, len(closedBeads))
		for _, b := range closedBeads {
			printNewsBead(b)
		}
		printed = true
	}

	if len(decisions) > 0 {
		if printed {
			fmt.Fprintln(os.Stdout)
		}
		fmt.Fprintf(os.Stdout, "Decisions resolved in last %s (%d):\n\n", windowStr, len(decisions))
		for _, b := range decisions {
			printNewsDecision(b)
		}
		printed = true
	}

	if len(reports) > 0 {
		if printed {
			fmt.Fprintln(os.Stdout)
		}
		fmt.Fprintf(os.Stdout, "Artifacts submitted in last %s (%d):\n\n", windowStr, len(reports))
		for _, b := range reports {
			printNewsReport(b)
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

func printNewsDecision(b *beadsapi.BeadDetail) {
	prompt := b.Fields["prompt"]
	if prompt == "" {
		prompt = b.Title
	}
	if len(prompt) > 60 {
		prompt = prompt[:59] + "…"
	}

	chosen := b.Fields["chosen"]
	// Try to resolve the chosen option ID to a human-readable short label.
	if chosen != "" {
		if label := resolveOptionLabel(b.Fields["options"], chosen); label != "" {
			chosen = label
		}
		if len(chosen) > 40 {
			chosen = chosen[:39] + "…"
		}
	}

	respondedBy := b.Fields["responded_by"]
	if respondedBy == "" {
		respondedBy = "unknown"
	}

	if chosen != "" {
		fmt.Fprintf(os.Stdout, "  %s — %s  -> %s  (by @%s)\n", b.ID, prompt, chosen, respondedBy)
	} else {
		fmt.Fprintf(os.Stdout, "  %s — %s  (resolved by @%s)\n", b.ID, prompt, respondedBy)
	}
}

func printNewsReport(b *beadsapi.BeadDetail) {
	reportType := b.Fields["report_type"]
	if reportType == "" {
		reportType = "report"
	}

	decisionID := b.Fields["decision_id"]

	title := b.Title
	if len(title) > 60 {
		title = title[:59] + "…"
	}

	createdBy := b.CreatedBy
	if createdBy == "" {
		createdBy = "unknown"
	}

	if decisionID != "" {
		fmt.Fprintf(os.Stdout, "  %s [%s] — %s  (for %s, by @%s)\n", b.ID, reportType, title, decisionID, createdBy)
	} else {
		fmt.Fprintf(os.Stdout, "  %s [%s] — %s  (by @%s)\n", b.ID, reportType, title, createdBy)
	}
}

// resolveOptionLabel finds the short or label text for a chosen option ID.
func resolveOptionLabel(optionsJSON, chosenID string) string {
	if optionsJSON == "" || chosenID == "" {
		return ""
	}
	var opts []map[string]any
	if json.Unmarshal([]byte(optionsJSON), &opts) != nil {
		return ""
	}
	for _, opt := range opts {
		id, _ := opt["id"].(string)
		if id != chosenID {
			continue
		}
		if short, _ := opt["short"].(string); short != "" {
			return short
		}
		if label, _ := opt["label"].(string); label != "" {
			return label
		}
	}
	return ""
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

