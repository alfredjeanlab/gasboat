package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

// controllerVersionInfo is the response from the controller's /version endpoint.
type controllerVersionInfo struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	AgentImage string `json:"agentImage"`
	Namespace  string `json:"namespace"`
}

// handleUnreleasedCommand shows unreleased changes across tracked repos.
func (b *Bot) handleUnreleasedCommand(ctx context.Context, cmd slack.SlashCommand) {
	if b.github == nil {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: GitHub client not configured (GITHUB_TOKEN not set)", false))
		return
	}

	data := GetUnreleasedData(ctx, UnreleasedConfig{
		GitHub:        b.github,
		Repos:         b.repos,
		ControllerURL: b.controllerURL,
		Version:       b.version,
		Images:        b.imageConfigs,
	})

	// Build Block Kit response from the shared data.
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", ":package: *Unreleased Changes*", false, false),
			nil, nil),
	}

	for _, r := range data.Repos {
		blocks = append(blocks, slack.NewDividerBlock())

		if r.Error != "" {
			text := fmt.Sprintf(":warning: *%s* — error: %s", r.Repo, r.Error)
			blocks = append(blocks,
				slack.NewSectionBlock(
					slack.NewTextBlockObject("mrkdwn", text, false, false),
					nil, nil))
			continue
		}

		if r.AheadBy == 0 {
			text := fmt.Sprintf(":white_check_mark: *%s* `%s` — up to date", r.Repo, r.LatestTag)
			blocks = append(blocks,
				slack.NewSectionBlock(
					slack.NewTextBlockObject("mrkdwn", text, false, false),
					nil, nil))
			continue
		}

		header := fmt.Sprintf(":rocket: *%s* `%s` → `main` — *%d* unreleased commit", r.Repo, r.LatestTag, r.AheadBy)
		if r.AheadBy != 1 {
			header += "s"
		}
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", header, false, false),
				nil, nil))

		// Commit list.
		var lines []string
		for _, c := range r.Commits {
			lines = append(lines, fmt.Sprintf("`%s` %s — _%s_", shortSHA(c.SHA), c.Message, c.Author))
		}
		if r.AheadBy > len(r.Commits) {
			lines = append(lines, fmt.Sprintf("_...and %d more_", r.AheadBy-len(r.Commits)))
		}
		if len(lines) > 0 {
			blocks = append(blocks,
				slack.NewSectionBlock(
					slack.NewTextBlockObject("mrkdwn", strings.Join(lines, "\n"), false, false),
					nil, nil))
		}
	}

	// Images section — shows unreleased build context changes per image.
	if len(data.Images) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", ":whale: *Image Build Changes*", false, false),
				nil, nil))

		for _, img := range data.Images {
			if img.Error != "" {
				text := fmt.Sprintf(":warning: *%s* `%s` — error: %s", img.Name, img.DeployedTag, img.Error)
				blocks = append(blocks,
					slack.NewSectionBlock(
						slack.NewTextBlockObject("mrkdwn", text, false, false),
						nil, nil))
				continue
			}

			if img.ImageAheadBy == 0 {
				text := fmt.Sprintf(":white_check_mark: *%s* `%s` — image up to date", img.Name, img.DeployedTag)
				blocks = append(blocks,
					slack.NewSectionBlock(
						slack.NewTextBlockObject("mrkdwn", text, false, false),
						nil, nil))
				continue
			}

			header := fmt.Sprintf(":hammer_and_wrench: *%s* `%s` — *%d* file", img.Name, img.DeployedTag, img.ImageAheadBy)
			if img.ImageAheadBy != 1 {
				header += "s"
			}
			header += " changed (rebuild needed)"
			blocks = append(blocks,
				slack.NewSectionBlock(
					slack.NewTextBlockObject("mrkdwn", header, false, false),
					nil, nil))

			// List changed files.
			var fileLines []string
			limit := len(img.Files)
			if limit > 10 {
				limit = 10
			}
			for _, f := range img.Files[:limit] {
				fileLines = append(fileLines, fmt.Sprintf("  `%s`", f))
			}
			if len(img.Files) > limit {
				fileLines = append(fileLines, fmt.Sprintf("  _...and %d more_", len(img.Files)-limit))
			}
			if len(fileLines) > 0 {
				blocks = append(blocks,
					slack.NewSectionBlock(
						slack.NewTextBlockObject("mrkdwn", strings.Join(fileLines, "\n"), false, false),
						nil, nil))
			}
		}
	}

	// Cluster section.
	if data.Cluster != nil {
		blocks = append(blocks, slack.NewDividerBlock())
		clusterText := ":gear: *Cluster*"
		clusterText += fmt.Sprintf("\nController: `%s` (%s)", data.Cluster.Version, shortSHA(data.Cluster.Commit))
		if data.Cluster.AgentImage != "" {
			clusterText += fmt.Sprintf("\nAgent image: `%s`", data.Cluster.AgentImage)
		}
		if data.Cluster.Namespace != "" {
			clusterText += fmt.Sprintf("\nNamespace: `%s`", data.Cluster.Namespace)
		}
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", clusterText, false, false),
				nil, nil))
	}

	// Footer.
	footer := fmt.Sprintf("slack-bridge running: `%s`", data.Bridge)
	blocks = append(blocks,
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", footer, false, false)))

	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionBlocks(blocks...))
}

// fetchControllerVersion calls the controller's /version endpoint.
func fetchControllerVersion(ctx context.Context, baseURL string) *controllerVersionInfo {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/version", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}

	var info controllerVersionInfo
	if json.Unmarshal(body, &info) != nil {
		return nil
	}
	return &info
}

// shortSHA returns the first 7 characters of a SHA.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// firstLine returns the first line of a multi-line string.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
