package bridge

import (
	"context"
	"fmt"

	"github.com/slack-go/slack"
)

// NotifyAgentCrash posts a crash alert to the agent's resolved Slack channel.
func (b *Bot) NotifyAgentCrash(ctx context.Context, bead BeadEvent) error {
	agent := bead.Assignee
	if agent == "" {
		agent = bead.Title
	}
	name := agent
	if name == "" {
		name = bead.ID
	}

	text := fmt.Sprintf(":warning: *Agent crashed: %s*", name)

	// Add reason from fields.
	reason := bead.Fields["agent_state"]
	podPhase := bead.Fields["pod_phase"]
	podName := bead.Fields["pod_name"]
	if podPhase == "failed" && reason != "failed" {
		text += fmt.Sprintf("\n> Pod phase: `%s`", podPhase)
	}
	if podName != "" {
		text += fmt.Sprintf("\n> Pod: `%s`", podName)
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", text, false, false),
			nil, nil),
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Agent: `%s` | Bead: `%s`", name, bead.ID), false, false)),
	}

	targetChannel := b.resolveChannel(agent)

	_, _, err := b.api.PostMessageContext(ctx, targetChannel,
		slack.MsgOptionText(fmt.Sprintf("Agent crashed: %s", name), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		return fmt.Errorf("post agent crash to Slack: %w", err)
	}

	b.logger.Info("posted agent crash to Slack",
		"agent", name, "bead", bead.ID, "channel", targetChannel)
	return nil
}

// NotifyJackOn posts a jack-raised alert to Slack.
func (b *Bot) NotifyJackOn(ctx context.Context, bead BeadEvent) error {
	target := bead.Fields["target"]
	agent := bead.Assignee
	ttl := bead.Fields["ttl"]
	reason := bead.Fields["reason"]

	text := fmt.Sprintf(":wrench: *Jack Raised: %s*\nTarget: `%s`", bead.ID, target)
	if agent != "" {
		text += fmt.Sprintf("\nAgent: `%s`", agent)
	}
	if ttl != "" {
		text += fmt.Sprintf("\nTTL: %s", ttl)
	}
	if reason != "" {
		text += fmt.Sprintf("\n> %s", reason)
	}

	targetChannel := b.resolveChannel(agent)
	_, _, err := b.api.PostMessageContext(ctx, targetChannel,
		slack.MsgOptionText(fmt.Sprintf("Jack raised: %s on %s", bead.ID, target), false),
		slack.MsgOptionBlocks(
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", text, false, false),
				nil, nil),
		),
	)
	if err != nil {
		return fmt.Errorf("post jack on to Slack: %w", err)
	}
	b.logger.Info("posted jack raised to Slack", "jack", bead.ID, "target", target)
	return nil
}

// NotifyJackOnBatch posts a batch summary of jack-raised events.
func (b *Bot) NotifyJackOnBatch(ctx context.Context, beads []BeadEvent) error {
	text := fmt.Sprintf(":wrench: *%d additional jacks raised* (batch)\n", len(beads))

	// Show first 5 individually.
	limit := 5
	if len(beads) < limit {
		limit = len(beads)
	}
	for _, bead := range beads[:limit] {
		target := bead.Fields["target"]
		line := fmt.Sprintf("• `%s` — target: `%s`", bead.ID, target)
		if bead.Assignee != "" {
			line += fmt.Sprintf(" (%s)", bead.Assignee)
		}
		text += line + "\n"
	}
	if len(beads) > limit {
		text += fmt.Sprintf("_...and %d more_\n", len(beads)-limit)
	}

	_, _, err := b.api.PostMessageContext(ctx, b.channel,
		slack.MsgOptionText(fmt.Sprintf("%d additional jacks raised", len(beads)), false),
		slack.MsgOptionBlocks(
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", text, false, false),
				nil, nil),
		),
	)
	if err != nil {
		return fmt.Errorf("post jack batch to Slack: %w", err)
	}
	b.logger.Info("posted jack batch to Slack", "count", len(beads))
	return nil
}

// NotifyJackOff posts a jack-lowered alert to Slack.
func (b *Bot) NotifyJackOff(ctx context.Context, bead BeadEvent) error {
	target := bead.Fields["target"]
	agent := bead.Assignee
	reason := bead.Fields["reason"]

	text := fmt.Sprintf(":white_check_mark: *Jack Lowered: %s*\nTarget: `%s`", bead.ID, target)
	if agent != "" {
		text += fmt.Sprintf("\nAgent: `%s`", agent)
	}
	if reason != "" {
		text += fmt.Sprintf("\n> %s", reason)
	}

	targetChannel := b.resolveChannel(agent)
	_, _, err := b.api.PostMessageContext(ctx, targetChannel,
		slack.MsgOptionText(fmt.Sprintf("Jack lowered: %s", bead.ID), false),
		slack.MsgOptionBlocks(
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", text, false, false),
				nil, nil),
		),
	)
	if err != nil {
		return fmt.Errorf("post jack off to Slack: %w", err)
	}
	b.logger.Info("posted jack lowered to Slack", "jack", bead.ID, "target", target)
	return nil
}

// NotifyJackExpired posts a jack-expired warning to Slack.
func (b *Bot) NotifyJackExpired(ctx context.Context, bead BeadEvent) error {
	target := bead.Fields["target"]
	agent := bead.Assignee
	reason := bead.Fields["reason"]

	text := fmt.Sprintf(":warning: *Jack Expired: %s*\nTarget: `%s`", bead.ID, target)
	if agent != "" {
		text += fmt.Sprintf("\nAgent: `%s`", agent)
	}
	if reason != "" {
		text += fmt.Sprintf("\n> %s", reason)
	}
	text += fmt.Sprintf("\n_Review revert plan and close with_ `bd jack off %s`", bead.ID)

	targetChannel := b.resolveChannel(agent)
	_, _, err := b.api.PostMessageContext(ctx, targetChannel,
		slack.MsgOptionText(fmt.Sprintf("Jack expired: %s on %s", bead.ID, target), false),
		slack.MsgOptionBlocks(
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", text, false, false),
				nil, nil),
		),
	)
	if err != nil {
		return fmt.Errorf("post jack expired to Slack: %w", err)
	}
	b.logger.Info("posted jack expired to Slack", "jack", bead.ID, "target", target)
	return nil
}
