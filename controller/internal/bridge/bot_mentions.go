package bridge

import (
	"bytes"
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

// handleAppMention processes @mention events.
//
// Routing priority:
//  1. If the first word after @gasboat matches a known agent name → route to that agent
//  2. If in a thread bound to an agent → route to that agent
//  3. If in a channel mapped to an agent via router → route to that agent
//  4. Otherwise → post ephemeral help message
func (b *Bot) handleAppMention(ctx context.Context, ev *slackevents.AppMentionEvent) {
	// Ignore bot-triggered mentions.
	if ev.BotID != "" {
		return
	}

	// Strip the bot mention from the message text.
	text := stripBotMention(ev.Text, b.botUserID)
	if text == "" {
		b.logger.Debug("app_mention ignored: empty after stripping mention",
			"channel", ev.Channel)
		return
	}

	var agent string
	replyTS := ev.ThreadTimeStamp // timestamp to thread the confirmation reply under
	agentSpawning := false // true if agent is still in spawning state

	// Priority 1: Check if the first word is an agent name.
	var agentPodName string // for coopmux terminal link
	if resolved, remaining := b.resolveAgentFromText(ctx, text); resolved != "" {
		// Validate the agent is active via FindAgentBead.
		agentBead, err := b.daemon.FindAgentBead(ctx, extractAgentName(resolved))
		if err != nil {
			b.logger.Info("mention: agent name resolved but not active",
				"agent", resolved, "channel", ev.Channel, "error", err)
			if b.api != nil {
				_, _ = b.api.PostEphemeral(ev.Channel, ev.User,
					slack.MsgOptionText(
						fmt.Sprintf(":x: No active agent named *%s*", extractAgentName(resolved)),
						false))
			}
			return
		}
		agent = resolved
		text = remaining
		agentPodName = beadsapi.ParseNotes(agentBead.Notes)["pod_name"]
		if agentBead.Fields["agent_state"] == "spawning" {
			agentSpawning = true
		}
		b.logger.Info("mention: resolved agent from text",
			"agent", agent, "channel", ev.Channel)
	}

	if agent == "" && ev.ThreadTimeStamp == "" {
		// Not in a thread — check if this channel is mapped to an agent via the router.
		// This handles "@gasboat <message>" in a dedicated agent channel.
		if b.router != nil {
			agent = b.router.GetAgentByChannel(ev.Channel)
		}
		if agent == "" {
			// No agent could be resolved — post an ephemeral help message.
			b.logger.Info("mention: no agent resolved for channel",
				"channel", ev.Channel, "user", ev.User)
			if b.api != nil {
				_, _ = b.api.PostEphemeral(ev.Channel, ev.User,
					slack.MsgOptionText(
						":thinking_face: No agent is mapped to this channel. "+
							"Try mentioning a specific agent: `@gasboat <agent-name> <message>`, "+
							"or mention me in a thread to spawn a thread-bound agent.",
						false))
			}
			return
		}
		// Use the mention's own timestamp as the reply anchor so the response
		// threads back to this message.
		replyTS = ev.TimeStamp
	} else if agent == "" {
		// In a thread — reverse-lookup which agent owns this thread.
		agent = b.getAgentByThread(ev.Channel, ev.ThreadTimeStamp)
		if agent == "" {
			// Before spawning, check if this channel is mapped to an agent via the router.
			// This handles @mentions in arbitrary threads within an agent's break-out channel —
			// route to the channel's agent instead of spawning a duplicate thread runner.
			if b.router != nil {
				agent = b.router.GetAgentByChannel(ev.Channel)
			}
		}
		if agent == "" {
			// Orphan thread in an unmapped channel — spawn a new ephemeral agent.
			b.handleThreadSpawn(ctx, ev, text)
			return
		}

		// Validate the thread-bound agent is still active (mirrors Priority 1 validation).
		agentBead, err := b.daemon.FindAgentBead(ctx, extractAgentName(agent))
		if err != nil {
			b.logger.Info("mention: thread-bound agent no longer active, respawning with session resume",
				"agent", agent, "channel", ev.Channel, "thread_ts", ev.ThreadTimeStamp, "error", err)
			// Agent is gone — respawn the SAME agent name so the entrypoint
			// finds the existing session JSONL and PVC for session continuity.
			b.respawnThreadAgent(ctx, ev.Channel, ev.ThreadTimeStamp, agent, text)
			return
		}
		agentPodName = beadsapi.ParseNotes(agentBead.Notes)["pod_name"]
		if agentBead.Fields["agent_state"] == "spawning" {
			agentSpawning = true
		}
	}

	if replyTS == "" {
		replyTS = ev.TimeStamp
	}

	// Canonicalize to short name so map lookups (agentPodName, etc.)
	// and stored refs use consistent keys.
	agent = extractAgentName(agent)

	// Resolve sender display name.
	username := ev.User
	if user, err := b.api.GetUserInfo(ev.User); err == nil {
		if user.RealName != "" {
			username = user.RealName
		} else if user.Name != "" {
			username = user.Name
		}
	}

	// Build bead title and description with slack metadata tag.
	title := truncateText(fmt.Sprintf("Mention: %s", text), 80)
	slackTag := fmt.Sprintf("[slack:%s:%s]", ev.Channel, replyTS)
	description := fmt.Sprintf("Mention from %s in Slack:\n\n%s\n\n---\n%s", username, text, slackTag)

	// Enrich with file attachments if present.
	files := b.fetchMessageFiles(ctx, ev.Channel, ev.TimeStamp)
	description += formatAttachmentsSection(files)

	// Build fields JSON with attachment metadata.
	var fieldsJSON json.RawMessage
	if fileFields := slackFilesToFields(files); fileFields != nil {
		fieldsJSON, _ = json.Marshal(fileFields)
	}

	// Create tracking bead assigned to the agent.
	beadID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       title,
		Type:        "task",
		Kind:        "issue",
		Description: description,
		Assignee:    extractAgentName(agent),
		Labels:      []string{"slack-mention"},
		Priority:    2,
		Fields:      fieldsJSON,
	})
	if err != nil {
		b.logger.Error("failed to create mention bead",
			"channel", ev.Channel, "agent", agent, "error", err)
		return
	}

	b.logger.Info("mention: created tracking bead",
		"bead", beadID, "agent", agent, "user", username)

	// Persist message ref for response relay.
	if b.state != nil {
		_ = b.state.SetChatMessage(beadID, MessageRef{
			ChannelID: ev.Channel,
			Timestamp: replyTS,
			Agent:     agent,
		})
	}

	// Nudge the agent via coop.
	nudgeErr := b.nudgeAgentForMention(ctx, agent, text, beadID)

	// Post confirmation threaded under the original message.
	// If pod name wasn't set from FindAgentBead (text resolution), try the cache.
	if agentPodName == "" {
		b.mu.Lock()
		agentPodName = b.agentPodName[agent]
		b.mu.Unlock()
	}
	agentLink := coopmuxAgentLink(b.coopmuxPublicURL, agentPodName, "agent")
	var confirmText string
	if nudgeErr != nil && agentSpawning {
		confirmText = fmt.Sprintf(":hourglass_flowing_sand: %s is still starting up — it will pick up this task when ready (tracking: `%s`)", agentLink, beadID)
	} else if nudgeErr != nil {
		confirmText = fmt.Sprintf(":warning: Created task for %s but nudge failed (tracking: `%s`). The agent will pick it up on its next cycle.", agentLink, beadID)
	} else {
		confirmText = fmt.Sprintf(":mega: Forwarded to %s (tracking: `%s`)", agentLink, beadID)
	}
	_, _, _ = b.api.PostMessage(ev.Channel,
		slack.MsgOptionText(confirmText, false),
		slack.MsgOptionTS(replyTS),
	)
}

