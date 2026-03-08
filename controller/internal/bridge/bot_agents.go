package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// agentThreadingEnabled returns true if agent card threading is active.
func (b *Bot) agentThreadingEnabled() bool {
	return b.threadingMode == "agent"
}

// pruneStaleAgentCards compresses agent cards for agents that are no longer active.
// On bot restart, the state file may contain cards for agents that have since
// finished (done/failed) or whose beads have been closed. This method queries the
// daemon for currently active agents and updates Slack messages for stale cards
// to a compact single-line format (rather than deleting them, since thread replies
// persist and a missing parent card is confusing).
func (b *Bot) pruneStaleAgentCards(ctx context.Context) {
	b.mu.Lock()
	cardCount := len(b.agentCards)
	b.mu.Unlock()

	if cardCount == 0 {
		return
	}

	activeAgents, err := b.daemon.ListAgentBeads(ctx)
	if err != nil {
		b.logger.Error("prune agent cards: failed to list active agents", "error", err)
		return
	}

	// Build a set of active agent short names, excluding done/failed agents.
	active := make(map[string]bool, len(activeAgents))
	for _, a := range activeAgents {
		if a.AgentState == "done" || a.AgentState == "failed" {
			continue // Treat terminal-state agents as stale even if bead is still open.
		}
		if a.Metadata["stop_requested"] != "" {
			continue // Agent has requested shutdown.
		}
		active[extractAgentName(a.AgentName)] = true
	}

	// Collect stale cards (agents not in the active set).
	b.mu.Lock()
	var stale []string
	for agent := range b.agentCards {
		if !active[extractAgentName(agent)] {
			stale = append(stale, agent)
		}
	}
	b.mu.Unlock()

	if len(stale) == 0 {
		b.logger.Info("prune agent cards: all cards are current", "total", cardCount)
		return
	}

	b.logger.Info("prune agent cards: compressing stale cards",
		"stale", len(stale), "total", cardCount, "active", len(activeAgents))

	for _, agent := range stale {
		b.mu.Lock()
		ref, ok := b.agentCards[agent]
		state := b.agentState[agent]
		if ok {
			delete(b.agentPending, agent)
			delete(b.agentPodName, agent)
			delete(b.agentImageTag, agent)
			delete(b.agentRole, agent)
		}
		b.mu.Unlock()

		if ok {
			// Update the card to a compact single-line format instead of deleting.
			if state == "" {
				state = "done"
			}
			blocks := buildCompactAgentCardBlocks(agent, state)
			_, _, _, err := b.api.UpdateMessageContext(ctx, ref.ChannelID, ref.Timestamp,
				slack.MsgOptionText(fmt.Sprintf("Agent: %s (%s)", extractAgentName(agent), state), false),
				slack.MsgOptionBlocks(blocks...),
			)
			if err != nil {
				b.logger.Warn("prune agent cards: failed to compress Slack message",
					"agent", agent, "error", err)
			}
			b.logger.Info("compressed stale agent card", "agent", agent)
		}
	}
}

// startPeriodicPrune runs pruneStaleAgentCards every 5 minutes until ctx is cancelled.
func (b *Bot) startPeriodicPrune(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.pruneStaleAgentCards(ctx)
		}
	}
}

// agentTaskTitle fetches the title of the task currently claimed by agent.
// Returns "" if none is found or on error. Tries both the full identity
// and the short name to handle assignee format mismatches.
func (b *Bot) agentTaskTitle(ctx context.Context, agent string) string {
	bead, err := b.daemon.ListAssignedTask(ctx, agent)
	if err == nil && bead != nil {
		return bead.Title
	}
	// Retry with short name if agent is a full path (project/role/name).
	if short := extractAgentName(agent); short != agent {
		bead, err = b.daemon.ListAssignedTask(ctx, short)
		if err == nil && bead != nil {
			return bead.Title
		}
	}
	return ""
}

