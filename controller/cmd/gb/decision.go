package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var decisionCmd = &cobra.Command{
	Use:     "decision",
	Short:   "Manage decision points",
	GroupID: "orchestration",
}

// ── decision create ─────────────────────────────────────────────────────

var decisionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a decision point and optionally wait for response",
	RunE: func(cmd *cobra.Command, args []string) error {
		prompt, _ := cmd.Flags().GetString("prompt")
		optionsJSON, _ := cmd.Flags().GetString("options")
		requestedBy, _ := cmd.Flags().GetString("requested-by")
		decisionCtx, _ := cmd.Flags().GetString("context")
		noWait, _ := cmd.Flags().GetBool("no-wait")

		if prompt == "" {
			return fmt.Errorf("--prompt is required")
		}

		fields := map[string]any{
			"prompt": prompt,
		}
		if optionsJSON != "" {
			var opts []map[string]any
			if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
				return fmt.Errorf("invalid --options JSON: %w", err)
			}
			for i, opt := range opts {
				at, _ := opt["artifact_type"].(string)
				if at == "" {
					id, _ := opt["id"].(string)
					if id == "" {
						id = fmt.Sprintf("#%d", i)
					}
					return fmt.Errorf("option %s missing required artifact_type (allowed: report, plan, checklist, diff-summary, epic, bug)", id)
				}
				if !validArtifactTypes[at] {
					return fmt.Errorf("unknown artifact_type %q on option %d (allowed: report, plan, checklist, diff-summary, epic, bug)", at, i)
				}
			}
			fields["options"] = json.RawMessage(optionsJSON)
		}
		if decisionCtx != "" {
			fields["context"] = decisionCtx
		}
		if requestedBy == "" {
			requestedBy = actor
		}
		fields["requested_by"] = requestedBy

		agentID, _ := resolveAgentID("")
		if agentID != "" {
			fields["requesting_agent_bead_id"] = agentID
		}

		fieldsJSON, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("encoding fields: %w", err)
		}

		priority, _ := cmd.Flags().GetInt("priority")

		id, err := daemon.CreateBead(cmd.Context(), beadsapi.CreateBeadRequest{
			Title:     prompt,
			Type:      "decision",
			Kind:      "data",
			Priority:  priority,
			Assignee:  actor,
			CreatedBy: actor,
			Fields:    fieldsJSON,
		})
		if err != nil {
			return fmt.Errorf("creating decision: %w", err)
		}

		if jsonOutput {
			printJSON(map[string]string{"id": id})
		} else {
			fmt.Printf("Created decision: %s\n", id)
		}

		if noWait {
			return nil
		}

		fmt.Fprintf(os.Stderr, "Waiting for response...\n")
		return waitForDecision(cmd, id)
	},
}

// ── decision list ─────────────────────────────────────────────────────

var decisionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List decision points",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, _ := cmd.Flags().GetStringSlice("status")
		limit, _ := cmd.Flags().GetInt("limit")

		if len(status) == 0 {
			status = []string{"open", "in_progress"}
		}

		result, err := daemon.ListBeadsFiltered(cmd.Context(), beadsapi.ListBeadsQuery{
			Types:    []string{"decision"},
			Statuses: status,
			Limit:    limit,
			Sort:     "-created_at",
		})
		if err != nil {
			return fmt.Errorf("listing decisions: %w", err)
		}

		if jsonOutput {
			printJSON(result.Beads)
		} else if len(result.Beads) == 0 {
			fmt.Println("No pending decisions")
		} else {
			for _, b := range result.Beads {
				printDecisionSummary(b)
				fmt.Println()
			}
		}
		return nil
	},
}

// ── decision show ─────────────────────────────────────────────────────

var decisionShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show details of a decision point",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bead, err := daemon.GetBead(cmd.Context(), args[0])
		if err != nil {
			return fmt.Errorf("getting decision %s: %w", args[0], err)
		}

		if jsonOutput {
			printJSON(bead)
		} else {
			printDecisionDetail(bead)
		}
		return nil
	},
}

