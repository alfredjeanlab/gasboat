package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/slack-go/slack"
)

// NotifyDecision posts a Block Kit message to Slack for a new decision.
// Layout matches the beads implementation: each option is a Section block
// with numbered label, description, and right-aligned accessory button.
func (b *Bot) NotifyDecision(ctx context.Context, bead BeadEvent) error {
	question := bead.Fields["question"]
	optionsRaw := bead.Fields["options"]
	agent := bead.Assignee

	// Parse options — try JSON array of objects first, then strings.
	type optionObj struct {
		ID          string `json:"id"`
		Short       string `json:"short"`
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	var optObjs []optionObj
	var optStrings []string

	if err := json.Unmarshal([]byte(optionsRaw), &optObjs); err != nil || len(optObjs) == 0 {
		if err := json.Unmarshal([]byte(optionsRaw), &optStrings); err != nil {
			optStrings = []string{optionsRaw}
		}
	}

	// Build Block Kit blocks — header section with question.
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf(":white_circle: *Decision Needed*\n%s", question), false, false),
			nil, nil,
		),
	}

	// Predecessor chain info.
	predecessorID := bead.Fields["predecessor_id"]
	if predecessorID != "" {
		chainText := fmt.Sprintf(":link: _Chained from: %s_", predecessorID)
		if iterStr := bead.Fields["iteration"]; iterStr != "" && iterStr != "1" {
			chainText = fmt.Sprintf(":link: _Iteration %s — chained from: %s_", iterStr, predecessorID)
		}
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", chainText, false, false),
		))
	}

	// Context block with agent info.
	if agent != "" {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Agent: `%s` | Bead: `%s`", agent, bead.ID), false, false),
		))
	} else {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Bead: `%s`", bead.ID), false, false),
		))
	}

	// Option blocks — each option is a Section with accessory button.
	if len(optObjs) > 0 || len(optStrings) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		if len(optObjs) > 0 {
			for i, opt := range optObjs {
				label := opt.Label
				if label == "" {
					label = opt.Short
				}
				if label == "" {
					label = opt.ID
				}

				optText := fmt.Sprintf("*%d. %s*", i+1, label)
				if opt.Description != "" {
					desc := opt.Description
					if len(desc) > 150 {
						desc = desc[:147] + "..."
					}
					optText += fmt.Sprintf("\n%s", desc)
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
					"*Other*\n_None of the above? Provide your own response._", false, false),
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

	// Thread under predecessor decision if present and in the same channel.
	var threadTS string
	var threadSource string

	if predecessorID != "" {
		if ref, ok := b.lookupMessage(predecessorID); ok && ref.ChannelID == targetChannel {
			threadTS = ref.Timestamp
			threadSource = "predecessor"
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
	b.mu.Lock()
	b.messages[bead.ID] = MessageRef{
		ChannelID: channelID,
		Timestamp: ts,
		Agent:     agent,
	}
	b.mu.Unlock()

	if b.state != nil {
		b.state.SetDecisionMessage(bead.ID, MessageRef{
			ChannelID: channelID,
			Timestamp: ts,
			Agent:     agent,
		})
	}

	// Mark predecessor as superseded if we threaded under it.
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
func (b *Bot) UpdateDecision(ctx context.Context, beadID, chosen string) error {
	b.updateMessageResolved(ctx, beadID, chosen, "", "", "")
	return nil
}

// NotifyEscalation posts a highlighted notification for an escalated decision.
func (b *Bot) NotifyEscalation(ctx context.Context, bead BeadEvent) error {
	question := bead.Fields["question"]
	agent := bead.Assignee

	displayID := bead.ID
	text := fmt.Sprintf(":rotating_light: *ESCALATED: %s*\n%s", displayID, question)

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", text, false, false),
			nil, nil),
	}

	// Add context with agent and requester info.
	contextParts := []string{fmt.Sprintf("Bead: `%s`", bead.ID)}
	if agent != "" {
		contextParts = append([]string{fmt.Sprintf("Agent: `%s`", agent)}, contextParts...)
	}
	if requestedBy := bead.Fields["requested_by"]; requestedBy != "" {
		contextParts = append(contextParts, fmt.Sprintf("Requested by: %s", requestedBy))
	}
	blocks = append(blocks, slack.NewContextBlock("",
		slack.NewTextBlockObject("mrkdwn", strings.Join(contextParts, " | "), false, false)))

	targetChannel := b.resolveChannel(agent)

	_, _, err := b.api.PostMessageContext(ctx, targetChannel,
		slack.MsgOptionText(fmt.Sprintf("ESCALATED: %s — %s", displayID, question), false),
		slack.MsgOptionBlocks(blocks...),
	)
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

	// Clean up tracking.
	b.mu.Lock()
	delete(b.messages, beadID)
	b.mu.Unlock()

	if b.state != nil {
		b.state.RemoveDecisionMessage(beadID)
	}

	b.logger.Info("dismissed decision from Slack",
		"bead", beadID, "channel", ref.ChannelID)
	return nil
}

// handleBlockActions processes button clicks on decision messages.
func (b *Bot) handleBlockActions(ctx context.Context, callback slack.InteractionCallback) {
	for _, action := range callback.ActionCallback.BlockActions {
		actionID := action.ActionID

		switch {
		// Dismiss button: action_id = "dismiss_decision", value = beadID.
		case actionID == "dismiss_decision":
			b.handleDismiss(ctx, action.Value, callback)
			return

		// "Other..." button: action_id = "resolve_other_{beadID}", value = beadID.
		case strings.HasPrefix(actionID, "resolve_other_"):
			beadID := strings.TrimPrefix(actionID, "resolve_other_")
			b.openOtherModal(ctx, beadID, callback)
			return

		// Option button: action_id = "resolve_{beadID}_{n}", value = "{beadID}:{n}".
		case strings.HasPrefix(actionID, "resolve_"):
			// value is "beadID:optionIndex" — extract beadID and resolve.
			parts := strings.SplitN(action.Value, ":", 2)
			if len(parts) != 2 {
				continue
			}
			beadID := parts[0]
			optIndex := parts[1]
			// Look up the option label from the bead.
			chosen := b.resolveOptionLabel(ctx, beadID, optIndex)
			b.openResolveModal(ctx, beadID, chosen, callback)
			return
		}
	}
}

// resolveOptionLabel looks up the label for an option by index from the bead's fields.
func (b *Bot) resolveOptionLabel(ctx context.Context, beadID, optIndex string) string {
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err != nil {
		return fmt.Sprintf("Option %s", optIndex)
	}
	type optionObj struct {
		ID    string `json:"id"`
		Short string `json:"short"`
		Label string `json:"label"`
	}
	var opts []optionObj
	if raw, ok := bead.Fields["options"]; ok {
		_ = json.Unmarshal([]byte(raw), &opts)
	}
	// optIndex is 1-based.
	idx := 0
	if _, err := fmt.Sscanf(optIndex, "%d", &idx); err == nil && idx >= 1 && idx <= len(opts) {
		opt := opts[idx-1]
		if opt.Label != "" {
			return opt.Label
		}
		if opt.Short != "" {
			return opt.Short
		}
		return opt.ID
	}
	return fmt.Sprintf("Option %s", optIndex)
}

// openResolveModal opens a modal for confirming a decision choice with optional rationale.
func (b *Bot) openResolveModal(ctx context.Context, beadID, chosen string, callback slack.InteractionCallback) {
	// Build and open the modal immediately (trigger_id expires in 3s).
	titleText := slack.NewTextBlockObject("plain_text", "Resolve Decision", false, false)
	submitText := slack.NewTextBlockObject("plain_text", "Confirm", false, false)
	closeText := slack.NewTextBlockObject("plain_text", "Cancel", false, false)

	// Fetch the decision question for display.
	question := beadID // fallback
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err == nil {
		if q, ok := bead.Fields["question"]; ok {
			question = q
		}
	}

	blocks := slack.Blocks{
		BlockSet: []slack.Block{
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*%s*", question), false, false),
				nil, nil,
			),
			slack.NewDividerBlock(),
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf(":white_check_mark: Selected: *%s*", chosen), false, false),
				nil, nil,
			),
			slack.NewInputBlock(
				"rationale",
				slack.NewTextBlockObject("plain_text", "Rationale (optional)", false, false),
				nil,
				slack.NewPlainTextInputBlockElement(
					slack.NewTextBlockObject("plain_text", "Why this choice?", false, false),
					"rationale_input",
				),
			),
		},
	}

	// Make rationale optional.
	inputBlock := blocks.BlockSet[3].(*slack.InputBlock)
	inputBlock.Optional = true

	// Encode metadata: beadID|chosen|channelID|messageTS
	metadata := fmt.Sprintf("%s|%s|%s|%s", beadID, chosen, callback.Channel.ID, callback.Message.Timestamp)

	modal := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           titleText,
		Submit:          submitText,
		Close:           closeText,
		Blocks:          blocks,
		PrivateMetadata: metadata,
		CallbackID:      "resolve_decision",
	}

	if _, err := b.api.OpenView(callback.TriggerID, modal); err != nil {
		b.logger.Error("failed to open resolve modal", "bead", beadID, "error", err)
	}
}

