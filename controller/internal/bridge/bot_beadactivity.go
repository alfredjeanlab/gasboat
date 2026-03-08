package bridge

import (
	"context"
	"fmt"
	"time"

	"github.com/slack-go/slack"
)

// NotifyBeadCreated posts a notification in the creating agent's Slack thread.
func (b *Bot) NotifyBeadCreated(ctx context.Context, bead BeadEvent) {
	agent := extractAgentName(bead.CreatedBy)
	if agent == "" {
		return
	}

	b.recordActivity(agent)

	title := truncateText(bead.Title, 200)
	text := fmt.Sprintf(":pencil2: Created %s: *%s*", bead.Type, title)
	b.postOrUpdateBeadMessage(ctx, agent, bead.ID, text)
}

// NotifyBeadClaimed posts a notification in the claiming agent's Slack thread.
func (b *Bot) NotifyBeadClaimed(ctx context.Context, bead BeadEvent) {
	agent := extractAgentName(bead.Assignee)
	if agent == "" {
		return
	}

	b.recordActivity(agent)

	title := truncateText(bead.Title, 200)
	text := fmt.Sprintf(":arrow_right: Claimed %s: *%s*", bead.Type, title)
	b.postOrUpdateBeadMessage(ctx, agent, bead.ID, text)
}

// NotifyBeadCreatedAndClaimed posts a single combined notification when an
// agent creates a bead and immediately claims it, reducing thread noise.
func (b *Bot) NotifyBeadCreatedAndClaimed(ctx context.Context, bead BeadEvent) {
	agent := extractAgentName(bead.Assignee)
	if agent == "" {
		agent = extractAgentName(bead.CreatedBy)
	}
	if agent == "" {
		return
	}

	b.recordActivity(agent)

	title := truncateText(bead.Title, 60)
	text := fmt.Sprintf(":arrow_right: Created & claimed %s: *%s*", bead.Type, title)
	b.postOrUpdateBeadMessage(ctx, agent, bead.ID, text)
}

// NotifyBeadClosed posts a notification in the assigned agent's Slack thread.
func (b *Bot) NotifyBeadClosed(ctx context.Context, bead BeadEvent) {
	agent := extractAgentName(bead.Assignee)
	if agent == "" {
		return
	}

	b.recordActivity(agent)

	title := truncateText(bead.Title, 200)
	text := fmt.Sprintf(":white_check_mark: Closed %s: *%s*", bead.Type, title)
	b.postOrUpdateBeadMessage(ctx, agent, bead.ID, text)

	// Clean up tracking — closed is a terminal state.
	b.mu.Lock()
	delete(b.beadMsgs, beadMsgKey(agent, bead.ID))
	b.mu.Unlock()
}

// beadMsgKey returns the map key for tracking a bead's Slack message per agent.
func beadMsgKey(agent, beadID string) string {
	return agent + ":" + beadID
}

// postOrUpdateBeadMessage posts a bead status message or updates an existing
// one in-place. If a previous message exists for this agent+bead, it is updated
// rather than posting a new reply, reducing thread noise.
func (b *Bot) postOrUpdateBeadMessage(ctx context.Context, agent, beadID, text string) {
	key := beadMsgKey(agent, beadID)

	// Check for an existing message to update in-place.
	b.mu.Lock()
	ref, hasRef := b.beadMsgs[key]
	b.mu.Unlock()

	if hasRef {
		_, _, _, err := b.api.UpdateMessageContext(ctx, ref.ChannelID, ref.Timestamp,
			slack.MsgOptionText(text, false),
		)
		if err != nil {
			b.logger.Warn("bead-activity: failed to update bead message in-place",
				"agent", agent, "bead", beadID, "error", err)
			// Fall through to post a new message.
		} else {
			return
		}
	}

	// Post a new message and track it.
	b.postBeadThreadMessage(ctx, agent, beadID, text)
}