// ── decision respond ──────────────────────────────────────────────────

var decisionRespondCmd = &cobra.Command{
	Use:   "respond <id>",
	Short: "Respond to a decision point",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		selected, _ := cmd.Flags().GetString("select")
		text, _ := cmd.Flags().GetString("text")

		if selected == "" && text == "" {
			return fmt.Errorf("--select or --text is required")
		}

		fields := map[string]string{}
		if selected != "" {
			fields["chosen"] = selected
		}
		if text != "" {
			fields["response_text"] = text
		}
		fields["responded_by"] = actor
		fields["responded_at"] = time.Now().UTC().Format(time.RFC3339)

		// Look up artifact_type from the chosen option.
		if selected != "" {
			bead, err := daemon.GetBead(cmd.Context(), id)
			if err == nil {
				if at := extractArtifactType(bead.Fields["options"], selected); at != "" {
					fields["required_artifact"] = at
					fields["artifact_status"] = "pending"
				}
			}
		}

		if err := daemon.CloseBead(cmd.Context(), id, fields); err != nil {
			return fmt.Errorf("closing decision %s: %w", id, err)
		}

		if jsonOutput {
			printJSON(map[string]string{"id": id, "status": "closed"})
		} else {
			fmt.Printf("Decision %s resolved\n", id)
		}
		return nil
	},
}

// ── helpers ────────────────────────────────────────────────────────────

func printDecisionSummary(b *beadsapi.BeadDetail) {
	prompt := b.Fields["prompt"]
	if prompt == "" {
		prompt = b.Title
	}
	status := b.Status
	chosen := b.Fields["chosen"]
	if chosen != "" {
		status = "resolved: " + chosen
	}

	fmt.Printf("  %s [%s] %s\n", b.ID, status, prompt)

	optionsRaw := b.Fields["options"]
	if optionsRaw != "" {
		var opts []map[string]any
		if err := json.Unmarshal([]byte(optionsRaw), &opts); err == nil {
			for _, opt := range opts {
				id, _ := opt["id"].(string)
				label, _ := opt["label"].(string)
				if label == "" {
					label, _ = opt["short"].(string)
				}
				at, _ := opt["artifact_type"].(string)
				if at != "" {
					fmt.Printf("    [%s] %s (artifact: %s)\n", id, label, at)
				} else {
					fmt.Printf("    [%s] %s\n", id, label)
				}
			}
		}
	}
}

func printDecisionDetail(b *beadsapi.BeadDetail) {
	fmt.Printf("ID:       %s\n", b.ID)
	fmt.Printf("Status:   %s\n", b.Status)

	prompt := b.Fields["prompt"]
	if prompt != "" {
		fmt.Printf("Prompt:   %s\n", prompt)
	} else {
		fmt.Printf("Title:    %s\n", b.Title)
	}

	if ctx := b.Fields["context"]; ctx != "" {
		fmt.Printf("Context:  %s\n", ctx)
	}

	optionsRaw := b.Fields["options"]
	if optionsRaw != "" {
		fmt.Println("Options:")
		var opts []map[string]any
		if err := json.Unmarshal([]byte(optionsRaw), &opts); err == nil {
			for _, opt := range opts {
				id, _ := opt["id"].(string)
				label, _ := opt["label"].(string)
				short, _ := opt["short"].(string)
				at, _ := opt["artifact_type"].(string)
				line := ""
				if label != "" {
					line = fmt.Sprintf("  [%s] %s — %s", id, short, label)
				} else {
					line = fmt.Sprintf("  [%s] %s", id, short)
				}
				if at != "" {
					line += fmt.Sprintf(" (artifact: %s)", at)
				}
				fmt.Println(line)
			}
		}
	}

	if chosen := b.Fields["chosen"]; chosen != "" {
		fmt.Printf("Chosen:   %s\n", chosen)
	}
	if respText := b.Fields["response_text"]; respText != "" {
		fmt.Printf("Response: %s\n", respText)
	}
	if respondedBy := b.Fields["responded_by"]; respondedBy != "" {
		fmt.Printf("By:       %s\n", respondedBy)
	}
	if ra := b.Fields["required_artifact"]; ra != "" {
		fmt.Printf("Artifact: %s (%s)\n", ra, b.Fields["artifact_status"])
	}
}