// openOtherModal opens a modal for custom freeform text response.
func (b *Bot) openOtherModal(ctx context.Context, beadID string, callback slack.InteractionCallback) {
	titleText := slack.NewTextBlockObject("plain_text", "Custom Response", false, false)
	submitText := slack.NewTextBlockObject("plain_text", "Submit", false, false)
	closeText := slack.NewTextBlockObject("plain_text", "Cancel", false, false)

	// Fetch the decision question for display.
	question := beadID
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err == nil {
		if q, ok := bead.Fields["question"]; ok {
			question = q
		}
	}

	blocks := slack.Blocks{
		BlockSet: []slack.Block{
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*%s*", question), false, false),
				nil, nil,
			),
			slack.NewDividerBlock(),
			slack.NewInputBlock(
				"response",
				slack.NewTextBlockObject("plain_text", "Your Response", false, false),
				nil,
				&slack.PlainTextInputBlockElement{
					Type:        slack.METPlainTextInput,
					ActionID:    "response_input",
					Multiline:   true,
					Placeholder: slack.NewTextBlockObject("plain_text", "Type your response...", false, false),
				},
			),
		},
	}

	// Encode metadata: beadID|channelID|messageTS
	otherMetadata := fmt.Sprintf("%s|%s|%s", beadID, callback.Channel.ID, callback.Message.Timestamp)

	modal := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           titleText,
		Submit:          submitText,
		Close:           closeText,
		Blocks:          blocks,
		PrivateMetadata: otherMetadata,
		CallbackID:      "resolve_other",
	}

	if _, err := b.api.OpenView(callback.TriggerID, modal); err != nil {
		b.logger.Error("failed to open other modal", "bead", beadID, "error", err)
	}
}

