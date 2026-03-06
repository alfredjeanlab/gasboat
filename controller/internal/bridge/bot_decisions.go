package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

// decisionPriorityEmoji returns a colored circle emoji for the given bead priority.
// P0–1 (critical/high) → red, P2 (normal) → white, P3–4 (low/backlog) → green.
func decisionPriorityEmoji(priority int) string {
	switch {
	case priority <= 1:
		return ":red_circle:"
	case priority >= 3:
		return ":large_green_circle:"
	default:
		return ":white_circle:"
	}
}

// decisionQuestion returns the question text from a decision bead's fields.
// Prefers the canonical "prompt" field, falling back to the legacy "question"
// field for backwards compatibility with older beads.
func decisionQuestion(fields map[string]string) string {
	if v := fields["prompt"]; v != "" {
		return v
	}
	return fields["question"]
}

// NotifyDecision posts a Block Kit message to Slack for a new decision.
// Layout matches the beads implementation: each option is a Section block
// with numbered label, description, and right-aligned accessory button.
func (b *Bot) NotifyDecision(ctx context.Context, bead BeadEvent) error {
	question := decisionQuestion(bead.Fields)
	optionsRaw := bead.Fields["options"]
	agent := extractAgentName(bead.Assignee)
	agentDisplay := b.agentDisplayName(agent)

	// Parse options — try JSON array of objects first, then strings.
	type optionObj struct {
		ID           string `json:"id"`
		Short        string `json:"short"`
		Label        string `json:"label"`
		Description  string `json:"description"`
		ArtifactType string `json:"artifact_type,omitempty"`
	}
	var optObjs []optionObj
	var optStrings []string

	if err := json.Unmarshal([]byte(optionsRaw), &optObjs); err != nil || len(optObjs) == 0 {
		if err := json.Unmarshal([]byte(optionsRaw), &optStrings); err != nil {
			optStrings = []string{optionsRaw}
		}
	}

	// Build Block Kit blocks — header section with priority-colored indicator.
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("%s *Decision Needed*\n%s", decisionPriorityEmoji(bead.Priority), question), false, false),
			nil, nil,
		),
	}

	// Predecessor chain info.
	predecessorID := bead.Fields["predecessor_id"]
	if predecessorID != "" {
		// Resolve predecessor title for human-readable display.
		predDisplay := predecessorID
		if pred, err := b.daemon.GetBead(ctx, predecessorID); err == nil {
			predDisplay = beadTitle(predecessorID, pred.Title)
		}
		chainText := fmt.Sprintf(":link: _Chained from: %s_", predDisplay)
		if iterStr := bead.Fields["iteration"]; iterStr != "" && iterStr != "1" {
			chainText = fmt.Sprintf(":link: _Iteration %s — chained from: %s_", iterStr, predDisplay)
		}
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", chainText, false, false),
		))
	}

	// Decision context — additional background provided by the agent.
	// Slack mrkdwn text blocks have a 3000-char limit; truncate with ellipsis.
	if decisionCtx := bead.Fields["context"]; decisionCtx != "" {
		if len(decisionCtx) > 2900 {
			decisionCtx = decisionCtx[:2897] + "..."
		}
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", decisionCtx, false, false),
				nil, nil))
	}

	// Context block — skip entirely in threaded mode since the parent card shows it.
	if b.agentThreadingEnabled() && agent != "" {
		// No context block needed — the thread parent card provides agent context.
	} else if agent != "" {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Agent: %s | _%s_", agentDisplay, beadTitle(bead.ID, bead.Title)), false, false),
		))
	} else {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("_%s_", beadTitle(bead.ID, bead.Title)), false, false),
		))
	}

	// Option blocks — each option is a Section with accessory button.
	if len(optObjs) > 0 || len(optStrings) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		if len(optObjs) > 0 {
			for i, opt := range optObjs {
				// Build display: Short is the concise title, Label is the description.
				// Show both when available so the option title is visible.
				title := opt.Short
				if title == "" {
					title = opt.ID
				}
				desc := opt.Label

				var optText string
				if desc != "" && desc != title {
					optText = fmt.Sprintf("*%d. %s*\n%s", i+1, title, desc)
				} else {
					optText = fmt.Sprintf("*%d. %s*", i+1, title)
				}
				if opt.Description != "" {
					d := opt.Description
					if len(d) > 150 {
						d = d[:147] + "..."
					}
					optText += fmt.Sprintf("\n%s", d)
				}
				if opt.ArtifactType != "" {
					optText += fmt.Sprintf("\n_Requires: %s_", opt.ArtifactType)
				}

				buttonLabel := "Choose"
				if len(optObjs) <= 4 {
					buttonLabel = fmt.Sprintf("Choose %d", i+1)
				}

				blocks = append(blocks,
					slack.NewSectionBlock(
						slack.NewTextBlockObject("mrkdwn", optText, false, false),
						nil,
						slack.NewAccessory(
							slack.NewButtonBlockElement(
								fmt.Sprintf("resolve_%s_%d", bead.ID, i+1),
								fmt.Sprintf("%s:%d", bead.ID, i+1),
								slack.NewTextBlockObject("plain_text", buttonLabel, false, false)))))
			}
		} else {
			for i, opt := range optStrings {
				optText := fmt.Sprintf("*%d. %s*", i+1, opt)

				buttonLabel := "Choose"
				if len(optStrings) <= 4 {
					buttonLabel = fmt.Sprintf("Choose %d", i+1)
				}

				blocks = append(blocks,
					slack.NewSectionBlock(
						slack.NewTextBlockObject("mrkdwn", optText, false, false),
						nil,
						slack.NewAccessory(
							slack.NewButtonBlockElement(
								fmt.Sprintf("resolve_%s_%d", bead.ID, i+1),
								fmt.Sprintf("%s:%d", bead.ID, i+1),
								slack.NewTextBlockObject("plain_text", buttonLabel, false, false)))))
			}
		}

		// "Other" option — own section with accessory button.
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn",
					"*Other*\n_None of the above? Provide a custom response and choose the required artifact type._", false, false),
				nil,
				slack.NewAccessory(
					slack.NewButtonBlockElement(
						fmt.Sprintf("resolve_other_%s", bead.ID),
						bead.ID,
						slack.NewTextBlockObject("plain_text", "Other...", false, false)))))
	}

	// Action buttons: Dismiss at the bottom.
	dismissBtn := slack.NewButtonBlockElement("dismiss_decision", bead.ID,
		slack.NewTextBlockObject("plain_text", "Dismiss", false, false))
	blocks = append(blocks,
		slack.NewActionBlock("", dismissBtn))

	// Build message options.
	msgOpts := []slack.MsgOption{
		slack.MsgOptionText(fmt.Sprintf("Decision needed: %s", question), false),
		slack.MsgOptionBlocks(blocks...),
	}

	// Resolve target channel for this agent.
	targetChannel := b.resolveChannel(agent)

	// Thread under agent card, slack thread, or predecessor decision.
	var threadTS string
	var threadSource string

	// Check if this agent is thread-bound (spawned from a Slack thread).
	if agent != "" {
		if slackChannel, slackTS := b.resolveAgentThread(ctx, agent); slackChannel != "" && slackTS != "" {
			targetChannel = slackChannel
			threadTS = slackTS
			threadSource = "slack_thread"
		}
	}

	if threadSource == "" && b.agentThreadingEnabled() && agent != "" {
		// Agent threading mode: thread under the agent's status card.
		cardTS, err := b.ensureAgentCard(ctx, agent, targetChannel)
		if err != nil {
			b.logger.Error("failed to ensure agent card", "agent", agent, "error", err)
			// Fall through to flat posting.
		} else {
			threadTS = cardTS
			threadSource = "agent_card"
		}
	}

	// Predecessor threading (within the agent thread or top-level).
	if predecessorID != "" {
		if ref, ok := b.lookupMessage(predecessorID); ok && ref.ChannelID == targetChannel {
			// In agent mode, predecessor still chains within the thread.
			// In flat mode, predecessor creates the thread.
			if threadTS == "" {
				threadTS = ref.Timestamp
				threadSource = "predecessor"
			}
		}
	}

	if threadTS != "" {
		msgOpts = append(msgOpts, slack.MsgOptionTS(threadTS))
	}

	// Post the message.
	channelID, ts, err := b.api.PostMessageContext(ctx, targetChannel, msgOpts...)
	if err != nil {
		return fmt.Errorf("post decision to Slack: %w", err)
	}

	// Track the message and update pending count.
	ref := MessageRef{ChannelID: channelID, Timestamp: ts, Agent: agent, ThreadTS: threadTS}
	b.mu.Lock()
	b.messages[bead.ID] = ref
	if b.agentThreadingEnabled() && agent != "" {
		b.agentPending[agent]++
	}
	if agent != "" {
		b.agentSeen[agent] = time.Now()
	}
	b.mu.Unlock()

	if b.state != nil {
		_ = b.state.SetDecisionMessage(bead.ID, ref)
	}

	// Update agent card with new pending count.
	if threadSource == "agent_card" {
		b.updateAgentCard(ctx, agent)
	}

	// Mark predecessor as superseded if we threaded under it (flat mode only).
	if threadSource == "predecessor" {
		b.markDecisionSuperseded(ctx, predecessorID, bead.ID, targetChannel, threadTS)
	}

	b.logger.Info("posted decision to Slack",
		"bead", bead.ID, "channel", channelID, "ts", ts,
		"thread_source", threadSource, "predecessor", predecessorID)
	return nil
}

