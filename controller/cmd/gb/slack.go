package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var slackCmd = &cobra.Command{
	Use:     "slack",
	Short:   "Slack thread commands for thread-spawned agents",
	Long:    `Read and reply to Slack threads. Requires SLACK_BRIDGE_URL, SLACK_THREAD_CHANNEL, and SLACK_THREAD_TS env vars.`,
	GroupID: "orchestration",
}

// ── slack thread ────────────────────────────────────────────────────

var slackThreadCmd = &cobra.Command{
	Use:   "thread",
	Short: "Fetch the Slack thread this agent was spawned from",
	Long: `Reads SLACK_THREAD_CHANNEL and SLACK_THREAD_TS from the environment,
fetches thread messages from the bridge HTTP API (omitting bot messages),
and outputs markdown-formatted thread content suitable for context injection.

Exits with an error if the required env vars are not set.`,
	RunE: runSlackThread,
}

func runSlackThread(cmd *cobra.Command, args []string) error {
	bridgeURL, channel, threadTS, err := resolveSlackEnv()
	if err != nil {
		return err
	}

	limit, _ := cmd.Flags().GetInt("limit")

	// Build request URL.
	u := fmt.Sprintf("%s/api/slack/threads?channel=%s&ts=%s",
		strings.TrimRight(bridgeURL, "/"),
		url.QueryEscape(channel),
		url.QueryEscape(threadTS))
	if limit > 0 {
		u += fmt.Sprintf("&limit=%d", limit)
	}

	resp, err := slackHTTPClient().Get(u)
	if err != nil {
		return fmt.Errorf("fetching thread: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bridge returned HTTP %d", resp.StatusCode)
	}

	var msgs []slackThreadMessage
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if jsonOutput {
		printJSON(msgs)
		return nil
	}

	// Markdown-formatted output for context injection.
	maxChars, _ := cmd.Flags().GetInt("max-chars")
	printThreadMarkdown(msgs, maxChars)
	return nil
}

// ── slack reply ─────────────────────────────────────────────────────

var slackReplyCmd = &cobra.Command{
	Use:   "reply <message>",
	Short: "Post a reply to the Slack thread this agent was spawned from",
	Long: `Posts a message as a reply in the originating Slack thread.
Reads SLACK_THREAD_CHANNEL and SLACK_THREAD_TS from the environment.
For conversational responses that don't need a full bead lifecycle.`,
	Args: cobra.ExactArgs(1),
	RunE: runSlackReply,
}

func runSlackReply(cmd *cobra.Command, args []string) error {
	bridgeURL, channel, threadTS, err := resolveSlackEnv()
	if err != nil {
		return err
	}

	payload := map[string]string{
		"channel":   channel,
		"thread_ts": threadTS,
		"text":      args[0],
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	u := strings.TrimRight(bridgeURL, "/") + "/api/slack/threads/reply"
	resp, err := slackHTTPClient().Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("posting reply: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bridge returned HTTP %d", resp.StatusCode)
	}

	var result slackReplyResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if jsonOutput {
		printJSON(result)
	} else {
		fmt.Println("Reply posted.")
	}
	return nil
}

// ── types ───────────────────────────────────────────────────────────

type slackThreadMessage struct {
	Author    string `json:"author"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	IsBot     bool   `json:"is_bot"`
}

type slackReplyResult struct {
	OK        bool   `json:"ok"`
	Timestamp string `json:"timestamp"`
}

// ── helpers ─────────────────────────────────────────────────────────

// resolveSlackEnv reads and validates the required Slack env vars.
func resolveSlackEnv() (bridgeURL, channel, threadTS string, err error) {
	bridgeURL = os.Getenv("SLACK_BRIDGE_URL")
	if bridgeURL == "" {
		return "", "", "", fmt.Errorf("SLACK_BRIDGE_URL is not set — cannot reach the Slack bridge")
	}
	channel = os.Getenv("SLACK_THREAD_CHANNEL")
	threadTS = os.Getenv("SLACK_THREAD_TS")
	if channel == "" || threadTS == "" {
		return "", "", "", fmt.Errorf("not a thread-spawned agent: SLACK_THREAD_CHANNEL and SLACK_THREAD_TS must be set")
	}
	return bridgeURL, channel, threadTS, nil
}

// isThreadSpawnedAgent returns true when both SLACK_THREAD_CHANNEL and
// SLACK_THREAD_TS are set, indicating this agent was spawned from a Slack thread.
func isThreadSpawnedAgent() bool {
	return os.Getenv("SLACK_THREAD_CHANNEL") != "" && os.Getenv("SLACK_THREAD_TS") != ""
}

// fetchThreadMessages fetches thread messages from the bridge HTTP API.
// Returns nil on any error (non-fatal for callers like prime).
func fetchThreadMessages(bridgeURL, channel, threadTS string, limit int) []slackThreadMessage {
	u := fmt.Sprintf("%s/api/slack/threads?channel=%s&ts=%s",
		strings.TrimRight(bridgeURL, "/"),
		url.QueryEscape(channel),
		url.QueryEscape(threadTS))
	if limit > 0 {
		u += fmt.Sprintf("&limit=%d", limit)
	}

	resp, err := slackHTTPClient().Get(u)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var msgs []slackThreadMessage
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil
	}
	return msgs
}

// formatThreadMarkdown formats thread messages as markdown, returning the string.
// maxChars controls truncation (default 4000 if <= 0).
func formatThreadMarkdown(msgs []slackThreadMessage, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 4000
	}

	if len(msgs) == 0 {
		return "(No thread messages)\n"
	}

	var buf strings.Builder
	for _, m := range msgs {
		line := fmt.Sprintf("**%s**: %s\n", m.Author, m.Text)
		if buf.Len()+len(line) > maxChars {
			buf.WriteString("\n_(thread truncated)_\n")
			break
		}
		buf.WriteString(line)
	}
	return buf.String()
}

// slackHTTPClient returns an HTTP client with a reasonable timeout.
func slackHTTPClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

// printThreadMarkdown formats thread messages as markdown for context injection.
func printThreadMarkdown(msgs []slackThreadMessage, maxChars int) {
	fmt.Print(formatThreadMarkdown(msgs, maxChars))
}

func init() {
	slackThreadCmd.Flags().Int("limit", 50, "maximum number of messages to fetch")
	slackThreadCmd.Flags().Int("max-chars", 4000, "truncate output after this many characters")

	slackCmd.AddCommand(slackThreadCmd)
	slackCmd.AddCommand(slackReplyCmd)
}