// handleDismiss dismisses a decision (deletes message, closes bead).
func (b *Bot) handleDismiss(ctx context.Context, beadID string, callback slack.InteractionCallback) {
	fields := map[string]string{
		"chosen":    "dismissed",
		"rationale": fmt.Sprintf("Dismissed by @%s via Slack", callback.User.Name),
	}
	if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
		b.logger.Error("failed to dismiss decision", "bead", beadID, "error", err)
		return
	}

	// Delete the Slack message.
	_, _, _ = b.api.DeleteMessage(callback.Channel.ID, callback.MessageTs)

	b.logger.Info("decision dismissed", "bead", beadID, "user", callback.User.Name)
}

// handleViewSubmission processes modal form submissions.
func (b *Bot) handleViewSubmission(ctx context.Context, callback slack.InteractionCallback) {
	switch callback.View.CallbackID {
	case "resolve_decision":
		b.handleResolveSubmission(ctx, callback)
	case "resolve_other":
		b.handleOtherSubmission(ctx, callback)
	}
}

// handleResolveSubmission processes the resolve decision modal submission.
func (b *Bot) handleResolveSubmission(ctx context.Context, callback slack.InteractionCallback) {
	metadata := callback.View.PrivateMetadata
	// Metadata format: beadID|chosen|channelID|messageTS
	parts := strings.SplitN(metadata, "|", 4)
	if len(parts) < 2 {
		b.logger.Error("invalid resolve modal metadata", "metadata", metadata)
		return
	}
	beadID := parts[0]
	chosen := parts[1]
	channelID := ""
	messageTS := ""
	if len(parts) >= 4 {
		channelID = parts[2]
		messageTS = parts[3]
	}

	// Extract rationale from form values.
	rationale := ""
	if v, ok := callback.View.State.Values["rationale"]["rationale_input"]; ok {
		rationale = v.Value
	}

	// Build attribution.
	user := callback.User.Name
	if rationale != "" {
		rationale = fmt.Sprintf("%s — @%s via Slack", rationale, user)
	} else {
		rationale = fmt.Sprintf("Chosen by @%s via Slack", user)
	}

	fields := map[string]string{
		"chosen":    chosen,
		"rationale": rationale,
	}
	if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
		b.logger.Error("failed to resolve decision from modal",
			"bead", beadID, "error", err)
		return
	}

	// Directly update the Slack message to show resolved state.
	b.updateMessageResolved(ctx, beadID, chosen, rationale, channelID, messageTS)

	b.logger.Info("decision resolved via modal",
		"bead", beadID, "chosen", chosen, "user", user)
}