// getAgentByThread reverse-maps (channel, thread_ts) to an agent identity
// by checking the threadAgents map, agentCards hot cache, and falling back
// to persisted state. Used for @mention routing where all thread types apply.
func (b *Bot) getAgentByThread(channelID, threadTS string) string {
	// Check direct thread→agent mapping first (thread-spawned agents).
	if b.state != nil {
		if agent, ok := b.state.GetThreadAgent(channelID, threadTS); ok {
			return agent
		}
	}

	// Check agentCards hot cache (agent card threads).
	b.mu.Lock()
	for agent, ref := range b.agentCards {
		if ref.ChannelID == channelID && ref.Timestamp == threadTS {
			b.mu.Unlock()
			return agent
		}
	}
	b.mu.Unlock()

	// Fall back to persisted agentCards state.
	if b.state != nil {
		for agent, ref := range b.state.AllAgentCards() {
			if ref.ChannelID == channelID && ref.Timestamp == threadTS {
				return agent
			}
		}
	}
	return ""
}

// getThreadSpawnedAgent returns the agent bound to a thread only if it was
// explicitly spawned for that thread (via SetThreadAgent). Unlike getAgentByThread,
// this does NOT check agent card threads — those are status/notification threads
// where casual replies should not be auto-forwarded without an @mention.
func (b *Bot) getThreadSpawnedAgent(channelID, threadTS string) string {
	if b.state != nil {
		if agent, ok := b.state.GetThreadAgent(channelID, threadTS); ok {
			return agent
		}
	}
	return ""
}

