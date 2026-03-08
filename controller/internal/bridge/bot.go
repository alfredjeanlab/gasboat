// Package bridge provides the Slack Socket Mode bot for the slack-bridge.
//
// Bot wraps the SlackNotifier and adds Socket Mode for real-time events,
// slash commands, and interactive modals. It runs alongside the SSE stream
// for bead lifecycle events.
//
// The Bot implementation is split across several files:
//   - bot.go — core struct, event dispatch, helpers
//   - bot_agents.go — agent card management and lifecycle operations
//   - bot_commands.go — slash command handlers (/spawn, /decisions, /roster)
//   - bot_decisions.go — decision notifications, modals, resolve/dismiss
//   - bot_decisions_modal.go — decision modal rendering
//   - bot_mentions.go — @mention handling in agent threads
//   - bot_notifications.go — agent crash, jack on/off/expired alerts
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Bot is the Slack Socket Mode bot that handles interactive events.
type Bot struct {
	api    *slack.Client
	socket *socketmode.Client
	state  *StateManager
	daemon BeadClient
	router *Router
	logger *slog.Logger

	channel   string // default channel ID
	botUserID string // bot's own user ID (set on connect)

	// Health state.
	connected        atomic.Bool
	numConnections   atomic.Int32

	// Threading mode: "" / "flat" = flat messages, "agent" = threaded under agent cards.
	threadingMode string

	// GitHub client for /unreleased command.
	github        *GitHubClient
	repos         []RepoRef
	version       string
	controllerURL string
	imageConfigs  []ImageTrackConfig

	// IP whitelist management (optional).
	bouncer *Bouncer

	// Coopmux terminal link support.
	coopmuxPublicURL string // e.g., "https://gasboat.app.e2e.dev.fics.ai/mux"

	// In-memory decision tracking (augments StateManager).
	mu           sync.Mutex
	messages     map[string]MessageRef // bead ID → Slack message ref (hot cache)
	agentCards   map[string]MessageRef // agent identity → status card ref (hot cache)
	agentPending map[string]int        // agent identity → pending decision count
	agentState   map[string]string     // agent identity → last known agent_state
	agentSeen    map[string]time.Time  // agent identity → last activity timestamp
	agentPodName  map[string]string    // agent identity → pod hostname (coopmux session ID)
	agentImageTag map[string]string   // agent identity → deployed image tag
	agentRole     map[string]string   // agent identity → role (e.g., "crew", "lead", "ops")
	threadSpawnMsgs  map[string]MessageRef // agent identity → spawn confirmation message ref (for in-place update)
	beadMsgs         map[string]MessageRef // "agent:beadID" → Slack message ref for inline bead status updates

	// Nudge throttling for thread reply forwarding.
	// Key: "agent:thread_ts", value: last nudge time.
	lastThreadNudge map[string]time.Time
}

// BotConfig holds configuration for the Socket Mode bot.
type BotConfig struct {
	BotToken       string
	AppToken       string
	Channel        string
	ThreadingMode  string // "agent" (default) or "flat" — controls decision threading
	Daemon         BeadClient
	State          *StateManager
	Router         *Router // optional channel router; nil = all to Channel
	Logger         *slog.Logger
	Debug          bool

	// GitHub /unreleased command support.
	GitHubToken   string
	Repos         []RepoRef
	Version       string
	ControllerURL string
	ImageConfigs  []ImageTrackConfig

	// Bouncer is the optional IP whitelist manager for Traefik middlewares.
	Bouncer *Bouncer

	// CoopmuxPublicURL is the public base URL for the coopmux terminal dashboard
	// (e.g., "https://gasboat.app.e2e.dev.fics.ai/mux"). When set, agent names
	// in Slack are rendered as clickable links to their terminal sessions.
	CoopmuxPublicURL string
}

