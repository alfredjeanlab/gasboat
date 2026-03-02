package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

const (
	gasboatMarkerStart = "<!-- gasboat:start — DO NOT EDIT BELOW THIS LINE -->"
	gasboatMarkerEnd   = "<!-- gasboat:end -->"
)

// MRDescriptionSection represents the context section that the bridge manages
// inside a GitLab MR description.
type MRDescriptionSection struct {
	BeadID         string
	JIRAKey        string
	JIRAStatus     string
	PipelineStatus string
	PipelineURL    string
	Approved       string
	Approvers      string
	MRState        string
}

// buildGasboatSection renders the managed section content (without markers).
func buildGasboatSection(s MRDescriptionSection) string {
	var b strings.Builder
	b.WriteString("### Agent Context\n")
	if s.BeadID != "" {
		b.WriteString(fmt.Sprintf("- **Bead:** %s\n", s.BeadID))
	}
	if s.JIRAKey != "" {
		line := fmt.Sprintf("- **JIRA:** %s", s.JIRAKey)
		if s.JIRAStatus != "" {
			line += fmt.Sprintf(" (%s)", s.JIRAStatus)
		}
		b.WriteString(line + "\n")
	}
	if s.MRState != "" {
		b.WriteString(fmt.Sprintf("- **MR State:** %s\n", s.MRState))
	}
	if s.PipelineStatus != "" {
		icon := pipelineIcon(s.PipelineStatus)
		line := fmt.Sprintf("- **Pipeline:** %s %s", icon, s.PipelineStatus)
		if s.PipelineURL != "" {
			line = fmt.Sprintf("- **Pipeline:** [%s %s](%s)", icon, s.PipelineStatus, s.PipelineURL)
		}
		b.WriteString(line + "\n")
	}
	if s.Approved != "" {
		approvedLine := fmt.Sprintf("- **Approved:** %s", s.Approved)
		if s.Approvers != "" {
			approvedLine += fmt.Sprintf(" (by %s)", s.Approvers)
		}
		b.WriteString(approvedLine + "\n")
	}
	return b.String()
}

// pipelineIcon returns a status icon for the pipeline status.
func pipelineIcon(status string) string {
	switch status {
	case "success":
		return "✅"
	case "failed":
		return "❌"
	case "running":
		return "🔄"
	case "pending", "created":
		return "⏳"
	case "canceled":
		return "🚫"
	default:
		return "⚙️"
	}
}

// spliceGasboatSection inserts or replaces the gasboat-managed section in an
// MR description. Content outside the markers is never modified.
func spliceGasboatSection(description, sectionContent string) string {
	wrapped := gasboatMarkerStart + "\n" + sectionContent + gasboatMarkerEnd

	startIdx := strings.Index(description, gasboatMarkerStart)
	endIdx := strings.Index(description, gasboatMarkerEnd)

	if startIdx >= 0 && endIdx >= 0 && endIdx > startIdx {
		// Replace existing section.
		return description[:startIdx] + wrapped + description[endIdx+len(gasboatMarkerEnd):]
	}

	// Append new section.
	if description != "" && !strings.HasSuffix(description, "\n") {
		description += "\n"
	}
	if description != "" {
		description += "\n"
	}
	return description + wrapped
}

// extractGasboatSection returns the content between the gasboat markers, or
// empty string if markers are not found.
func extractGasboatSection(description string) string {
	startIdx := strings.Index(description, gasboatMarkerStart)
	endIdx := strings.Index(description, gasboatMarkerEnd)
	if startIdx < 0 || endIdx < 0 || endIdx <= startIdx {
		return ""
	}
	content := description[startIdx+len(gasboatMarkerStart) : endIdx]
	return strings.TrimPrefix(content, "\n")
}

// syncMRDescription fetches the current MR description, splices the gasboat
// section, and updates it via the GitLab API. This is a no-op if the section
// content hasn't changed.
func syncMRDescription(ctx context.Context, gitlab *GitLabClient, projectPath string, mrIID int, section MRDescriptionSection, logger *slog.Logger) error {
	mr, err := gitlab.GetMergeRequestByPath(ctx, projectPath, mrIID)
	if err != nil {
		return fmt.Errorf("fetch MR for description sync: %w", err)
	}

	newContent := buildGasboatSection(section)
	existingContent := extractGasboatSection(mr.Description)

	if existingContent == newContent {
		logger.Debug("MR description section unchanged, skipping update",
			"project", projectPath, "mr_iid", mrIID)
		return nil
	}

	updated := spliceGasboatSection(mr.Description, newContent)
	if err := gitlab.UpdateMergeRequestDescription(ctx, projectPath, mrIID, updated); err != nil {
		return fmt.Errorf("update MR description: %w", err)
	}

	logger.Info("synced gasboat context to MR description",
		"project", projectPath, "mr_iid", mrIID, "bead", section.BeadID)
	return nil
}
