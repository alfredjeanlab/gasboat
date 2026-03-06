package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// formatWrapUpSlack parses a structured wrapup JSON string and renders it as
// a Slack-formatted text block. Returns an empty string if parsing fails.
func formatWrapUpSlack(wrapupJSON string) string {
	var w struct {
		Accomplishments string            `json:"accomplishments"`
		Blockers        string            `json:"blockers"`
		HandoffNotes    string            `json:"handoff_notes"`
		BeadsClosed     []string          `json:"beads_closed"`
		PullRequests    []string          `json:"pull_requests"`
		Custom          map[string]string `json:"custom"`
	}
	if err := json.Unmarshal([]byte(wrapupJSON), &w); err != nil {
		return fmt.Sprintf("\n> %s", truncateText(wrapupJSON, 500))
	}

	var parts []string
	if w.Accomplishments != "" {
		parts = append(parts, fmt.Sprintf("*Accomplishments:* %s", truncateText(w.Accomplishments, 500)))
	}
	if w.Blockers != "" {
		parts = append(parts, fmt.Sprintf("*Blockers:* %s", truncateText(w.Blockers, 300)))
	}
	if w.HandoffNotes != "" {
		parts = append(parts, fmt.Sprintf("*Handoff:* %s", truncateText(w.HandoffNotes, 300)))
	}
	if len(w.BeadsClosed) > 0 {
		parts = append(parts, fmt.Sprintf("*Beads closed:* %s", strings.Join(w.BeadsClosed, ", ")))
	}
	if len(w.PullRequests) > 0 {
		parts = append(parts, fmt.Sprintf("*PRs:* %s", strings.Join(w.PullRequests, ", ")))
	}
	for k, v := range w.Custom {
		parts = append(parts, fmt.Sprintf("*%s:* %s", k, truncateText(v, 200)))
	}

	if len(parts) == 0 {
		return ""
	}
	return "\n" + strings.Join(parts, "\n")
}