func waitForDecision(cmd *cobra.Command, id string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	// Try SSE first, fall back to polling.
	ch, err := daemon.EventStream(ctx, "beads.>")
	if err != nil {
		return waitDecisionPoll(ctx, id)
	}

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return waitDecisionPoll(ctx, id)
			}
			var data map[string]any
			if json.Unmarshal(evt.Data, &data) == nil && evt.Event == "beads.bead.closed" {
				// BeadClosed payload: {"bead": {"id": "...", ...}, "closed_by": "..."}
				// (not "bead_id" which is only used by BeadDeleted events)
				if bead, ok := data["bead"].(map[string]any); ok {
					if beadID, _ := bead["id"].(string); beadID == id {
						return printDecisionResult(id)
					}
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func waitDecisionPoll(ctx context.Context, id string) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}

		bead, err := daemon.GetBead(ctx, id)
		if err != nil {
			continue
		}
		if bead.Status == "closed" {
			return printDecisionResult(id)
		}
		if bead.Fields["chosen"] != "" || bead.Fields["response_text"] != "" {
			printDecisionDetail(bead)
			return nil
		}
	}
}

func printDecisionResult(id string) error {
	bead, err := daemon.GetBead(context.Background(), id)
	if err != nil {
		return err
	}
	chosen := bead.Fields["chosen"]
	responseText := bead.Fields["response_text"]
	if chosen != "" {
		fmt.Printf("Decision %s resolved: %s\n", id, chosen)
	} else if responseText != "" {
		fmt.Printf("Decision %s resolved: %s\n", id, responseText)
	} else {
		fmt.Printf("Decision %s closed\n", id)
	}
	if ra := bead.Fields["required_artifact"]; ra != "" {
		fmt.Printf("  Artifact required: %s (%s)\n", ra, bead.Fields["artifact_status"])
	}
	return nil
}

// ── decision report ───────────────────────────────────────────────────