// resolveAgentFromText checks if the first word of text matches a known active
// agent name. Matching is case-insensitive and supports both bare names
// ("crew-k8s") and project-qualified names ("gasboat/crew/k8s").
// Returns the matched agent identity and the remaining text (with agent name
// stripped), or ("", text) if no match.
func (b *Bot) resolveAgentFromText(ctx context.Context, text string) (string, string) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return "", text
	}
	candidate := words[0]

	agents, err := b.daemon.ListAgentBeads(ctx)
	if err != nil {
		b.logger.Debug("failed to list agents for name resolution", "error", err)
		return "", text
	}

	candidateLower := strings.ToLower(candidate)
	for _, a := range agents {
		// Match against the short agent name (e.g., "crew-k8s").
		if strings.EqualFold(a.AgentName, candidateLower) {
			remaining := strings.TrimSpace(strings.TrimPrefix(text, candidate))
			return a.Title, remaining
		}
		// Match against the full title/identity (e.g., "crew-gasboat-crew-k8s").
		if strings.EqualFold(a.Title, candidateLower) {
			remaining := strings.TrimSpace(strings.TrimPrefix(text, candidate))
			return a.Title, remaining
		}
	}
	return "", text
}

// parseListenFlag checks for a --listen flag in mention text.
// If present, returns (true, text_without_flag). Otherwise returns (false, text).
// When --listen is used on a thread spawn, the agent receives ALL thread replies,
// not just @mentions.
func parseListenFlag(text string) (bool, string) {
	words := strings.Fields(text)
	filtered := make([]string, 0, len(words))
	found := false
	for _, w := range words {
		if w == "--listen" {
			found = true
			continue
		}
		filtered = append(filtered, w)
	}
	if found {
		return true, strings.Join(filtered, " ")
	}
	return false, text
}

// parseProjectOverride extracts a project override from mention text.
// Supports two syntaxes:
//   - "project:gasboat fix the helm chart" → ("gasboat", "fix the helm chart")
//   - "--project gasboat fix the helm chart" → ("gasboat", "fix the helm chart")
//
// Returns ("", text) if no override is found.
func parseProjectOverride(text string) (string, string) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return "", text
	}

	// Syntax 1: project:<name>
	if project, ok := strings.CutPrefix(words[0], "project:"); ok && project != "" {
		remaining := strings.TrimSpace(strings.Join(words[1:], " "))
		return project, remaining
	}

	// Syntax 2: --project <name>
	if words[0] == "--project" && len(words) >= 2 {
		project := words[1]
		remaining := strings.TrimSpace(strings.Join(words[2:], " "))
		return project, remaining
	}

	return "", text
}