// ensureAgentCard posts or retrieves the agent status card for threading.
// Returns the card's message timestamp for use as threadTS.
func (b *Bot) ensureAgentCard(ctx context.Context, agent, channelID string) (string, error) {
	b.mu.Lock()
	if ref, ok := b.agentCards[agent]; ok && ref.ChannelID == channelID {
		b.mu.Unlock()
		return ref.Timestamp, nil
	}
	b.mu.Unlock()

	// Post a new status card.
	b.mu.Lock()
	pending := b.agentPending[agent]
	state := b.agentState[agent]
	seen := b.agentSeen[agent]
	podName := b.agentPodName[agent]
	imageTag := b.agentImageTag[agent]
	role := b.agentRole[agent]
	b.mu.Unlock()

	taskTitle := b.agentTaskTitle(ctx, agent)
	blocks := buildAgentCardBlocks(agent, pending, state, taskTitle, seen, b.coopmuxPublicURL, podName, imageTag, role)
	cardChannel, ts, err := b.api.PostMessageContext(ctx, channelID,
		slack.MsgOptionText(fmt.Sprintf("Agent: %s", extractAgentName(agent)), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		return "", fmt.Errorf("post agent card: %w", err)
	}

	ref := MessageRef{ChannelID: cardChannel, Timestamp: ts, Agent: agent}

	b.mu.Lock()
	b.agentCards[agent] = ref
	b.mu.Unlock()

	if b.state != nil {
		_ = b.state.SetAgentCard(agent, ref)
	}

	b.logger.Info("posted agent status card", "agent", agent, "channel", cardChannel, "ts", ts)
	return ts, nil
}

// NotifyAgentSpawn is called when an agent bead is first created.
// It records the initial spawning state and posts the agent card immediately.
// On SSE replay after restart, stale created events for closed agents are
// skipped to prevent zombie cards from reappearing.
func (b *Bot) NotifyAgentSpawn(ctx context.Context, bead BeadEvent) {
	// Guard: skip spawn for beads that are already closed (zombie prevention).
	// During normal operation this is a no-op (new beads are never closed).
	// During SSE replay after restart, this catches closed agents that slipped
	// past the dedup pre-population (CatchUpAgents only covers active agents).
	if bead.ID != "" {
		if detail, err := b.daemon.GetBead(ctx, bead.ID); err == nil && detail.Status == "closed" {
			b.logger.Debug("skipping spawn for closed agent bead", "id", bead.ID)
			return
		}
	}

	agent := bead.Assignee
	// The created event often lacks Assignee — reconstruct from fields.
	if agent == "" {
		if n := bead.Fields["agent"]; n != "" {
			agent = n
		}
	}
	if agent == "" {
		agent = bead.Title
	}
	if agent == "" {
		return
	}
	// Canonicalize to short name so all map keys are consistent across
	// events that may use full paths vs short names.
	agent = extractAgentName(agent)

	b.mu.Lock()
	b.agentState[agent] = "spawning"
	b.agentSeen[agent] = time.Now()
	if role := bead.Fields["role"]; role != "" {
		b.agentRole[agent] = role
	}
	b.mu.Unlock()

	// Fetch pod_name from the agent bead notes for coopmux terminal linking.
	b.fetchAndCachePodName(ctx, agent)

	// Thread-bound agents: skip the spawn notification here because
	// handleThreadSpawn already posted a confirmation in the thread
	// ("Spinning up..." or "Assigned a prewarmed agent...").
	// Posting again would create duplicate notifications.
	if slackChannel, slackTS := b.resolveAgentThread(ctx, agent); slackChannel != "" && slackTS != "" {
		b.logger.Debug("skipping spawn notification for thread-bound agent",
			"agent", agent, "channel", slackChannel, "thread_ts", slackTS)
		return
	}

	channel := b.resolveChannel(agent)

	if b.agentThreadingEnabled() {
		if _, err := b.ensureAgentCard(ctx, agent, channel); err != nil {
			b.logger.Error("failed to post agent spawn card",
				"agent", agent, "error", err)
		}
	} else {
		displayName := b.agentDisplayName(agent)
		spawnText := fmt.Sprintf(":rocket: *Agent spawned: %s*", displayName)
		if role := bead.Fields["role"]; role != "" {
			spawnText += fmt.Sprintf(" (%s)", role)
		}
		_, _, err := b.api.PostMessageContext(ctx, channel,
			slack.MsgOptionText(fmt.Sprintf("Agent spawned: %s", extractAgentName(agent)), false),
			slack.MsgOptionBlocks(
				slack.NewSectionBlock(
					slack.NewTextBlockObject("mrkdwn", spawnText, false, false),
					nil, nil),
			),
		)
		if err != nil {
			b.logger.Error("failed to post agent spawn message",
				"agent", agent, "error", err)
		}
	}
}

// NotifyAgentState is called when an agent bead's agent_state changes.
// It records the new state and refreshes the agent card in Slack.
func (b *Bot) NotifyAgentState(_ context.Context, bead BeadEvent) {
	agent := bead.Assignee
	// Mirror the identity reconstruction from NotifyAgentSpawn: updated events may
	// also lack Assignee when the bead was created without one.
	if agent == "" {
		if n := bead.Fields["agent"]; n != "" {
			agent = n
		}
	}
	if agent == "" {
		return
	}
	agent = extractAgentName(agent)
	state := bead.Fields["agent_state"]
	b.mu.Lock()
	prevState := b.agentState[agent]
	b.agentState[agent] = state
	b.agentSeen[agent] = time.Now()
	_, hasPod := b.agentPodName[agent]
	b.mu.Unlock()

	// Fetch pod_name if not yet cached (e.g., spawn event missed or reconnect).
	if !hasPod {
		b.fetchAndCachePodName(context.Background(), agent)
	}

	// Thread-bound agents: post terminal state transitions as thread replies.
	// Only post if the state actually changed to avoid duplicate notifications.
	// The "working" state is not posted — the spawn confirmation is sufficient.
	if (state == "done" || state == "failed") && state != prevState {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if slackChannel, slackTS := b.resolveAgentThread(ctx, agent); slackChannel != "" && slackTS != "" {
			b.postThreadStateReply(ctx, agent, state, bead, slackChannel, slackTS)
			// Keep the thread→agent mapping so future replies in this thread
			// can respawn the same agent with session resume (same name, same PVC).
			return
		}
	}

	// Refresh the card if one exists for this agent.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// For terminal states with a wrapup, replace the card with the wrapup summary.
	if (state == "done" || state == "failed") && bead.Fields["wrapup"] != "" {
		b.replaceAgentCardWithWrapUp(ctx, agent, state, bead.Fields["wrapup"])
	} else {
		b.updateAgentCard(ctx, agent)
	}
}

// postThreadStateReply updates the spawn confirmation message in-place with
// the agent's terminal state and wrapup. If no spawn message is tracked,
// falls back to posting a new reply.
func (b *Bot) postThreadStateReply(ctx context.Context, agent, state string, bead BeadEvent, channel, threadTS string) {
	var emoji, status string
	switch state {
	case "done":
		emoji = ":white_check_mark:"
		status = "finished"
	case "failed":
		emoji = ":x:"
		status = "failed"
	default:
		return // Only post for terminal states.
	}

	agentLink := b.agentThreadLink(agent)
	text := fmt.Sprintf("%s %s %s.", emoji, agentLink, status)

	// Append close reason if available.
	if reason := bead.Fields["close_reason"]; reason != "" {
		text += fmt.Sprintf("\n> %s", truncateText(reason, 500))
	}

	// Append structured wrapup if available.
	if wrapupJSON := bead.Fields["wrapup"]; wrapupJSON != "" {
		text += formatWrapUpSlack(wrapupJSON)
	}

	// Try to update the spawn confirmation message in-place.
	b.mu.Lock()
	spawnRef, hasSpawn := b.threadSpawnMsgs[agent]
	if hasSpawn {
		delete(b.threadSpawnMsgs, agent)
	}
	b.mu.Unlock()

	if hasSpawn {
		_, _, _, err := b.api.UpdateMessageContext(ctx, spawnRef.ChannelID, spawnRef.Timestamp,
			slack.MsgOptionText(text, false),
		)
		if err != nil {
			b.logger.Warn("failed to update spawn message, posting new reply",
				"agent", agent, "error", err)
		} else {
			return
		}
	}

	// Fallback: post as a new reply.
	_, _, err := b.api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(threadTS),
	)
	if err != nil {
		b.logger.Error("failed to post thread state reply",
			"agent", agent, "state", state, "error", err)
	}
}