var decisionReportCmd = &cobra.Command{
	Use:   "report <decision-id>",
	Short: "Submit a report for a decision that requires one",
	Long: `Submit an artifact for a decision that requires one, then satisfy the gate.

When a decision option has an artifact_type, the agent's gate stays pending
after the decision resolves. This command creates a report bead, links it to
the decision, closes it, and satisfies the decision gate so the agent can
proceed.

Content can be provided via --content flag or piped from stdin.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		decisionID := args[0]
		content, _ := cmd.Flags().GetString("content")
		reportType, _ := cmd.Flags().GetString("type")
		format, _ := cmd.Flags().GetString("format")

		// Read from stdin if no --content flag.
		if content == "" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			content = strings.TrimSpace(string(data))
		}
		if content == "" {
			return fmt.Errorf("--content or stdin is required")
		}

		// Fetch the decision bead to verify and extract artifact type.
		decBead, err := daemon.GetBead(cmd.Context(), decisionID)
		if err != nil {
			return fmt.Errorf("fetching decision %s: %w", decisionID, err)
		}
		if decBead.Type != "decision" {
			return fmt.Errorf("%s is not a decision bead (type=%s)", decisionID, decBead.Type)
		}

		// Extract report type from the decision's required_artifact field if not specified.
		if reportType == "" {
			reportType = decBead.Fields["required_artifact"]
		}
		if reportType == "" {
			reportType = "summary" // default
		}

		if format == "" {
			format = "markdown"
		}

		// Create report bead.
		fields := map[string]any{
			"decision_id": decisionID,
			"report_type": reportType,
			"content":     content,
			"format":      format,
		}
		fieldsJSON, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("encoding fields: %w", err)
		}

		reportID, err := daemon.CreateBead(cmd.Context(), beadsapi.CreateBeadRequest{
			Title:     fmt.Sprintf("Report (%s) for %s", reportType, decisionID),
			Type:      "report",
			Kind:      "data",
			Priority:  2,
			CreatedBy: actor,
			Fields:    fieldsJSON,
		})
		if err != nil {
			return fmt.Errorf("creating report bead: %w", err)
		}

		// Link report → decision (parent-child dependency).
		if err := daemon.AddDependency(cmd.Context(), reportID, decisionID, "parent-child", actor); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add dependency: %v\n", err)
		}

		// Close the report bead.
		if err := daemon.CloseBead(cmd.Context(), reportID, nil); err != nil {
			return fmt.Errorf("closing report bead: %w", err)
		}

		// Satisfy the decision gate and mark it as yield-satisfied so the stop hook allows exit.
		agentID, _ := resolveAgentID("")
		if agentID != "" {
			satisfyCtx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := daemon.SatisfyGate(satisfyCtx, agentID, "decision"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to satisfy decision gate: %v\n", err)
			}
			if err := daemon.UpdateBeadFields(context.Background(), agentID, map[string]string{
				"gate_satisfied_by": "yield",
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to set gate_satisfied_by field: %v\n", err)
			}
		}

		if jsonOutput {
			printJSON(map[string]string{"id": reportID, "decision_id": decisionID})
		} else {
			fmt.Printf("Report %s submitted for decision %s\n", reportID, decisionID)
		}
		return nil
	},
}

// validArtifactTypes lists allowed artifact_type values for decision options.
var validArtifactTypes = map[string]bool{
	"report": true, "plan": true, "checklist": true,
	"diff-summary": true, "epic": true, "bug": true,
}

// extractArtifactType looks up the chosen option and returns its artifact_type (if any).
func extractArtifactType(optionsJSON, chosenID string) string {
	var opts []map[string]any
	if json.Unmarshal([]byte(optionsJSON), &opts) != nil {
		return ""
	}
	for _, opt := range opts {
		if id, _ := opt["id"].(string); id == chosenID {
			if at, _ := opt["artifact_type"].(string); at != "" {
				return at
			}
		}
	}
	return ""
}

// senderFromLabels extracts the sender name from a "from:<name>" label.
func senderFromLabels(labels []string) string {
	for _, l := range labels {
		if strings.HasPrefix(l, "from:") {
			return strings.TrimPrefix(l, "from:")
		}
	}
	return ""
}

func init() {
	decisionCmd.AddCommand(decisionCreateCmd)
	decisionCmd.AddCommand(decisionListCmd)
	decisionCmd.AddCommand(decisionShowCmd)
	decisionCmd.AddCommand(decisionRespondCmd)
	decisionCmd.AddCommand(decisionReportCmd)

	decisionCreateCmd.Flags().String("prompt", "", "decision prompt (required)")
	decisionCreateCmd.Flags().String("options", "", "options JSON array")
	decisionCreateCmd.Flags().String("requested-by", "", "who is requesting (default: actor)")
	decisionCreateCmd.Flags().String("context", "", "background context for the decision")
	decisionCreateCmd.Flags().Bool("no-wait", false, "return immediately without waiting for response")
	decisionCreateCmd.Flags().Int("priority", 2, "decision priority: 0=critical, 1=high, 2=normal, 3=low, 4=backlog")

	decisionListCmd.Flags().StringSliceP("status", "s", nil, "filter by status")
	decisionListCmd.Flags().Int("limit", 20, "maximum number of results")

	decisionRespondCmd.Flags().String("select", "", "selected option ID")
	decisionRespondCmd.Flags().String("text", "", "free-text response")

	decisionReportCmd.Flags().String("content", "", "report content (or pipe from stdin)")
	decisionReportCmd.Flags().String("type", "", "report type (default: from decision label or 'summary')")
	decisionReportCmd.Flags().String("format", "markdown", "content format: markdown, json, text")
}