// handleThreadSpawn spawns an ephemeral agent bound to a Slack thread when
// @gasboat is mentioned in a thread with no existing agent binding.
func (b *Bot) handleThreadSpawn(ctx context.Context, ev *slackevents.AppMentionEvent, text string) {
	channel := ev.Channel
	threadTS := ev.ThreadTimeStamp

	// Guard: prevent spawning duplicate agents for the same thread.
	// Another mention may have raced us, or the state may have been set
	// between the caller's check and this point.
	if b.state != nil {
		if agent, ok := b.state.GetThreadAgent(channel, threadTS); ok {
			b.logger.Info("thread-spawn: agent already bound to thread, skipping",
				"channel", channel, "thread_ts", threadTS, "agent", agent)
			if b.api != nil {
				_, _, _ = b.api.PostMessage(channel,
					slack.MsgOptionText(
						fmt.Sprintf(":information_source: An agent (*%s*) is already working in this thread.", extractAgentName(agent)),
						false),
					slack.MsgOptionTS(threadTS),
				)
			}
			return
		}
	}

	// Check for --listen flag (auto-forward all thread replies without @mention).
	listen, text := parseListenFlag(text)

	// Check for explicit project override in the mention text.
	// Supports "project:<name>" and "--project <name>" syntax.
	projectOverride, text := parseProjectOverride(text)

	// Fetch thread context from Slack.
	threadContext := b.fetchThreadContext(ctx, channel, threadTS)

	// Generate a unique agent name based on the thread timestamp.
	agentName := "thread-" + sanitizeTS(threadTS)

	// Use explicit project override if provided, otherwise infer from channel.
	project := projectOverride
	if project == "" {
		project = b.projectFromChannel(ctx, channel)
	}
	b.logger.Info("thread-spawn: project resolution",
		"channel", channel, "project", project, "override", projectOverride)
	if project == "" && b.router != nil {
		if mapped := b.router.GetAgentByChannel(channel); mapped != "" {
			project = projectFromAgentIdentity(mapped)
		}
	}

	// Build agent description with thread context.
	description := fmt.Sprintf("Thread-spawned agent for Slack thread.\n\n"+
		"## Thread Context\n\n%s\n\n---\n"+
		"Triggered by: %s", threadContext, text)
	description = truncateText(description, 4000)

	// Try to assign a prewarmed agent from the pool first.
	// This avoids the ~60-120s cold-start time for new agent pods.
	if beadID, assignedAgent := b.tryPoolAssign(ctx, channel, threadTS, description, project); beadID != "" {
		b.logger.Info("thread-spawn: assigned prewarmed agent",
			"bead", beadID, "agent", assignedAgent,
			"channel", channel, "thread_ts", threadTS, "listen", listen)

		if b.state != nil {
			_ = b.state.SetThreadAgent(channel, threadTS, assignedAgent)
			if listen {
				_ = b.state.SetListenThread(channel, threadTS)
			}
		}
		if b.api != nil {
			_, msgTS, _ := b.api.PostMessage(channel,
				slack.MsgOptionText(
					fmt.Sprintf(":zap: Assigned a prewarmed agent — should be ready in seconds! (tracking: `%s`)", beadID),
					false),
				slack.MsgOptionTS(threadTS),
			)
			if msgTS != "" {
				b.mu.Lock()
				b.threadSpawnMsgs[extractAgentName(assignedAgent)] = MessageRef{
					ChannelID: channel, Timestamp: msgTS, ThreadTS: threadTS,
				}
				b.mu.Unlock()
			}
		}
		return
	}

	// Fallback: cold-start a new agent pod.
	// Build fields including thread metadata.
	fields := map[string]string{
		"agent":                agentName,
		"mode":                 "job",
		"role":                 "thread",
		"project":              project,
		"slack_thread_channel": channel,
		"slack_thread_ts":      threadTS,
		"spawn_source":         "slack-thread",
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		b.logger.Error("failed to marshal agent fields", "error", err)
		return
	}

	labels := []string{"slack-thread"}
	if project != "" {
		labels = append(labels, "project:"+project)
	}

	beadID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       agentName,
		Type:        "agent",
		Description: description,
		Fields:      json.RawMessage(fieldsJSON),
		Labels:      labels,
	})
	if err != nil {
		b.logger.Error("failed to create thread-spawned agent bead",
			"channel", channel, "thread_ts", threadTS, "error", err)
		return
	}

	b.logger.Info("thread-spawn: created agent bead (cold-start)",
		"bead", beadID, "agent", agentName,
		"channel", channel, "thread_ts", threadTS)

	// Record thread→agent mapping in state.
	if b.state != nil {
		_ = b.state.SetThreadAgent(channel, threadTS, agentName)
		if listen {
			_ = b.state.SetListenThread(channel, threadTS)
		}
	}

	// Post confirmation reply in thread.
	if b.api != nil {
		_, msgTS, _ := b.api.PostMessage(channel,
			slack.MsgOptionText(
				fmt.Sprintf(":zap: Spinning up an agent to help here... (tracking: `%s`)", beadID),
				false),
			slack.MsgOptionTS(threadTS),
		)
		if msgTS != "" {
			b.mu.Lock()
			b.threadSpawnMsgs[extractAgentName(agentName)] = MessageRef{
				ChannelID: channel, Timestamp: msgTS, ThreadTS: threadTS,
			}
			b.mu.Unlock()
		}
	}
}