// UpdateDecision edits the Slack message to show resolved state.
// Called via SSE close event. The modal submission handler may have already
// updated the message directly, so this serves as a fallback.
func (b *Bot) UpdateDecision(ctx context.Context, beadID, chosen, rationale string) error {
	b.updateMessageResolved(ctx, beadID, chosen, rationale, "", "")
	return nil
}

// NotifyEscalation posts a highlighted notification for an escalated decision.
func (b *Bot) NotifyEscalation(ctx context.Context, bead BeadEvent) error {
	question := decisionQuestion(bead.Fields)
	agent := extractAgentName(bead.Assignee)
	agentDisplay := b.agentDisplayName(agent)

	text := fmt.Sprintf(":rotating_light: *ESCALATED: %s*\n%s", beadTitle(bead.ID, bead.Title), question)

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", text, false, false),
			nil, nil),
	}

	// Add context — skip agent info in threaded mode since the parent card shows it.
	contextParts := []string{fmt.Sprintf("_%s_", beadTitle(bead.ID, bead.Title))}
	if agent != "" && !b.agentThreadingEnabled() {
		contextParts = append([]string{fmt.Sprintf("Agent: %s", agentDisplay)}, contextParts...)
	}
	if requestedBy := bead.Fields["requested_by"]; requestedBy != "" {
		contextParts = append(contextParts, fmt.Sprintf("Requested by: %s", requestedBy))
	}
	blocks = append(blocks, slack.NewContextBlock("",
		slack.NewTextBlockObject("mrkdwn", strings.Join(contextParts, " | "), false, false)))

	targetChannel := b.resolveChannel(agent)

	msgOpts := []slack.MsgOption{
		slack.MsgOptionText(fmt.Sprintf("ESCALATED: %s — %s", beadTitle(bead.ID, bead.Title), question), false),
		slack.MsgOptionBlocks(blocks...),
	}

	// Thread-bound agents: post in the originating Slack thread.
	if agent != "" {
		if slackChannel, slackTS := b.resolveAgentThread(ctx, agent); slackChannel != "" && slackTS != "" {
			targetChannel = slackChannel
			msgOpts = append(msgOpts, slack.MsgOptionTS(slackTS))
		} else if b.agentThreadingEnabled() {
			// Agent threading mode: thread escalation under the agent's card.
			if cardTS, err := b.ensureAgentCard(ctx, agent, targetChannel); err == nil {
				msgOpts = append(msgOpts, slack.MsgOptionTS(cardTS))
			}
		}
	}

	_, _, err := b.api.PostMessageContext(ctx, targetChannel, msgOpts...)
	if err != nil {
		return fmt.Errorf("post escalation to Slack: %w", err)
	}

	b.logger.Info("posted escalation to Slack",
		"bead", bead.ID, "channel", targetChannel)
	return nil
}