// NewBot creates a new Socket Mode bot.
func NewBot(cfg BotConfig) *Bot {
	api := slack.New(
		cfg.BotToken,
		slack.OptionAppLevelToken(cfg.AppToken),
	)

	socket := socketmode.New(
		api,
		socketmode.OptionDebug(cfg.Debug),
	)

	var gh *GitHubClient
	if cfg.GitHubToken != "" || len(cfg.Repos) > 0 {
		gh = NewGitHubClient(cfg.GitHubToken, cfg.Logger)
	}

	b := &Bot{
		api:              api,
		socket:           socket,
		state:            cfg.State,
		daemon:           cfg.Daemon,
		router:           cfg.Router,
		bouncer:          cfg.Bouncer,
		logger:           cfg.Logger,
		channel:          cfg.Channel,
		threadingMode:    cfg.ThreadingMode,
		coopmuxPublicURL: strings.TrimRight(cfg.CoopmuxPublicURL, "/"),
		messages:         make(map[string]MessageRef),
		agentCards:       make(map[string]MessageRef),
		agentPending:     make(map[string]int),
		agentState:       make(map[string]string),
		agentSeen:        make(map[string]time.Time),
		agentPodName:     make(map[string]string),
		agentImageTag:    make(map[string]string),
		agentRole:        make(map[string]string),
		threadSpawnMsgs:  make(map[string]MessageRef),
		beadMsgs:         make(map[string]MessageRef),
		lastThreadNudge: make(map[string]time.Time),
		github:           gh,
		repos:            cfg.Repos,
		version:          cfg.Version,
		controllerURL:    cfg.ControllerURL,
		imageConfigs:     cfg.ImageConfigs,
	}

	// Hydrate hot caches from persisted state.
	// Canonicalize agent identity keys to short names during hydration
	// to prevent identity drift (full path vs short name).
	if cfg.State != nil {
		for id, ref := range cfg.State.AllDecisionMessages() {
			b.messages[id] = ref
		}
		for agent, ref := range cfg.State.AllAgentCards() {
			b.agentCards[extractAgentName(agent)] = ref
		}
	}

	// Count pending decisions per agent from hydrated messages.
	if b.agentThreadingEnabled() {
		for _, ref := range b.messages {
			if ref.Agent != "" {
				b.agentPending[extractAgentName(ref.Agent)]++
			}
		}
	}

	return b
}

// API returns the underlying Slack API client for direct API calls.
func (b *Bot) API() *slack.Client {
	return b.api
}

// IsConnected returns the bot's connection status.
func (b *Bot) IsConnected() bool {
	return b.connected.Load()
}

// NumConnections returns the number of active socket connections.
func (b *Bot) NumConnections() int {
	return int(b.numConnections.Load())
}

// Run starts the Socket Mode event loop. Blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	// Get bot user ID.
	auth, err := b.api.AuthTest()
	if err != nil {
		return fmt.Errorf("Slack auth test: %w", err)
	}
	b.botUserID = auth.UserID
	b.logger.Info("Slack bot authenticated", "user_id", b.botUserID, "team", auth.Team)

	// Prune agent cards for agents that are no longer active (done/failed/closed).
	// This prevents stale cards from reappearing after bot restarts.
	b.pruneStaleAgentCards(ctx)

	// Start periodic pruning to catch zombie cards from crashed agent pods.
	go b.startPeriodicPrune(ctx)

	go b.handleEvents(ctx)

	err = b.socket.RunContext(ctx)
	b.connected.Store(false)
	return err
}

// handleEvents processes Socket Mode events.
func (b *Bot) handleEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-b.socket.Events:
			if !ok {
				return
			}
			b.handleEvent(ctx, evt)
		}
	}
}

func (b *Bot) handleEvent(ctx context.Context, evt socketmode.Event) {
	b.logger.Debug("socket mode event received", "type", string(evt.Type))

	switch evt.Type {
	case socketmode.EventTypeConnecting:
		b.logger.Info("Slack Socket Mode connecting")

	case socketmode.EventTypeHello:
		if evt.Request != nil && evt.Request.NumConnections > 0 {
			b.numConnections.Store(int32(evt.Request.NumConnections))
			if evt.Request.NumConnections > 1 {
				b.logger.Warn("multiple Socket Mode connections detected",
					"num_connections", evt.Request.NumConnections)
			}
		}

	case socketmode.EventTypeConnected:
		b.connected.Store(true)
		b.logger.Info("Slack Socket Mode connected")

	case socketmode.EventTypeConnectionError:
		b.connected.Store(false)
		b.logger.Error("Slack Socket Mode connection error")

	case socketmode.EventTypeEventsAPI:
		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		b.socket.Ack(*evt.Request)
		b.handleEventsAPI(ctx, eventsAPIEvent)

	case socketmode.EventTypeInteractive:
		callback, ok := evt.Data.(slack.InteractionCallback)
		if !ok {
			return
		}
		b.handleInteraction(ctx, evt, callback)

	case socketmode.EventTypeSlashCommand:
		cmd, ok := evt.Data.(slack.SlashCommand)
		if !ok {
			return
		}
		b.socket.Ack(*evt.Request)
		b.handleSlashCommand(ctx, cmd)
	}
}