// NotifyAgentTaskUpdate is called when a task bead assigned to an agent changes
// to in_progress (i.e., the agent claimed it). It refreshes any matching agent
// cards so the claimed task title appears without waiting for a pod phase change.
func (b *Bot) NotifyAgentTaskUpdate(_ context.Context, agentName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Canonicalize to short name — all map keys use short names.
	agent := extractAgentName(agentName)

	b.mu.Lock()
	_, hasCard := b.agentCards[agent]
	if hasCard {
		b.agentSeen[agent] = time.Now()
	}
	b.mu.Unlock()

	if hasCard {
		b.updateAgentCard(ctx, agent)
	}
}

// updateAgentCard updates the agent status card with the current pending count, state, and task.
func (b *Bot) updateAgentCard(ctx context.Context, agent string) {
	b.mu.Lock()
	ref, ok := b.agentCards[agent]
	pending := b.agentPending[agent]
	state := b.agentState[agent]
	seen := b.agentSeen[agent]
	podName := b.agentPodName[agent]
	imageTag := b.agentImageTag[agent]
	role := b.agentRole[agent]
	b.mu.Unlock()

	if !ok {
		// No card under this identity — create one if threading is enabled.
		// This handles identity drift (e.g., spawn used short name, updates use
		// full assignee path) by posting a card under the canonical identity.
		if b.agentThreadingEnabled() {
			channel := b.resolveChannel(agent)
			if _, err := b.ensureAgentCard(ctx, agent, channel); err != nil {
				b.logger.Error("failed to create agent card on state update",
					"agent", agent, "error", err)
			}
		}
		return
	}

	taskTitle := b.agentTaskTitle(ctx, agent)
	blocks := buildAgentCardBlocks(agent, pending, state, taskTitle, seen, b.coopmuxPublicURL, podName, imageTag, role)
	_, _, _, err := b.api.UpdateMessageContext(ctx, ref.ChannelID, ref.Timestamp,
		slack.MsgOptionText(fmt.Sprintf("Agent: %s", extractAgentName(agent)), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		b.logger.Error("failed to update agent card", "agent", agent, "error", err)
	}
}

// replaceAgentCardWithWrapUp updates the agent card in-place with the wrap-up
// summary instead of posting it as a thread reply. The card is replaced with a
// compact block showing the agent name, terminal state, and wrapup content.
func (b *Bot) replaceAgentCardWithWrapUp(ctx context.Context, agent, state, wrapupJSON string) {
	b.mu.Lock()
	ref, ok := b.agentCards[agent]
	b.mu.Unlock()

	if !ok {
		// No card — fall back to regular update (which may create one).
		b.updateAgentCard(ctx, agent)
		return
	}

	blocks := buildWrapUpAgentCardBlocks(agent, state, wrapupJSON)
	_, _, _, err := b.api.UpdateMessageContext(ctx, ref.ChannelID, ref.Timestamp,
		slack.MsgOptionText(fmt.Sprintf("Agent: %s (%s)", extractAgentName(agent), state), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		b.logger.Error("failed to replace agent card with wrapup",
			"agent", agent, "error", err)
	}
}

// buildWrapUpAgentCardBlocks constructs Block Kit blocks that replace the agent
// card with a wrap-up summary. The header shows the agent name and terminal
// state, followed by the wrapup content in a section block that Slack can
// truncate, and a Clear button for dismissal.
func buildWrapUpAgentCardBlocks(agent, agentState, wrapupJSON string) []slack.Block {
	name := extractAgentName(agent)

	var indicator string
	switch agentState {
	case "done":
		indicator = ":white_check_mark:"
	case "failed":
		indicator = ":x:"
	default:
		indicator = ":white_circle:"
	}

	headerText := fmt.Sprintf("%s *%s* · %s", indicator, name, agentState)
	wrapupText := formatWrapUpSlack(wrapupJSON)

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", headerText, false, false),
			nil, nil),
	}

	if wrapupText != "" {
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", wrapupText, false, false),
				nil, nil),
		)
	}

	clearBtn := slack.NewButtonBlockElement(
		"clear_agent",
		agent,
		slack.NewTextBlockObject("plain_text", "Clear", false, false),
	)
	blocks = append(blocks, slack.NewActionBlock("", clearBtn))

	return blocks
}