// DismissDecision deletes the Slack message for an expired/dismissed decision.
func (b *Bot) DismissDecision(ctx context.Context, beadID string) error {
	ref, ok := b.lookupMessage(beadID)
	if !ok {
		b.logger.Debug("no Slack message found for dismissed decision", "bead", beadID)
		return nil
	}

	_, _, err := b.api.DeleteMessageContext(ctx, ref.ChannelID, ref.Timestamp)
	if err != nil {
		return fmt.Errorf("delete dismissed decision from Slack: %w", err)
	}

	// Clean up tracking and update agent card.
	agent := extractAgentName(ref.Agent)
	b.mu.Lock()
	delete(b.messages, beadID)
	if b.agentThreadingEnabled() && agent != "" {
		if b.agentPending[agent] > 0 {
			b.agentPending[agent]--
		}
	}
	b.mu.Unlock()

	if b.state != nil {
		_ = b.state.RemoveDecisionMessage(beadID)
	}

	if b.agentThreadingEnabled() && agent != "" {
		b.updateAgentCard(ctx, agent)
	}

	b.logger.Info("dismissed decision from Slack",
		"bead", beadID, "channel", ref.ChannelID)
	return nil
}

// reportEmoji returns an emoji for the given artifact/report type.
func reportEmoji(reportType string) string {
	switch reportType {
	case "plan":
		return ":clipboard:"
	case "checklist":
		return ":ballot_box_with_check:"
	case "diff-summary":
		return ":mag:"
	case "epic":
		return ":rocket:"
	case "bug":
		return ":bug:"
	default:
		return ":page_facing_up:"
	}
}