// handleEventsAPI processes Events API events received via Socket Mode.
func (b *Bot) handleEventsAPI(ctx context.Context, event slackevents.EventsAPIEvent) {
	switch event.Type {
	case slackevents.CallbackEvent:
		innerEvent := event.InnerEvent
		switch ev := innerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			b.handleMessageEvent(ctx, ev)
		case *slackevents.AppMentionEvent:
			b.handleAppMention(ctx, ev)
		}
	}
}

// handleMessageEvent processes Slack message events (for thread replies and chat forwarding).
func (b *Bot) handleMessageEvent(ctx context.Context, ev *slackevents.MessageEvent) {
	// Ignore bot's own messages and message subtypes (edits, deletes, etc.)
	if ev.User == b.botUserID || ev.User == "" || ev.SubType != "" {
		return
	}

	// Thread reply handling.
	if ev.ThreadTimeStamp != "" && ev.ThreadTimeStamp != ev.TimeStamp {
		// Decision thread replies (resolve the decision).
		b.handleThreadReply(ctx, ev)

		isMention := strings.Contains(ev.Text, fmt.Sprintf("<@%s>", b.botUserID))

		// In private channels (group), Slack does not send a separate
		// app_mention event — the message event is all we get. Synthesize
		// an AppMentionEvent so the mention handler fires.
		if isMention && ev.ChannelType == "group" {
			b.handleAppMention(ctx, &slackevents.AppMentionEvent{
				User:            ev.User,
				Text:            ev.Text,
				TimeStamp:       ev.TimeStamp,
				ThreadTimeStamp: ev.ThreadTimeStamp,
				Channel:         ev.Channel,
				BotID:           ev.BotID,
			})
			return
		}

		// Forward to bound agent only if this is a thread-spawned agent thread
		// with --listen mode enabled. Without --listen, thread agents require
		// an @mention (same as agent card threads).
		// Skip messages containing @mention — those are handled by app_mention event.
		if !isMention {
			if agent := b.getThreadSpawnedAgent(ev.Channel, ev.ThreadTimeStamp); agent != "" {
				// Only auto-forward if --listen was set on the original spawn mention.
				if b.state != nil && b.state.IsListenThread(ev.Channel, ev.ThreadTimeStamp) {
					b.handleThreadForward(ctx, ev, agent)
				} else {
					b.hintMentionRequired(ctx, ev.Channel, ev.ThreadTimeStamp, agent)
				}
			} else if agent := b.getAgentByThread(ev.Channel, ev.ThreadTimeStamp); agent != "" {
				b.hintMentionRequired(ctx, ev.Channel, ev.ThreadTimeStamp, agent)
			}
		}
		return
	}

	// In private channels (group), Slack does not send app_mention events.
	// Synthesize one for top-level mentions so the bot responds.
	if ev.ChannelType == "group" && strings.Contains(ev.Text, fmt.Sprintf("<@%s>", b.botUserID)) {
		b.handleAppMention(ctx, &slackevents.AppMentionEvent{
			User:      ev.User,
			Text:      ev.Text,
			TimeStamp: ev.TimeStamp,
			Channel:   ev.Channel,
			BotID:     ev.BotID,
		})
		return
	}

	// Non-mention messages in non-thread contexts are ignored.
	// Users must @mention the bot to interact with agents.
}

// hintMentionRequired posts a one-time hint in an agent card thread when a
// user posts without @mentioning the bot. Throttled to at most once per thread
// per 10 minutes to avoid spam.
func (b *Bot) hintMentionRequired(ctx context.Context, channel, threadTS, agent string) {
	key := "hint:" + channel + ":" + threadTS
	now := time.Now()

	b.mu.Lock()
	if last, ok := b.lastThreadNudge[key]; ok && now.Sub(last) < 10*time.Minute {
		b.mu.Unlock()
		return
	}
	b.lastThreadNudge[key] = now
	b.mu.Unlock()

	hint := fmt.Sprintf(":bulb: _Tip: @mention me so *%s* sees your message._", agent)
	if b.api != nil {
		_, _, _ = b.api.PostMessageContext(ctx, channel,
			slack.MsgOptionText(hint, false),
			slack.MsgOptionTS(threadTS),
		)
	}
}