// tryPoolAssign attempts to assign a prewarmed agent from the controller's pool.
// Returns (beadID, agentName) on success, or ("", "") if the pool is unavailable
// or empty. This is a best-effort optimization — callers should fall back to
// cold-start on failure.
func (b *Bot) tryPoolAssign(ctx context.Context, channel, threadTS, description, project string) (string, string) {
	if b.controllerURL == "" {
		return "", ""
	}

	reqBody, err := json.Marshal(map[string]string{
		"channel":     channel,
		"thread_ts":   threadTS,
		"description": description,
		"project":     project,
	})
	if err != nil {
		return "", ""
	}

	assignURL := strings.TrimRight(b.controllerURL, "/") + "/api/v1/pool/assign"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, assignURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", ""
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		b.logger.Debug("pool assign request failed", "error", err)
		return "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b.logger.Debug("pool assign returned non-200", "status", resp.StatusCode)
		return "", ""
	}

	var result struct {
		BeadID    string `json:"bead_id"`
		AgentName string `json:"agent_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.logger.Warn("pool assign: failed to decode response", "error", err)
		return "", ""
	}

	return result.BeadID, result.AgentName
}

// fetchThreadContext retrieves thread messages from Slack, filtering out bot
// messages to keep the context clean for the new agent.
func (b *Bot) fetchThreadContext(ctx context.Context, channel, threadTS string) string {
	msgs, _, _, err := b.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: threadTS,
		Limit:     50,
	})
	if err != nil {
		b.logger.Error("failed to fetch thread context",
			"channel", channel, "thread_ts", threadTS, "error", err)
		return "(could not fetch thread context)"
	}

	var buf strings.Builder
	for _, msg := range msgs {
		// Skip bot messages to keep the prompt clean.
		if msg.BotID != "" || msg.SubType == "bot_message" {
			continue
		}
		author := msg.User
		if author == "" {
			author = msg.Username
		}
		line := fmt.Sprintf("**%s**: %s\n", author, msg.Text)
		// Annotate messages that have file attachments.
		for _, f := range msg.Files {
			line += fmt.Sprintf("  [attachment: %s (%s) — /api/slack/files/%s]\n", f.Name, f.Mimetype, f.ID)
		}
		if buf.Len()+len(line) > 3000 {
			buf.WriteString("\n_(thread truncated)_\n")
			break
		}
		buf.WriteString(line)
	}

	if buf.Len() == 0 {
		return "(empty thread)"
	}
	return buf.String()
}

// sanitizeTS converts a Slack timestamp like "1234567890.123456" to a safe
// identifier fragment "1234567890-123456".
func sanitizeTS(ts string) string {
	return strings.ReplaceAll(ts, ".", "-")
}

// projectFromAgentIdentity extracts the project name from an agent identity
// like "gasboat/crew/agent-name" → "gasboat". Returns "" if not project-qualified.
func projectFromAgentIdentity(identity string) string {
	parts := strings.Split(identity, "/")
	if len(parts) >= 2 {
		return parts[0]
	}
	return ""
}

// resolveAgentThread checks if the given agent is thread-bound (spawned from a
// Slack thread). Returns the thread's channel and timestamp if so, or empty
// strings for regular agents. Validates that both fields are non-empty and
// well-formed before returning them.
func (b *Bot) resolveAgentThread(ctx context.Context, agent string) (channel, threadTS string) {
	agentName := extractAgentName(agent)
	detail, err := b.daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		b.logger.Debug("resolveAgentThread: agent bead not found",
			"agent", agentName, "error", err)
		return "", ""
	}
	ch := detail.Fields["slack_thread_channel"]
	ts := detail.Fields["slack_thread_ts"]
	if !isValidThreadBinding(ch, ts) {
		return "", ""
	}
	return ch, ts
}

// isValidThreadBinding validates that a Slack thread binding has both a
// non-empty channel ID and a well-formed timestamp (contains a dot separator).
func isValidThreadBinding(channel, threadTS string) bool {
	if channel == "" || threadTS == "" {
		return false
	}
	// Slack channel IDs start with C, D, or G.
	if len(channel) < 2 {
		return false
	}
	// Slack timestamps are "seconds.microseconds" format.
	if !strings.Contains(threadTS, ".") {
		return false
	}
	return true
}


// stripBotMention removes all <@BOTID> occurrences from text and trims whitespace.
func stripBotMention(text, botUserID string) string {
	mention := fmt.Sprintf("<@%s>", botUserID)
	text = strings.ReplaceAll(text, mention, "")
	return strings.TrimSpace(text)
}

// nudgeAgentForMention delivers a mention nudge to the target agent with retry.
func (b *Bot) nudgeAgentForMention(ctx context.Context, agent, text, beadID string) error {
	agentName := extractAgentName(agent)

	message := fmt.Sprintf("Slack mention (bead %s): %s", beadID, text)
	client := &http.Client{Timeout: 10 * time.Second}
	if err := NudgeAgent(ctx, b.daemon, client, b.logger, agentName, message); err != nil {
		b.logger.Error("failed to nudge agent for mention",
			"agent", agentName, "bead", beadID, "error", err)
		return err
	}

	b.logger.Info("nudged agent for mention",
		"agent", agentName, "bead", beadID)
	return nil
}