// PostReport inlines the report into the resolved decision message.
// Slack's Block Kit automatically renders a "Show more" link for long content,
// so we send the full report as blocks and let the platform handle truncation.
// If the original message was deleted, falls back to posting the report as a
// new message in the same thread or channel.
func (b *Bot) PostReport(ctx context.Context, decisionID, reportType, content string) error {
	ref, ok := b.lookupMessage(decisionID)
	if !ok {
		b.logger.Debug("no Slack message found for report's decision", "decision", decisionID)
		return nil
	}

	// Fetch the decision bead to get its priority and title.
	priority := 2 // default: normal
	decisionTitle := decisionID
	if dec, err := b.daemon.GetBead(ctx, decisionID); err == nil {
		priority = dec.Priority
		decisionTitle = beadTitle(decisionID, dec.Title)
	}

	emoji := decisionPriorityEmoji(priority) + " " + reportEmoji(reportType)

	// Fetch the existing resolved message so we can append the report.
	// Thread replies are not visible in conversations.history — use
	// conversations.replies when the message was posted in a thread.
	var existing slack.Message
	messageFound := false
	if ref.ThreadTS != "" {
		replies, _, _, err := b.api.GetConversationReplies(&slack.GetConversationRepliesParameters{
			ChannelID: ref.ChannelID,
			Timestamp: ref.ThreadTS,
			Oldest:    ref.Timestamp,
			Latest:    ref.Timestamp,
			Inclusive: true,
			Limit:     1,
		})
		if err != nil {
			b.logger.Warn("fetch decision message from thread failed, will post as new message",
				"decision", decisionID, "error", err)
		} else {
			for _, m := range replies {
				if m.Timestamp == ref.Timestamp {
					existing = m
					messageFound = true
					break
				}
			}
		}
	} else {
		msgs, err := b.api.GetConversationHistory(&slack.GetConversationHistoryParameters{
			ChannelID: ref.ChannelID,
			Latest:    ref.Timestamp,
			Inclusive: true,
			Limit:     1,
		})
		if err != nil {
			b.logger.Warn("fetch decision message failed, will post as new message",
				"decision", decisionID, "error", err)
		} else if len(msgs.Messages) > 0 && msgs.Messages[0].Timestamp == ref.Timestamp {
			// Verify timestamp matches — if the message was deleted, Slack may
			// return the nearest remaining message with a different timestamp.
			existing = msgs.Messages[0]
			messageFound = true
		}
	}

	// Fallback: if original message was deleted or not found, post as new message
	// in the same thread or channel.
	if !messageFound {
		return b.postReportFallback(ctx, ref, decisionID, decisionTitle, reportType, content, emoji)
	}

	// Build updated blocks: keep existing blocks, append divider + report.
	var blocks []slack.Block
	if len(existing.Blocks.BlockSet) > 0 {
		blocks = existing.Blocks.BlockSet
	} else {
		blocks = []slack.Block{
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", existing.Text, false, false),
				nil, nil),
		}
	}

	// Structured report using Block Kit best practices:
	// - Divider separates report from the resolved header
	// - Section block for report title
	// - Context block for metadata (type, decision link)
	// - Section block with content in a code block — Slack auto-collapses
	//   long code blocks with "Show more"
	blocks = append(blocks, slack.NewDividerBlock())
	blocks = append(blocks,
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("%s *Report (%s)*", emoji, reportType), false, false),
			nil, nil))
	blocks = append(blocks,
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Decision _%s_ · %s", decisionTitle, reportType), false, false)))
	// Wrap content in a code block so Slack collapses it with "Show more".
	blocks = append(blocks,
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("```\n%s\n```", content), false, false),
			nil, nil))

	_, _, _, updateErr := b.api.UpdateMessageContext(ctx, ref.ChannelID, ref.Timestamp,
		slack.MsgOptionBlocks(blocks...),
	)
	if updateErr != nil {
		// Update failed (message may have been deleted between fetch and update).
		// Fall back to posting as a new message.
		b.logger.Warn("failed to inline report, falling back to new message",
			"decision", decisionID, "error", updateErr)
		return b.postReportFallback(ctx, ref, decisionID, decisionTitle, reportType, content, emoji)
	}

	b.logger.Info("inlined report in decision message",
		"decision", decisionID, "report_type", reportType, "channel", ref.ChannelID)
	return nil
}