// postBeadThreadMessage posts a new bead status message and stores the ref
// for future in-place updates.
func (b *Bot) postBeadThreadMessage(ctx context.Context, agent, beadID, text string) {
	// Check for thread-bound agent first.
	if slackChannel, slackTS := b.resolveAgentThread(ctx, agent); slackChannel != "" && slackTS != "" {
		_, msgTS, err := b.api.PostMessageContext(ctx, slackChannel,
			slack.MsgOptionText(text, false),
			slack.MsgOptionTS(slackTS),
		)
		if err != nil {
			b.logger.Warn("bead-activity: failed to post to thread-bound agent",
				"agent", agent, "channel", slackChannel, "thread_ts", slackTS, "error", err)
			return
		}
		if msgTS != "" {
			b.mu.Lock()
			b.beadMsgs[beadMsgKey(agent, beadID)] = MessageRef{
				ChannelID: slackChannel, Timestamp: msgTS,
			}
			b.mu.Unlock()
		}
		return
	}

	// Fall back to agent card thread (regular threading mode).
	if !b.agentThreadingEnabled() {
		b.logger.Debug("bead-activity: agent threading disabled, dropping notification",
			"agent", agent)
		return
	}

	threadTS := b.getAgentThreadTS(agent)
	if threadTS == "" {
		b.logger.Debug("bead-activity: no agent card found, dropping notification",
			"agent", agent)
		return
	}

	channel := b.resolveChannel(agent)
	_, msgTS, err := b.api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		b.logger.Warn("bead-activity: failed to post to agent card thread",
			"agent", agent, "channel", channel, "thread_ts", threadTS, "error", err)
		return
	}
	if msgTS != "" {
		b.mu.Lock()
		b.beadMsgs[beadMsgKey(agent, beadID)] = MessageRef{
			ChannelID: channel, Timestamp: msgTS,
		}
		b.mu.Unlock()
	}
}

// postAgentThreadMessage posts a message in the agent's thread — either the
// agent card thread (for regular agents) or the bound Slack thread (for
// thread-spawned agents).
func (b *Bot) postAgentThreadMessage(ctx context.Context, agent, text string) {
	// Check for thread-bound agent first.
	if slackChannel, slackTS := b.resolveAgentThread(ctx, agent); slackChannel != "" && slackTS != "" {
		_, _, err := b.api.PostMessageContext(ctx, slackChannel,
			slack.MsgOptionText(text, false),
			slack.MsgOptionTS(slackTS),
		)
		if err != nil {
			b.logger.Warn("bead-activity: failed to post to thread-bound agent",
				"agent", agent, "channel", slackChannel, "thread_ts", slackTS, "error", err)
		}
		return
	}

	// Fall back to agent card thread (regular threading mode).
	if !b.agentThreadingEnabled() {
		b.logger.Debug("bead-activity: agent threading disabled, dropping notification",
			"agent", agent)
		return
	}

	threadTS := b.getAgentThreadTS(agent)
	if threadTS == "" {
		b.logger.Debug("bead-activity: no agent card found, dropping notification",
			"agent", agent)
		return
	}

	channel := b.resolveChannel(agent)
	_, _, err := b.api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		b.logger.Warn("bead-activity: failed to post to agent card thread",
			"agent", agent, "channel", channel, "thread_ts", threadTS, "error", err)
	}
}

// tryUpdateSpawnMessage attempts to update the spawn confirmation message
// in-place with the given text. Returns true if a spawn message existed and
// was successfully updated (consuming the ref). Returns false if no spawn
// message is tracked, allowing the caller to fall back to posting a new reply.
func (b *Bot) tryUpdateSpawnMessage(ctx context.Context, agent, text string) bool {
	agent = extractAgentName(agent)

	b.mu.Lock()
	spawnRef, hasSpawn := b.threadSpawnMsgs[agent]
	if hasSpawn {
		delete(b.threadSpawnMsgs, agent)
	}
	b.mu.Unlock()

	if !hasSpawn {
		return false
	}

	_, _, _, err := b.api.UpdateMessageContext(ctx, spawnRef.ChannelID, spawnRef.Timestamp,
		slack.MsgOptionText(text, false),
	)
	if err != nil {
		b.logger.Warn("failed to update spawn message with squawk, posting new reply",
			"agent", agent, "error", err)
		return false
	}
	return true
}

// getAgentThreadTS returns the thread timestamp for an agent's card,
// or "" if no card exists. Does not create a card.
func (b *Bot) getAgentThreadTS(agent string) string {
	b.mu.Lock()
	ref, ok := b.agentCards[agent]
	b.mu.Unlock()
	if ok {
		return ref.Timestamp
	}
	return ""
}

// recordActivity updates the agent's last-seen timestamp so the agent card
// reflects recent activity.
func (b *Bot) recordActivity(agent string) {
	b.mu.Lock()
	b.agentSeen[agent] = time.Now()
	b.mu.Unlock()
}