// extractAgentName returns the last segment of an agent identity.
// "gasboat/crew/test-bot" → "test-bot", "test-bot" → "test-bot"
func extractAgentName(identity string) string {
	if i := strings.LastIndex(identity, "/"); i >= 0 {
		return identity[i+1:]
	}
	return identity
}

// beadTitle returns the human-readable title for a bead, falling back to the
// bead ID when the title is empty. Use this instead of raw bead IDs in
// human-visible Slack messages.
func beadTitle(id, title string) string {
	if title != "" {
		return title
	}
	return id
}

// formatAge formats the duration since t as a compact human-readable string.
// Examples: "just now", "2m ago", "1h ago", "3d ago".
func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// truncateText truncates s to maxLen, appending "..." if truncated.
func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// handleThreadReply processes replies to decision threads.
func (b *Bot) handleThreadReply(ctx context.Context, ev *slackevents.MessageEvent) {
	// Find which decision this thread belongs to.
	beadID := b.getDecisionByThread(ev.Channel, ev.ThreadTimeStamp)
	if beadID == "" {
		return // Not a decision thread we're tracking
	}

	// Get user info for attribution.
	user, err := b.api.GetUserInfo(ev.User)
	username := ev.User
	if err == nil {
		if user.RealName != "" {
			username = user.RealName
		} else if user.Name != "" {
			username = user.Name
		}
	}

	// Try to resolve the decision with the thread reply text.
	bead, err := b.daemon.GetBead(ctx, beadID)
	if err != nil {
		b.logger.Error("failed to get decision for thread reply", "bead", beadID, "error", err)
		return
	}

	// If decision is still open, resolve it with the reply text.
	if bead.Status == "open" || bead.Status == "in_progress" {
		fields := map[string]string{
			"chosen":    ev.Text,
			"rationale": fmt.Sprintf("Thread reply by %s via Slack", username),
		}
		if err := b.daemon.CloseBead(ctx, beadID, fields); err != nil {
			b.logger.Error("failed to resolve decision via thread reply",
				"bead", beadID, "error", err)
			return
		}
		// Confirm in thread.
		_, _, _ = b.api.PostMessage(ev.Channel,
			slack.MsgOptionText(fmt.Sprintf(":white_check_mark: Decision resolved by %s", username), false),
			slack.MsgOptionTS(ev.ThreadTimeStamp),
		)
		b.logger.Info("decision resolved via thread reply",
			"bead", beadID, "user", username)
	}
}

// getDecisionByThread reverse-maps (channel, thread_ts) to a bead ID.
func (b *Bot) getDecisionByThread(channelID, threadTS string) string {
	if b.state == nil {
		return ""
	}
	for id, ref := range b.state.AllDecisionMessages() {
		if ref.ChannelID == channelID && ref.Timestamp == threadTS {
			return id
		}
	}
	return ""
}

// handleInteraction processes interactive component callbacks (buttons, modals).
func (b *Bot) handleInteraction(ctx context.Context, evt socketmode.Event, callback slack.InteractionCallback) {
	switch callback.Type {
	case slack.InteractionTypeBlockActions:
		b.socket.Ack(*evt.Request)
		b.handleBlockActions(ctx, callback)

	case slack.InteractionTypeViewSubmission:
		b.socket.Ack(*evt.Request)
		b.handleViewSubmission(ctx, callback)

	default:
		b.logger.Debug("unhandled interaction type", "type", callback.Type)
	}
}

// lookupMessage finds a tracked decision message by bead ID.
// Checks hot cache first, then falls back to state manager.
func (b *Bot) lookupMessage(beadID string) (MessageRef, bool) {
	b.mu.Lock()
	ref, ok := b.messages[beadID]
	b.mu.Unlock()
	if ok {
		return ref, true
	}
	if b.state != nil {
		return b.state.GetDecisionMessage(beadID)
	}
	return MessageRef{}, false
}