// postReportFallback posts a report as a new message when the original decision
// message is unavailable (deleted, expired, etc.).
func (b *Bot) postReportFallback(ctx context.Context, ref MessageRef, decisionID, decisionTitle, reportType, content, emoji string) error {
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("%s *Report (%s)* — Decision: _%s_", emoji, reportType, decisionTitle), false, false),
			nil, nil),
		slack.NewDividerBlock(),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("```\n%s\n```", content), false, false),
			nil, nil),
	}

	msgOpts := []slack.MsgOption{
		slack.MsgOptionText(fmt.Sprintf("Report (%s) for decision %s", reportType, decisionTitle), false),
		slack.MsgOptionBlocks(blocks...),
	}

	// Thread under the original thread if available.
	threadTS := ref.ThreadTS
	if threadTS == "" {
		threadTS = ref.Timestamp
	}
	if threadTS != "" {
		msgOpts = append(msgOpts, slack.MsgOptionTS(threadTS))
	}

	_, _, err := b.api.PostMessageContext(ctx, ref.ChannelID, msgOpts...)
	if err != nil {
		return fmt.Errorf("post report fallback: %w", err)
	}

	b.logger.Info("posted report as fallback message",
		"decision", decisionID, "report_type", reportType, "channel", ref.ChannelID)
	return nil
}