// buildAgentCardBlocks constructs Block Kit blocks for an agent status card.
// agentState is the agent's current lifecycle state (spawning, working, etc.).
// taskTitle is the title of the bead the agent currently has in_progress ("" if idle).
// seen is the last time activity was recorded for this agent (zero = unknown).
// coopmuxURL and podName are used to render the agent name as a clickable terminal link.
// imageTag is the deployed image tag (e.g., "v2026.58.3") shown in the context line.
// role is the agent's role (e.g., "crew", "lead", "ops") shown in the header.
func buildAgentCardBlocks(agent string, pendingCount int, agentState, taskTitle string, seen time.Time, coopmuxURL, podName, imageTag, role string) []slack.Block {
	name := extractAgentName(agent)
	project := extractAgentProject(agent)

	var indicator, status string
	switch {
	case pendingCount > 0:
		indicator = ":large_blue_circle:"
		status = fmt.Sprintf("%d pending", pendingCount)
	case agentState == "working":
		indicator = ":large_green_circle:"
		status = "working"
	case agentState == "spawning":
		indicator = ":hourglass_flowing_sand:"
		status = "starting"
	case agentState == "done":
		indicator = ":white_check_mark:"
		status = "done"
	case agentState == "failed":
		indicator = ":x:"
		status = "failed"
	case agentState == "rate_limited":
		indicator = ":warning:"
		status = "rate limited"
	default:
		indicator = ":white_circle:"
		status = "idle"
	}

	displayName := coopmuxAgentLink(coopmuxURL, podName, name)
	headerText := fmt.Sprintf("%s *%s*", indicator, displayName)
	if project != "" {
		headerText += fmt.Sprintf(" \u00b7 _%s_", project)
	}
	if role != "" {
		headerText += fmt.Sprintf(" \u00b7 %s", role)
	}
	headerText += fmt.Sprintf(" \u00b7 %s", status)

	contextText := fmt.Sprintf("`%s` \u00b7 Decisions thread below", agent)
	if imageTag != "" {
		contextText += fmt.Sprintf(" \u00b7 %s", imageTag)
	}
	if !seen.IsZero() {
		contextText += fmt.Sprintf(" \u00b7 _%s_", formatAge(seen))
	}
	if taskTitle != "" {
		contextText += fmt.Sprintf("\n:wrench: %s", truncateText(taskTitle, 200))
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", headerText, false, false),
			nil, nil),
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", contextText, false, false)),
	}

	// Show a Clear button for terminated agents so humans can dismiss them from Slack.
	if agentState == "done" || agentState == "failed" {
		clearBtn := slack.NewButtonBlockElement(
			"clear_agent",
			agent,
			slack.NewTextBlockObject("plain_text", "Clear", false, false),
		)
		blocks = append(blocks, slack.NewActionBlock("", clearBtn))
	}

	return blocks
}