// handleOtherSubmission processes the custom response modal submission.
func (b *Bot) handleOtherSubmission(ctx context.Context, callback slack.InteractionCallback) {
	metadata := callback.View.PrivateMetadata
	// Metadata format: beadID|channelID|messageTS
	parts := strings.SplitN(metadata, "|", 3)
	beadID := parts[0]
	channelID := ""
	messageTS := ""
	if len(parts) >= 3 {
		channelID = parts[1]
		messageTS = parts[2]
	}

	response := ""
	if v, ok := callback.View.State.Values["response"]["response_input"]; ok {
		response = v.Value
	}
	if response == "" {
		return
	}

	user := callback.User.Name
	rationale := fmt.Sprintf("Custom response by @%s via Slack", user)

	fields := map[string]string{
		"chosen":    response,
		"rationale": rationale,
	}
	if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
		b.logger.Error("failed to resolve decision from other modal",
			"bead", beadID, "error", err)
		return
	}

	// Directly update the Slack message to show resolved state.
	b.updateMessageResolved(ctx, beadID, response, rationale, channelID, messageTS)

	b.logger.Info("decision resolved via custom response",
		"bead", beadID, "response", response, "user", user)
}

// markDecisionSuperseded replaces the predecessor decision message with a
// "Superseded" notice linking to the new follow-up decision.
func (b *Bot) markDecisionSuperseded(ctx context.Context, predecessorID, newDecisionID, channelID, messageTS string) {
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("*Superseded*\n\nA follow-up decision (`%s`) has been posted in this thread.\n_Please refer to the latest decision in the thread below._", newDecisionID),
				false, false),
			nil, nil),
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Original decision: `%s`", predecessorID), false, false)),
	}

	_, _, _, err := b.api.UpdateMessageContext(ctx, channelID, messageTS,
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		b.logger.Error("failed to mark decision as superseded",
			"predecessor", predecessorID, "successor", newDecisionID, "error", err)
	}
}
