package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gasboat/controller/internal/bridge"

	"github.com/spf13/cobra"
)

// attJiraKeyRe matches a strict JIRA key (e.g., "PE-7001").
var attJiraKeyRe = regexp.MustCompile(`^[A-Z]+-\d+$`)

var attachmentsCmd = &cobra.Command{
	Use:     "attachments <bead-id-or-jira-key>",
	Short:   "List JIRA attachments for a bead or issue key",
	GroupID: "orchestration",
	Args:    cobra.ExactArgs(1),
	RunE:    runAttachments,
}

var (
	attJiraURL   string
	attJiraEmail string
	attJiraToken string
)

func init() {
	attachmentsCmd.Flags().StringVar(&attJiraURL, "jira-url", os.Getenv("JIRA_BASE_URL"), "JIRA base URL")
	attachmentsCmd.Flags().StringVar(&attJiraEmail, "jira-email", os.Getenv("JIRA_EMAIL"), "JIRA email")
	attachmentsCmd.Flags().StringVar(&attJiraToken, "jira-token", os.Getenv("JIRA_API_TOKEN"), "JIRA API token")
}

func runAttachments(cmd *cobra.Command, args []string) error {
	arg := args[0]

	// Resolve JIRA key: either passed directly or fetched from bead fields.
	var jiraKey string
	if attJiraKeyRe.MatchString(arg) {
		jiraKey = arg
	} else {
		bead, err := daemon.GetBead(cmd.Context(), arg)
		if err != nil {
			return fmt.Errorf("fetching bead %s: %w", arg, err)
		}
		jiraKey = bead.Fields["jira_key"]
		if jiraKey == "" {
			return fmt.Errorf("bead %s has no jira_key field", arg)
		}
	}

	if attJiraURL == "" {
		return fmt.Errorf("--jira-url or JIRA_BASE_URL is required")
	}

	jiraClient := bridge.NewJiraClient(bridge.JiraClientConfig{
		BaseURL:  attJiraURL,
		Email:    attJiraEmail,
		APIToken: attJiraToken,
	})

	issue, err := jiraClient.GetIssue(cmd.Context(), jiraKey)
	if err != nil {
		return fmt.Errorf("fetching JIRA issue %s: %w", jiraKey, err)
	}

	attachments := issue.Fields.Attachments
	if len(attachments) == 0 {
		if jsonOutput {
			printJSON([]any{})
		} else {
			cmd.Printf("No attachments for %s\n", jiraKey)
		}
		return nil
	}

	if jsonOutput {
		data, err := json.MarshalIndent(attachments, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Print table.
	cmd.Printf("%-30s %-20s %10s  %s\n", "FILENAME", "MIME TYPE", "SIZE", "URL")
	cmd.Printf("%-30s %-20s %10s  %s\n",
		strings.Repeat("-", 30), strings.Repeat("-", 20), strings.Repeat("-", 10), strings.Repeat("-", 40))
	for _, att := range attachments {
		cmd.Printf("%-30s %-20s %10s  %s\n",
			truncateStr(att.Filename, 30),
			truncateStr(att.MimeType, 20),
			formatSize(att.Size),
			att.Content)
	}
	return nil
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func formatSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