// buildCompactAgentCardBlocks constructs a single-line Block Kit card for a
// dead/finished agent. This replaces the full multi-block card when the agent
// is no longer active, keeping the card visible (since thread replies persist)
// but taking minimal space. A "Clear" button allows manual removal.
func buildCompactAgentCardBlocks(agent, agentState string) []slack.Block {
	name := extractAgentName(agent)

	var indicator string
	switch agentState {
	case "done":
		indicator = ":white_check_mark:"
	case "failed":
		indicator = ":x:"
	default:
		indicator = ":white_circle:"
	}

	text := fmt.Sprintf("%s ~%s~ · %s", indicator, name, agentState)

	clearBtn := slack.NewButtonBlockElement(
		"clear_agent",
		agent,
		slack.NewTextBlockObject("plain_text", "Clear", false, false),
	)

	return []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", text, false, false),
			nil,
			slack.NewAccessory(clearBtn),
		),
	}
}

// fetchAndCachePodName fetches the agent bead from the daemon, extracts
// pod_name and image_tag from Notes, and caches them for coopmux terminal
// linking and agent card display respectively.
func (b *Bot) fetchAndCachePodName(ctx context.Context, agent string) {
	detail, err := b.daemon.FindAgentBead(ctx, agent)
	if err != nil {
		b.logger.Debug("could not fetch agent bead for pod_name", "agent", agent, "error", err)
		return
	}
	notes := beadsapi.ParseNotes(detail.Notes)
	podName := notes["pod_name"]
	imageTag := extractImageTag(notes["image_tag"])
	role := detail.Fields["role"]

	b.mu.Lock()
	if podName != "" {
		b.agentPodName[agent] = podName
	}
	if imageTag != "" {
		b.agentImageTag[agent] = imageTag
	}
	if role != "" {
		b.agentRole[agent] = role
	}
	b.mu.Unlock()
}

// extractImageTag extracts the tag portion from a container image reference.
// "ghcr.io/org/agent:v2026.58.3" → "v2026.58.3", "latest" → "latest", "" → "".
// If the input has no colon, it is returned as-is (already a bare tag).
func extractImageTag(image string) string {
	if image == "" {
		return ""
	}
	if i := strings.LastIndex(image, ":"); i >= 0 {
		return image[i+1:]
	}
	return image
}

// extractAgentProject returns the first segment (project) of an agent identity.
// "gasboat/crew/test-bot" → "gasboat", "test-bot" → ""
func extractAgentProject(identity string) string {
	if i := strings.Index(identity, "/"); i >= 0 {
		return identity[:i]
	}
	return ""
}

// resolveChannel returns the target Slack channel for an agent.
// Uses the router if configured, otherwise falls back to the default channel.
func (b *Bot) resolveChannel(agent string) string {
	if b.router != nil && agent != "" {
		result := b.router.Resolve(agent)
		if result.ChannelID != "" {
			return result.ChannelID
		}
	}
	return b.channel
}

// Agent kill, clear, respawn, and coop shutdown functions are in bot_agent_kill.go.
