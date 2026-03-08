package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// threadNudgeInterval is the minimum time between nudges for the same agent+thread.
const threadNudgeInterval = 30 * time.Second

// handleThreadForward creates a tracking bead and nudges the bound agent when
// a non-mention message is posted in an agent thread.
func (b *Bot) handleThreadForward(ctx context.Context, ev *slackevents.MessageEvent, agent string) {
	agentName := extractAgentName(agent)

	// Validate the agent is still active.
	if _, err := b.daemon.FindAgentBead(ctx, agentName); err != nil {
		b.logger.Info("thread-forward: agent no longer active, respawning with session resume",
			"agent", agentName, "channel", ev.Channel, "thread_ts", ev.ThreadTimeStamp)
		// Agent is gone — respawn the SAME agent name so the entrypoint
		// finds the existing session JSONL and PVC for session continuity.
		b.respawnThreadAgent(ctx, ev.Channel, ev.ThreadTimeStamp, agentName, ev.Text)
		return
	}

	// Resolve sender display name.
	username := ev.User
	if b.api != nil {
		if user, err := b.api.GetUserInfo(ev.User); err == nil {
			if user.RealName != "" {
				username = user.RealName
			} else if user.Name != "" {
				username = user.Name
			}
		}
	}

	// Build bead description.
	title := truncateText(fmt.Sprintf("Thread: %s", ev.Text), 80)
	slackTag := fmt.Sprintf("[slack:%s:%s]", ev.Channel, ev.ThreadTimeStamp)
	description := fmt.Sprintf("Thread reply from %s in Slack:\n\n%s\n\n---\n%s", username, ev.Text, slackTag)

	// Enrich with file attachments.
	files := b.fetchMessageFiles(ctx, ev.Channel, ev.TimeStamp)
	description += formatAttachmentsSection(files)

	var fieldsJSON json.RawMessage
	if fileFields := slackFilesToFields(files); fileFields != nil {
		fieldsJSON, _ = json.Marshal(fileFields)
	}

	// Create tracking bead.
	beadID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       title,
		Type:        "task",
		Kind:        "issue",
		Description: description,
		Assignee:    agentName,
		Labels:      []string{"slack-thread-reply"},
		Priority:    3,
		Fields:      fieldsJSON,
	})
	if err != nil {
		b.logger.Error("failed to create thread-forward bead",
			"channel", ev.Channel, "agent", agentName, "error", err)
		return
	}

	b.logger.Info("thread-forward: created tracking bead",
		"bead", beadID, "agent", agentName, "user", username)

	// Persist message ref for response relay.
	if b.state != nil {
		_ = b.state.SetChatMessage(beadID, MessageRef{
			ChannelID: ev.Channel,
			Timestamp: ev.ThreadTimeStamp,
			Agent:     agent,
		})
	}

	// Nudge with throttling — avoid flooding the agent in active threads.
	if !b.shouldThrottleNudge(agentName, ev.ThreadTimeStamp) {
		message := fmt.Sprintf("Slack thread reply (bead %s): %s", beadID, truncateText(ev.Text, 200))
		client := &http.Client{Timeout: 10 * time.Second}
		if err := NudgeAgent(ctx, b.daemon, client, b.logger, agentName, message); err != nil {
			b.logger.Error("failed to nudge agent for thread forward",
				"agent", agentName, "bead", beadID, "error", err)
		}
	}
}

// shouldThrottleNudge returns true if a nudge was sent recently for this agent+thread.
// Updates the last nudge time if not throttled.
func (b *Bot) shouldThrottleNudge(agent, threadTS string) bool {
	key := agent + ":" + threadTS
	b.mu.Lock()
	defer b.mu.Unlock()
	if last, ok := b.lastThreadNudge[key]; ok && time.Since(last) < threadNudgeInterval {
		b.logger.Debug("thread-forward: nudge throttled",
			"agent", agent, "thread_ts", threadTS, "last_nudge_ago", time.Since(last))
		return true
	}
	b.lastThreadNudge[key] = time.Now()
	return false
}

// fetchMessageFiles re-fetches a single message to extract its Files array.
// Both AppMentionEvent and MessageEvent in slack-go lack a Files field,
// so we call GetConversationReplies with Limit:1+Inclusive to get the full message.
func (b *Bot) fetchMessageFiles(ctx context.Context, channel, ts string) []slack.File {
	if b.api == nil {
		return nil
	}
	msgs, _, _, err := b.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: ts,
		Limit:     1,
		Inclusive:  true,
	})
	if err != nil {
		b.logger.Debug("failed to fetch message files", "channel", channel, "ts", ts, "error", err)
		return nil
	}
	for _, msg := range msgs {
		if msg.Timestamp == ts {
			return msg.Files
		}
	}
	return nil
}

// formatAttachmentsSection builds a markdown "## Attachments" block for bead descriptions.
func formatAttachmentsSection(files []slack.File) string {
	if len(files) == 0 {
		return ""
	}
	var buf strings.Builder
	buf.WriteString("\n\n## Attachments\n")
	for _, f := range files {
		proxyURL := "/api/slack/files/" + f.ID
		fmt.Fprintf(&buf, "- **%s** (%s, %s) — `%s`\n", f.Name, f.Mimetype, formatFileSize(f.Size), proxyURL)
	}
	return buf.String()
}

// formatFileSize returns a human-readable file size string.
func formatFileSize(bytes int) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// slackFilesToFields returns bead fields for attachment metadata.
func slackFilesToFields(files []slack.File) map[string]string {
	if len(files) == 0 {
		return nil
	}
	fields := map[string]string{
		"slack_attachment_count": fmt.Sprintf("%d", len(files)),
	}
	for _, f := range files {
		if strings.HasPrefix(f.Mimetype, "image/") {
			fields["slack_has_images"] = "true"
			break
		}
	}
	return fields
}
