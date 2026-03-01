package main

import (
	"fmt"
	"os"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

// resolveMailActor returns the actor name for mail operations.
func resolveMailActor() string {
	if v := os.Getenv("KD_ACTOR"); v != "" {
		return v
	}
	return actor
}

var mailCmd = &cobra.Command{
	Use:     "mail",
	Short:   "Agent-to-agent mail via beads",
	Long:    `Send and receive mail between agents. Mail items are beads with type="mail".`,
	GroupID: "orchestration",
}

// ── mail send ────────────────────────────────────────────────────────

var mailSendCmd = &cobra.Command{
	Use:   "send <recipient>",
	Short: "Send mail to another agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		recipient := args[0]
		subject, _ := cmd.Flags().GetString("subject")
		body, _ := cmd.Flags().GetString("body")

		if subject == "" {
			return fmt.Errorf("--subject (-s) is required")
		}

		sender := resolveMailActor()

		id, err := daemon.CreateBead(cmd.Context(), beadsapi.CreateBeadRequest{
			Title:       subject,
			Type:        "mail",
			Kind:        "data",
			Description: body,
			Assignee:    recipient,
			Labels:      []string{"from:" + sender},
			CreatedBy:   sender,
			Priority:    2,
		})
		if err != nil {
			return fmt.Errorf("sending mail: %w", err)
		}

		if jsonOutput {
			printJSON(map[string]string{"id": id})
		} else {
			fmt.Printf("Sent: %s → %s (id: %s)\n", sender, recipient, id)
		}
		return nil
	},
}

// ── mail inbox / inbox ────────────────────────────────────────────────

func runInbox(cmd *cobra.Command, args []string) error {
	me := resolveMailActor()
	limit, _ := cmd.Flags().GetInt("limit")

	result, err := daemon.ListBeadsFiltered(cmd.Context(), beadsapi.ListBeadsQuery{
		Types:    []string{"mail"},
		Statuses: []string{"open"},
		Assignee: me,
		Sort:     "-created_at",
		Limit:    limit,
	})
	if err != nil {
		return fmt.Errorf("listing inbox: %w", err)
	}

	if jsonOutput {
		printJSON(result.Beads)
	} else if len(result.Beads) == 0 {
		fmt.Println("No mail")
	} else {
		for _, b := range result.Beads {
			printMailLine(b)
		}
		fmt.Printf("\n%d message(s)\n", len(result.Beads))
	}
	return nil
}

var mailInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Show your mail inbox",
	RunE:  runInbox,
}

var inboxCmd = &cobra.Command{
	Use:     "inbox",
	Short:   "Show your mail inbox (alias for 'mail inbox')",
	GroupID: "orchestration",
	RunE:    runInbox,
}

// ── mail read ────────────────────────────────────────────────────────

var mailReadCmd = &cobra.Command{
	Use:   "read <id>",
	Short: "Read a mail message",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bead, err := daemon.GetBead(cmd.Context(), args[0])
		if err != nil {
			return fmt.Errorf("reading mail %s: %w", args[0], err)
		}

		if jsonOutput {
			printJSON(bead)
		} else {
			printMailDetail(bead)
		}
		return nil
	},
}

// ── mail list ────────────────────────────────────────────────────────

var mailListCmd = &cobra.Command{
	Use:   "list",
	Short: "List mail with optional filters",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, _ := cmd.Flags().GetStringSlice("status")
		limit, _ := cmd.Flags().GetInt("limit")

		if len(status) == 0 {
			status = []string{"open"}
		}

		me := resolveMailActor()
		result, err := daemon.ListBeadsFiltered(cmd.Context(), beadsapi.ListBeadsQuery{
			Types:    []string{"mail"},
			Statuses: status,
			Assignee: me,
			Sort:     "-created_at",
			Limit:    limit,
		})
		if err != nil {
			return fmt.Errorf("listing mail: %w", err)
		}

		if jsonOutput {
			printJSON(result.Beads)
		} else if len(result.Beads) == 0 {
			fmt.Println("No mail")
		} else {
			for _, b := range result.Beads {
				printMailLine(b)
			}
			fmt.Printf("\n%d message(s)\n", len(result.Beads))
		}
		return nil
	},
}

// ── helpers ─────────────────────────────────────────────────────────

func printMailLine(b *beadsapi.BeadDetail) {
	sender := senderFromLabels(b.Labels)
	fmt.Printf("  %s  %-12s  %s\n", b.ID, sender, b.Title)
}

func printMailDetail(b *beadsapi.BeadDetail) {
	sender := senderFromLabels(b.Labels)
	fmt.Printf("From:    %s\n", sender)
	fmt.Printf("To:      %s\n", b.Assignee)
	fmt.Printf("Subject: %s\n", b.Title)
	if b.Description != "" {
		fmt.Printf("Body:    %s\n", b.Description)
	}
	fmt.Printf("ID:      %s\n", b.ID)
}

func init() {
	mailSendCmd.Flags().StringP("subject", "s", "", "mail subject (required)")
	mailSendCmd.Flags().StringP("body", "b", "", "mail body")

	mailInboxCmd.Flags().Int("limit", 20, "maximum messages to show")
	mailListCmd.Flags().StringSlice("status", nil, "filter by status (default: open)")
	mailListCmd.Flags().Int("limit", 20, "maximum messages to show")
	inboxCmd.Flags().Int("limit", 20, "maximum messages to show")

	mailCmd.AddCommand(mailSendCmd)
	mailCmd.AddCommand(mailInboxCmd)
	mailCmd.AddCommand(mailReadCmd)
	mailCmd.AddCommand(mailListCmd)
}
