package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var jiraKeyExactRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+$`)

var attachmentsCmd = &cobra.Command{
	Use:     "attachments <bead-id|jira-key>",
	Short:   "Fetch JIRA attachment details for a bead or JIRA key",
	Long:    `Fetches attachment metadata from JIRA for a given bead (by looking up its jira_key field) or a JIRA key directly. Does not download attachment content.`,
	GroupID: "orchestration",
	Args:    cobra.ExactArgs(1),
	RunE:    runAttachments,
}

func init() {
	attachmentsCmd.Flags().Bool("open", false, "open first image/video attachment URL in browser")
	rootCmd.AddCommand(attachmentsCmd)
}

// jiraAttachment is a JIRA attachment from the REST API.
type jiraAttachment struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mimeType"`
	Size      int    `json:"size"`
	Content   string `json:"content"`   // download URL
	Thumbnail string `json:"thumbnail"` // thumbnail URL
	Created   string `json:"created"`
}

func runAttachments(cmd *cobra.Command, args []string) error {
	arg := args[0]

	// Resolve to a JIRA key.
	jiraKey, err := resolveJiraKey(cmd.Context(), arg)
	if err != nil {
		return err
	}

	// Fetch attachments from JIRA.
	attachments, err := fetchJiraAttachments(cmd.Context(), jiraKey)
	if err != nil {
		return err
	}

	if jsonOutput {
		printJSON(attachments)
		return nil
	}

	if len(attachments) == 0 {
		fmt.Printf("No attachments for %s\n", jiraKey)
		return nil
	}

	// Print table.
	fmt.Printf("Attachments for %s (%d):\n\n", jiraKey, len(attachments))
	fmt.Printf("  %-30s  %-20s  %10s  %-20s  %s\n", "FILENAME", "TYPE", "SIZE", "CREATED", "URL")
	fmt.Printf("  %-30s  %-20s  %10s  %-20s  %s\n",
		strings.Repeat("-", 30), strings.Repeat("-", 20), strings.Repeat("-", 10),
		strings.Repeat("-", 20), strings.Repeat("-", 3))

	for _, a := range attachments {
		created := a.Created
		if t, err := time.Parse("2006-01-02T15:04:05.000-0700", a.Created); err == nil {
			created = t.Format("2006-01-02 15:04")
		}
		fmt.Printf("  %-30s  %-20s  %10s  %-20s  %s\n",
			truncateStr(a.Filename, 30),
			truncateStr(a.MimeType, 20),
			formatSize(a.Size),
			created,
			a.Content)
	}

	// --open: print URL of first image/video attachment.
	openFlag, _ := cmd.Flags().GetBool("open")
	if openFlag {
		for _, a := range attachments {
			if strings.HasPrefix(a.MimeType, "image/") || strings.HasPrefix(a.MimeType, "video/") {
				fmt.Printf("\nOpening: %s\n", a.Content)
				// Print the URL — actual browser open is platform-dependent and
				// may not work in agent pods. The URL is the actionable output.
				return nil
			}
		}
		fmt.Println("\nNo image/video attachments to open.")
	}

	return nil
}

// resolveJiraKey converts a bead ID or JIRA key argument to a JIRA key.
func resolveJiraKey(ctx context.Context, arg string) (string, error) {
	// If it looks like a JIRA key (e.g., PE-7001), use directly.
	if jiraKeyExactRe.MatchString(arg) {
		return arg, nil
	}

	// Otherwise treat as a bead ID and look up jira_key field.
	bead, err := daemon.GetBead(ctx, arg)
	if err != nil {
		return "", fmt.Errorf("looking up bead %s: %w", arg, err)
	}
	jiraKey := bead.Fields["jira_key"]
	if jiraKey == "" {
		return "", fmt.Errorf("bead %s has no jira_key field", arg)
	}
	return jiraKey, nil
}

// fetchJiraAttachments calls the JIRA REST API to get attachments for an issue.
func fetchJiraAttachments(ctx context.Context, jiraKey string) ([]jiraAttachment, error) {
	baseURL := os.Getenv("JIRA_BASE_URL")
	email := os.Getenv("JIRA_EMAIL")
	apiToken := os.Getenv("JIRA_API_TOKEN")

	if baseURL == "" || email == "" || apiToken == "" {
		return nil, fmt.Errorf("JIRA_BASE_URL, JIRA_EMAIL, and JIRA_API_TOKEN environment variables are required")
	}

	baseURL = strings.TrimRight(baseURL, "/")
	reqURL := baseURL + "/rest/api/3/issue/" + url.PathEscape(jiraKey) + "?fields=attachment"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating JIRA request: %w", err)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(email + ":" + apiToken))
	req.Header.Set("Authorization", "Basic "+auth)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("JIRA request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading JIRA response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("JIRA API returned %d: %s", resp.StatusCode, truncateStr(string(body), 512))
	}

	var result struct {
		Fields struct {
			Attachment []jiraAttachment `json:"attachment"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decoding JIRA response: %w", err)
	}

	return result.Fields.Attachment, nil
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatSize(bytes int) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
