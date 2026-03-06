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
)

// handleBlockActions processes button clicks on decision messages.
func (b *Bot) handleBlockActions(ctx context.Context, callback slack.InteractionCallback) {
	for _, action := range callback.ActionCallback.BlockActions {
		actionID := action.ActionID

		switch {
		// Autodestruct confirm/cancel buttons.
		case actionID == "autodestruct_confirm" || actionID == "autodestruct_cancel":
			b.handleAutoDestructButtonAction(ctx, actionID, callback)
			return

		// Clear button: action_id = "clear_agent", value = agent identity.
		case actionID == "clear_agent":
			b.handleClearAgent(ctx, action.Value, callback)
			return

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

// lookupArtifactType fetches the bead's options and returns the artifact_type
// for the option matching the given label. Returns "" if not found.
func (b *Bot) lookupArtifactType(ctx context.Context, beadID, chosenLabel string) string {
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err != nil {
		return ""
	}
	type optWithArtifact struct {
		ID           string `json:"id"`
		Short        string `json:"short"`
		Label        string `json:"label"`
		ArtifactType string `json:"artifact_type"`
	}
	var opts []optWithArtifact
	if raw, ok := bead.Fields["options"]; ok {
		_ = json.Unmarshal([]byte(raw), &opts)
	}
	for _, opt := range opts {
		label := opt.Label
		if label == "" {
			label = opt.Short
		}
		if label == "" {
			label = opt.ID
		}
		if label == chosenLabel && opt.ArtifactType != "" {
			return opt.ArtifactType
		}
	}
	return ""
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
		if q := decisionQuestion(bead.Fields); q != "" {
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
		if q := decisionQuestion(bead.Fields); q != "" {
			question = q
		}
	}

	// Build artifact_type select options: "none" plus all valid types.
	artifactTypeOpts := []*slack.OptionBlockObject{
		slack.NewOptionBlockObject("none",
			slack.NewTextBlockObject("plain_text", "None (no artifact required)", false, false), nil),
	}
	for _, at := range []string{"report", "plan", "checklist", "diff-summary", "epic", "bug"} {
		artifactTypeOpts = append(artifactTypeOpts,
			slack.NewOptionBlockObject(at,
				slack.NewTextBlockObject("plain_text", at, false, false), nil))
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
			slack.NewInputBlock(
				"artifact_type",
				slack.NewTextBlockObject("plain_text", "Required Artifact Type", false, false),
				slack.NewTextBlockObject("plain_text", "What artifact will you produce?", false, false),
				slack.NewOptionsSelectBlockElement(
					slack.OptTypeStatic,
					slack.NewTextBlockObject("plain_text", "Choose artifact type...", false, false),
					"artifact_type_input",
					artifactTypeOpts...,
				),
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

	// Look up artifact_type from the chosen option.
	if at := b.lookupArtifactType(ctx, beadID, chosen); at != "" {
		fields["required_artifact"] = at
		fields["artifact_status"] = "pending"
	}

	if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
		b.logger.Error("failed to resolve decision from modal",
			"bead", beadID, "error", err)
		return
	}

	// Directly update the Slack message to show resolved state.
	b.updateMessageResolved(ctx, beadID, chosen, rationale, channelID, messageTS)

	// Nudge the requesting agent so it wakes up immediately instead of waiting
	// for the next gb yield poll cycle.
	b.nudgeDecisionRequestingAgent(ctx, beadID)

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

	// Extract artifact_type from the dropdown; set required_artifact if not "none".
	if v, ok := callback.View.State.Values["artifact_type"]["artifact_type_input"]; ok {
		if at := v.SelectedOption.Value; at != "" && at != "none" {
			fields["required_artifact"] = at
			fields["artifact_status"] = "pending"
		}
	}

	if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
		b.logger.Error("failed to resolve decision from other modal",
			"bead", beadID, "error", err)
		return
	}

	// Directly update the Slack message to show resolved state.
	b.updateMessageResolved(ctx, beadID, response, rationale, channelID, messageTS)

	// Nudge the requesting agent so it wakes up immediately instead of waiting
	// for the next gb yield poll cycle.
	b.nudgeDecisionRequestingAgent(ctx, beadID)

	b.logger.Info("decision resolved via custom response",
		"bead", beadID, "response", response, "user", user)
}

// nudgeDecisionRequestingAgent looks up the requesting agent from the decision
// bead's requesting_agent_bead_id field and sends a coop nudge to wake it up.
// This ensures the agent responds to a decision resolution immediately rather
// than waiting for the next gb yield poll cycle (up to 2 seconds).
// Errors are logged and silently ignored — the agent will still detect the
// resolution via polling.
func (b *Bot) nudgeDecisionRequestingAgent(ctx context.Context, decisionBeadID string) {
	dec, err := b.daemon.GetBead(ctx, decisionBeadID)
	if err != nil {
		b.logger.Debug("nudge: could not fetch decision bead", "bead", decisionBeadID, "error", err)
		return
	}

	agentBeadID := dec.Fields["requesting_agent_bead_id"]
	if agentBeadID == "" {
		return
	}

	agentBead, err := b.daemon.GetBead(ctx, agentBeadID)
	if err != nil {
		b.logger.Debug("nudge: could not fetch agent bead", "agent_bead", agentBeadID, "error", err)
		return
	}

	coopURL := beadsapi.ParseNotes(agentBead.Notes)["coop_url"]
	if coopURL == "" {
		b.logger.Debug("nudge: agent bead has no coop_url", "agent_bead", agentBeadID)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	message := "Decision resolved: " + decisionBeadID
	if err := nudgeCoop(ctx, client, coopURL, message); err != nil {
		b.logger.Warn("nudge: failed to nudge agent for decision response",
			"agent_bead", agentBeadID, "coop_url", coopURL, "error", err)
		return
	}

	b.logger.Info("nudged agent for decision response",
		"agent_bead", agentBeadID, "decision", decisionBeadID)
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
