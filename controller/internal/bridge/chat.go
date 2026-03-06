// Package bridge provides chat forwarding between Slack and agents.
//
// Chat watches the kbeads SSE event stream for closed beads with the
// "slack-chat" or "slack-mention" label. When a chat bead is closed
// (by an agent), it relays the close reason/notes back to the original
// Slack thread as a reply.
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// ChatConfig holds configuration for the Chat watcher.
type ChatConfig struct {
	Daemon BeadClient
	Bot    *Bot
	State  *StateManager
	Logger *slog.Logger
}

// Chat watches for closed chat beads and relays responses to Slack threads.
type Chat struct {
	daemon     BeadClient
	bot        *Bot
	state      *StateManager
	logger     *slog.Logger
	httpClient *http.Client
}

// NewChat creates a new chat forwarding watcher.
func NewChat(cfg ChatConfig) *Chat {
	return &Chat{
		daemon:     cfg.Daemon,
		bot:        cfg.Bot,
		state:      cfg.State,
		logger:     cfg.Logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// chat bead closed events.
func (c *Chat) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.closed", c.handleClosed)
	c.logger.Info("chat watcher registered SSE handlers",
		"topics", []string{"beads.bead.closed"})
}

func (c *Chat) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	// Only handle beads with the "slack-chat" or "slack-mention" label.
	if !hasLabel(bead.Labels, "slack-chat") && !hasLabel(bead.Labels, "slack-mention") {
		return
	}

	c.logger.Info("chat bead closed",
		"id", bead.ID,
		"assignee", bead.Assignee)

	// Look up the Slack message ref: try state first, then parse from bead description.
	ref, ok := c.lookupChatMessage(ctx, bead.ID)
	if !ok {
		c.logger.Warn("chat bead closed but no Slack message ref found",
			"id", bead.ID)
		return
	}

	// Fetch full bead to get close reason/notes.
	detail, err := c.daemon.GetBead(ctx, bead.ID)
	if err != nil {
		c.logger.Error("failed to get chat bead details",
			"id", bead.ID, "error", err)
		return
	}

	// Build response text from close reason or notes.
	agentDisplay := extractAgentName(bead.Assignee)
	if agentDisplay == "" {
		agentDisplay = "Agent"
	}
	if c.bot != nil {
		agentDisplay = c.bot.agentDisplayName(bead.Assignee)
	}
	response := buildChatResponse(detail, agentDisplay)

	// Post as thread reply.
	if c.bot != nil && c.bot.api != nil {
		_, _, _ = c.bot.api.PostMessage(ref.ChannelID,
			slack.MsgOptionText(response, false),
			slack.MsgOptionTS(ref.Timestamp),
		)
		c.logger.Info("chat response relayed to Slack",
			"bead", bead.ID, "channel", ref.ChannelID)
	}

	// Clean up state.
	if c.state != nil {
		_ = c.state.RemoveChatMessage(bead.ID)
	}

	// Nudge agent so it picks up the completed task.
	c.nudgeAgent(ctx, *bead)
}

// lookupChatMessage finds the Slack message ref for a chat bead.
// Tries persisted state first, then falls back to parsing the bead description.
func (c *Chat) lookupChatMessage(ctx context.Context, beadID string) (MessageRef, bool) {
	// Try state manager first.
	if c.state != nil {
		if ref, ok := c.state.GetChatMessage(beadID); ok {
			return ref, true
		}
	}

	// Fallback: parse [slack:CHANNEL:TS] from bead description.
	detail, err := c.daemon.GetBead(ctx, beadID)
	if err != nil {
		return MessageRef{}, false
	}

	channelID, ts := parseSlackMeta(detail.Fields["description"])
	if channelID == "" {
		// Also try the notes field (some beads store description there).
		channelID, ts = parseSlackMeta(detail.Notes)
	}
	if channelID == "" {
		return MessageRef{}, false
	}
	return MessageRef{ChannelID: channelID, Timestamp: ts}, true
}

// parseSlackMeta extracts channel ID and message timestamp from text
// containing a [slack:CHANNEL:TIMESTAMP] tag.
func parseSlackMeta(text string) (channelID, messageTS string) {
	const prefix = "[slack:"
	idx := strings.Index(text, prefix)
	if idx < 0 {
		return "", ""
	}
	rest := text[idx+len(prefix):]
	endIdx := strings.Index(rest, "]")
	if endIdx < 0 {
		return "", ""
	}
	parts := strings.SplitN(rest[:endIdx], ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// buildChatResponse constructs the Slack reply text from a closed bead.
// agentDisplay should be a pre-resolved display name (clickable link or plain name).
func buildChatResponse(detail *beadsapi.BeadDetail, agentDisplay string) string {
	if agentDisplay == "" {
		agentDisplay = "Agent"
	}

	// Use close reason (from fields) or notes for the response body.
	reason := detail.Fields["reason"]
	if reason == "" {
		reason = detail.Fields["close_reason"]
	}
	if reason == "" {
		reason = detail.Notes
	}

	if reason != "" {
		return fmt.Sprintf(":robot_face: *%s* responded:\n\n%s", agentDisplay, reason)
	}
	return fmt.Sprintf(":robot_face: *%s* completed the task (no response text).", agentDisplay)
}

// hasLabel checks if a label is present in a slice.
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// nudgeAgent delivers a chat nudge to the assigned agent with retry.
func (c *Chat) nudgeAgent(ctx context.Context, bead BeadEvent) {
	agentName := bead.Assignee
	if agentName == "" {
		return
	}

	message := fmt.Sprintf("Chat task completed: %s", bead.Title)
	if err := NudgeAgent(ctx, c.daemon, c.httpClient, c.logger, agentName, message); err != nil {
		c.logger.Error("failed to nudge agent for chat",
			"agent", agentName, "error", err)
	}
}