// updateMessageResolved updates the original Slack message to show resolved state.
// It tries the provided channelID/messageTS first (from modal metadata), then falls
// back to the hot cache / state manager.
func (b *Bot) updateMessageResolved(ctx context.Context, beadID, chosen, rationale, channelID, messageTS string) {
	// Try hot cache first.
	if messageTS == "" {
		b.mu.Lock()
		if ref, ok := b.messages[beadID]; ok {
			messageTS = ref.Timestamp
			channelID = ref.ChannelID
		}
		b.mu.Unlock()
	}
	// Fall back to state manager.
	if messageTS == "" && b.state != nil {
		if ref, ok := b.state.GetDecisionMessage(beadID); ok {
			messageTS = ref.Timestamp
			channelID = ref.ChannelID
		}
	}

	if messageTS == "" || channelID == "" {
		b.logger.Debug("no Slack message found for resolved decision", "bead", beadID)
		return
	}

	text := fmt.Sprintf(":white_check_mark: *Resolved*: %s", chosen)
	if rationale != "" {
		text += fmt.Sprintf("\n_%s_", rationale)
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", text, false, false),
			nil, nil,
		),
	}

	_, _, _, err := b.api.UpdateMessageContext(ctx, channelID, messageTS,
		slack.MsgOptionText(fmt.Sprintf("Decision resolved: %s", chosen), false),
		slack.MsgOptionBlocks(blocks...),
	)
	if err != nil {
		b.logger.Error("failed to update Slack message", "bead", beadID, "error", err)
		return
	}

	// Decrement agent pending count and update card. Clear the Agent field
	// on the cached ref to prevent double-decrement when the SSE close event
	// triggers UpdateDecision after the modal submit already resolved it.
	// The ref stays in hot cache (with Agent="") so PostReport can still
	// thread under the resolved decision message.
	b.mu.Lock()
	ref, hadRef := b.messages[beadID]
	agent := extractAgentName(ref.Agent)
	if hadRef && b.agentThreadingEnabled() && agent != "" {
		if b.agentPending[agent] > 0 {
			b.agentPending[agent]--
		}
		ref.Agent = ""
		b.messages[beadID] = ref
	}
	b.mu.Unlock()

	// Persist with Agent cleared so PostReport can still find the message
	// ref after a restart, while the pending count won't re-inflate.
	if b.state != nil && hadRef {
		persistRef := ref
		persistRef.Agent = ""
		_ = b.state.SetDecisionMessage(beadID, persistRef)
	}

	if hadRef && b.agentThreadingEnabled() && agent != "" {
		b.updateAgentCard(ctx, agent)
	}
}

// agentDisplayName returns the agent's short name as a clickable coopmux link
// (if the pod name is known), otherwise the plain short name. Thread-safe.
func (b *Bot) agentDisplayName(agent string) string {
	name := extractAgentName(agent)
	b.mu.Lock()
	podName := b.agentPodName[name]
	b.mu.Unlock()
	return coopmuxAgentLink(b.coopmuxPublicURL, podName, name)
}

// agentThreadLink returns "agent" as a clickable coopmux link for use in
// thread replies where the agent identity is already clear from context.
func (b *Bot) agentThreadLink(agent string) string {
	b.mu.Lock()
	podName := b.agentPodName[extractAgentName(agent)]
	b.mu.Unlock()
	return coopmuxAgentLink(b.coopmuxPublicURL, podName, "agent")
}

// coopmuxAgentLink returns a Slack mrkdwn link to the agent's coopmux terminal
// session if both coopmuxPublicURL and podName are available. Otherwise it
// returns the plain agent name in bold.
func coopmuxAgentLink(coopmuxURL, podName, agentName string) string {
	if coopmuxURL != "" && podName != "" {
		return fmt.Sprintf("<%s#%s|%s>", coopmuxURL, podName, agentName)
	}
	return agentName
}

// Ensure Bot implements Notifier, AgentNotifier, JackNotifier, and BeadActivityNotifier.
var _ Notifier = (*Bot)(nil)
var _ AgentNotifier = (*Bot)(nil)
var _ JackNotifier = (*Bot)(nil)
var _ BeadActivityNotifier = (*Bot)(nil)
