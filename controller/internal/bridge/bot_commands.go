package bridge

import (
	"context"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// handleSlashCommand processes Slack slash commands.
func (b *Bot) handleSlashCommand(ctx context.Context, cmd slack.SlashCommand) {
	switch cmd.Command {
	case "/decisions", "/decide":
		b.handleDecisionsCommand(ctx, cmd)
	case "/roster":
		b.handleRosterCommand(ctx, cmd)
	case "/spawn":
		b.handleSpawnCommand(ctx, cmd)
	case "/start":
		b.handleStartCommand(ctx, cmd)
	case "/kill":
		b.handleKillCommand(ctx, cmd)
	case "/unreleased":
		b.handleUnreleasedCommand(ctx, cmd)
	case "/clear-threads":
		b.handleClearThreadsCommand(ctx, cmd)
	case "/bouncer":
		b.handleBouncerCommand(ctx, cmd)
	case "/autodestruct":
		b.handleAutoDestructCommand(ctx, cmd)
	default:
		b.logger.Debug("unhandled slash command", "command", cmd.Command)
	}
}

// handleSpawnCommand processes the /spawn slash command.
// Usage: /spawn [task|ticket] [--role <role>]
//
// The new /spawn does NOT require an agent name — it auto-generates one.
// Project is inferred from the channel's default project (via project beads'
// slack_channel field). If no channel mapping exists, the user must use /start
// which accepts an explicit agent name and project.
func (b *Bot) handleSpawnCommand(ctx context.Context, cmd slack.SlashCommand) {
	args := splitQuotedArgs(strings.TrimSpace(cmd.Text))

	// Extract --role flag from args, leaving positional args intact.
	role := ""
	positional := args[:0]
	for i := 0; i < len(args); i++ {
		if args[i] == "--role" && i+1 < len(args) {
			role = args[i+1]
			i++ // skip value
		} else if v, ok := strings.CutPrefix(args[i], "--role="); ok {
			role = v
		} else {
			positional = append(positional, args[i])
		}
	}

	// Resolve project from channel.
	project := b.projectFromChannel(ctx, cmd.ChannelID)

	taskID := ""

	// Detect task description: either positional[0] has spaces (old form: /spawn "desc"),
	// or positional[0] is a plain project name and positional[1] has spaces
	// (new form: /spawn gasboat "desc").
	taskDescription := ""
	if len(positional) > 0 && strings.Contains(positional[0], " ") {
		taskDescription = positional[0]
	} else if len(positional) >= 2 && strings.Contains(positional[1], " ") {
		// /spawn <project> "task description"
		project = positional[0]
		taskDescription = positional[1]
	}

	if taskDescription != "" {
		// Task-first mode: create a task bead, auto-generate name from description.
		agentName := generateAgentName(taskDescription)

		// Validate project exists.
		if project != "" {
			if !b.validateProject(ctx, cmd, project) {
				return
			}
		}

		// Create a task bead for the description.
		var labels []string
		if project != "" {
			labels = []string{"project:" + project}
		}
		var err error
		taskID, err = b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
			Title:    taskDescription,
			Type:     "task",
			Kind:     "issue",
			Labels:   labels,
			Priority: 2,
		})
		if err != nil {
			b.logger.Error("failed to create task bead", "error", err)
			_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
				slack.MsgOptionText(fmt.Sprintf(":x: Failed to create task: %s", err.Error()), false))
			return
		}

		b.spawnAndRespond(ctx, cmd, agentName, project, taskID, role, "")
		return
	}

	// If a positional arg is a ticket/bead reference, resolve it.
	if len(positional) > 0 && isTicketRef(positional[0]) {
		bead, err := b.daemon.ResolveTicket(ctx, positional[0])
		if err != nil {
			b.logger.Error("failed to resolve ticket", "ticket", positional[0], "error", err)
			_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
				slack.MsgOptionText(fmt.Sprintf(":x: Could not resolve ticket %q: %s", positional[0], err.Error()), false))
			return
		}
		taskID = bead.ID
		// Infer project from ticket if channel didn't provide one.
		if project == "" {
			project = projectFromLabels(bead.Labels)
			if project == "" {
				project = b.projectFromTicketPrefix(ctx, positional[0])
			}
		}
	}

	// Auto-generate agent name.
	agentName := generateSpawnName(project)
	if taskID != "" {
		// If we have a task, try to generate a better name from the task title.
		taskBead, err := b.daemon.GetBead(ctx, taskID)
		if err == nil && taskBead.Title != "" {
			agentName = generateAgentName(taskBead.Title)
		}
	}

	// Validate project exists.
	if project != "" {
		if !b.validateProject(ctx, cmd, project) {
			return
		}
	}

	b.spawnAndRespond(ctx, cmd, agentName, project, taskID, role, "")
}

// handleStartCommand processes the /start slash command.
// Usage: /start <agent> [project|ticket|"PROMPT TEXT"] [task] [--role <role>]
//
//	/start "task description" [project] [--role <role>]
//
// This is the original /spawn behavior, preserved for power users who want to
// specify an explicit agent name.
func (b *Bot) handleStartCommand(ctx context.Context, cmd slack.SlashCommand) {
	args := splitQuotedArgs(strings.TrimSpace(cmd.Text))
	if len(args) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Usage: `/start <agent> [project|ticket|\"PROMPT TEXT\"] [task] [--role <role>]`\nor: `/start \"task description\" [project] [--role <role>]`", false))
		return
	}

	// Extract --role flag from args, leaving positional args intact.
	role := ""
	positional := args[:0]
	for i := 0; i < len(args); i++ {
		if args[i] == "--role" && i+1 < len(args) {
			role = args[i+1]
			i++ // skip value
		} else if v, ok := strings.CutPrefix(args[i], "--role="); ok {
			role = v
		} else {
			positional = append(positional, args[i])
		}
	}

	// Task-first mode: if the first positional arg contains spaces, it was a
	// quoted task description rather than an agent name.
	if strings.Contains(positional[0], " ") {
		b.handleTaskFirstSpawn(ctx, cmd, positional, role)
		return
	}

	agentName := positional[0]
	if !isValidAgentName(agentName) {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Invalid agent name %q — use lowercase letters, digits, and hyphens only", agentName), false))
		return
	}

	project := ""
	customPrompt := ""
	taskID := ""

	if len(positional) >= 2 {
		arg2 := positional[1]
		// Quoted strings are custom prompts, not project names.
		if strings.Contains(arg2, " ") {
			customPrompt = arg2
		} else if isTicketRef(arg2) {
			// Ticket reference (e.g., "PE-1234", "kd-abc123") — resolve to
			// bead ID and infer project from the ticket.
			bead, err := b.daemon.ResolveTicket(ctx, arg2)
			if err != nil {
				b.logger.Error("failed to resolve ticket", "ticket", arg2, "error", err)
				_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
					slack.MsgOptionText(fmt.Sprintf(":x: Could not resolve ticket %q: %s", arg2, err.Error()), false))
				return
			}
			taskID = bead.ID
			// Infer project from the bead's project label.
			project = projectFromLabels(bead.Labels)
			// If no project label on the bead, try the prefix → project mapping.
			if project == "" {
				project = b.projectFromTicketPrefix(ctx, arg2)
			}
		} else {
			project = arg2
		}
	}

	if len(positional) >= 3 && taskID == "" {
		taskID = positional[2]
	}

	// Validate project exists.
	if project != "" {
		if !b.validateProject(ctx, cmd, project) {
			return
		}
	}

	b.spawnAndRespond(ctx, cmd, agentName, project, taskID, role, customPrompt)
}

// spawnAndRespond creates an agent bead and sends a confirmation ephemeral message.
// Extracted from the old handleSpawnCommand to share between /spawn and /start.
func (b *Bot) spawnAndRespond(ctx context.Context, cmd slack.SlashCommand, agentName, project, taskID, role, customPrompt string) {
	beadID, err := b.daemon.SpawnAgent(ctx, agentName, project, taskID, role, customPrompt)
	if err != nil {
		b.logger.Error("failed to spawn agent", "agent", agentName, "project", project, "task", taskID, "role", role, "prompt", customPrompt, "error", err)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to spawn agent %q: %s", agentName, err.Error()), false))
		return
	}

	b.logger.Info("spawned agent via Slack", "agent", agentName, "project", project, "task", taskID, "role", role, "prompt", customPrompt, "bead", beadID, "user", cmd.UserID)

	text := fmt.Sprintf(":rocket: Spawning agent *%s*", agentName)
	if project != "" {
		text += fmt.Sprintf(" in project *%s*", project)
	}
	if role != "" {
		text += fmt.Sprintf(" with role *%s*", role)
	}
	if taskID != "" {
		text += fmt.Sprintf(" assigned to task `%s`", taskID)
	}
	if customPrompt != "" {
		promptPreview := customPrompt
		if len(promptPreview) > 60 {
			promptPreview = promptPreview[:57] + "..."
		}
		text += fmt.Sprintf("\nPrompt: _%s_", promptPreview)
	}
	text += fmt.Sprintf("\nBead: `%s` · Use `/roster` to check status.", beadID)
	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionText(text, false))
}

// handleTaskFirstSpawn handles /start "task description" [project] [--role <role>].
// It auto-generates an agent name from the task description, creates a task bead,
// and spawns an agent assigned to that task.
func (b *Bot) handleTaskFirstSpawn(ctx context.Context, cmd slack.SlashCommand, positional []string, role string) {
	taskDescription := positional[0]
	agentName := generateAgentName(taskDescription)

	project := ""
	if len(positional) >= 2 {
		project = positional[1]
	}

	// Validate project exists.
	if project != "" {
		if !b.validateProject(ctx, cmd, project) {
			return
		}
	}

	// Create a task bead for the description.
	var labels []string
	if project != "" {
		labels = []string{"project:" + project}
	}
	taskID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:    taskDescription,
		Type:     "task",
		Kind:     "issue",
		Labels:   labels,
		Priority: 2,
	})
	if err != nil {
		b.logger.Error("failed to create task bead", "error", err)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to create task: %s", err.Error()), false))
		return
	}

	b.spawnAndRespond(ctx, cmd, agentName, project, taskID, role, "")
}

// validateProject checks whether a project name is known and sends an ephemeral
// error if not. Returns true if the project is valid (or validation failed
// silently), false if an error was sent to the user.
func (b *Bot) validateProject(ctx context.Context, cmd slack.SlashCommand, project string) bool {
	projects, err := b.daemon.ListProjectBeads(ctx)
	if err != nil {
		b.logger.Error("failed to list projects for validation", "error", err)
		return true // allow through on error
	}
	if _, ok := projects[project]; !ok {
		names := make([]string, 0, len(projects))
		for name := range projects {
			names = append(names, name)
		}
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Unknown project %q — available: %s", project, strings.Join(names, ", ")), false))
		return false
	}
	return true
}

// generateAgentName creates a valid agent name from a task description by
// slugifying the first 3 words and appending a random suffix.
// Example: "fix the login bug" → "fix-the-login-a7k"
func generateAgentName(description string) string {
	words := strings.Fields(strings.ToLower(description))
	var slugWords []string
	for _, w := range words {
		var clean strings.Builder
		for _, c := range w {
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				clean.WriteRune(c)
			}
		}
		if clean.Len() > 0 {
			slugWords = append(slugWords, clean.String())
		}
		if len(slugWords) == 3 {
			break
		}
	}
	if len(slugWords) == 0 {
		slugWords = []string{"agent"}
	}
	return strings.Join(slugWords, "-") + "-" + randomSuffix(3)
}

// randomSuffix returns a random string of n lowercase alphanumeric characters.
func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return string(b)
}

// splitQuotedArgs splits a command string into arguments, respecting double-quoted
// strings as single arguments. Quotes are stripped from the resulting tokens.
// Example: `my-bot "fix the login bug" --role crew` → ["my-bot", "fix the login bug", "--role", "crew"]
func splitQuotedArgs(s string) []string {
	var args []string
	var current strings.Builder
	inQuotes := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"':
			inQuotes = !inQuotes
		case ch == ' ' && !inQuotes:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// isValidAgentName reports whether s is a valid agent name.
// Valid names are non-empty and contain only lowercase letters, digits, and hyphens.
func isValidAgentName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// ticketRefRe matches external ticket references like "PE-1234" or "DEVOPS-42".
var ticketRefRe = regexp.MustCompile(`^[A-Za-z]+-\d+$`)

// isTicketRef reports whether s looks like a ticket reference.
// Matches JIRA-style keys (PE-1234, DEVOPS-42) and internal bead IDs (kd-xxx).
func isTicketRef(s string) bool {
	return strings.HasPrefix(s, "kd-") || ticketRefRe.MatchString(s)
}

// projectFromLabels extracts the project name from a bead's labels.
// Returns "" if no project label is found.
func projectFromLabels(labels []string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, "project:"); ok {
			return v
		}
	}
	return ""
}

// generateSpawnName creates a valid agent name for /spawn when no task is provided.
// Uses the project name as a prefix with a random suffix for uniqueness.
// Examples: "gasboat-k7x2", "monorepo-m3nq", "agent-a1b2" (no project).
func generateSpawnName(project string) string {
	prefix := project
	if prefix == "" {
		prefix = "agent"
	}
	return prefix + "-" + randomSuffix(4)
}

// projectFromChannel resolves a Slack channel ID to a project name by checking
// the slack_channel field on project beads. Supports multiple comma-separated
// channels per project.
func (b *Bot) projectFromChannel(ctx context.Context, channelID string) string {
	projects, err := b.daemon.ListProjectBeads(ctx)
	if err != nil {
		b.logger.Error("failed to list projects for channel lookup", "error", err)
		return ""
	}
	for name, info := range projects {
		if info.HasChannel(channelID) {
			b.logger.Debug("projectFromChannel: matched",
				"channel", channelID, "project", name)
			return name
		}
	}
	// Log all projects and their channels for debugging.
	projectChannels := make(map[string][]string, len(projects))
	for name, info := range projects {
		projectChannels[name] = info.SlackChannels
	}
	b.logger.Info("no project matched channel",
		"channel", channelID, "project_count", len(projects),
		"project_channels", projectChannels)
	return ""
}

// projectFromTicketPrefix looks up the project name for a ticket's prefix
// (e.g., "PE" from "PE-1234") by checking registered project beads' Prefix fields.
func (b *Bot) projectFromTicketPrefix(ctx context.Context, ticket string) string {
	parts := strings.SplitN(ticket, "-", 2)
	if len(parts) < 2 {
		return ""
	}
	prefix := strings.ToLower(parts[0])

	projects, err := b.daemon.ListProjectBeads(ctx)
	if err != nil {
		b.logger.Error("failed to list projects for prefix lookup", "error", err)
		return ""
	}
	for name, info := range projects {
		if strings.EqualFold(info.Prefix, prefix) {
			return name
		}
	}
	return ""
}

// handleDecisionsCommand shows pending decisions as an ephemeral message.
func (b *Bot) handleDecisionsCommand(ctx context.Context, cmd slack.SlashCommand) {
	decisions, err := b.daemon.ListDecisionBeads(ctx)
	if err != nil {
		b.logger.Error("failed to list decisions", "error", err)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Failed to fetch decisions", false))
		return
	}

	if len(decisions) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":white_check_mark: No pending decisions! All decisions have been resolved.", false))
		return
	}

	// Count escalated decisions.
	escalatedCount := 0
	for _, d := range decisions {
		for _, label := range d.Labels {
			if label == "escalated" {
				escalatedCount++
				break
			}
		}
	}

	// Build summary header.
	headerText := fmt.Sprintf(":clipboard: *%d Pending Decision", len(decisions))
	if len(decisions) != 1 {
		headerText += "s"
	}
	headerText += "*"
	if escalatedCount > 0 {
		headerText += fmt.Sprintf(" (%d :rotating_light: escalated)", escalatedCount)
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", headerText, false, false),
			nil, nil),
		slack.NewDividerBlock(),
	}

	// Per-decision entries (limit to 15 to stay within Slack block limits).
	limit := 15
	if len(decisions) < limit {
		limit = len(decisions)
	}
	for _, d := range decisions[:limit] {
		question := d.Fields["question"]
		if question == "" {
			question = d.Title
		}
		if len(question) > 100 {
			question = question[:97] + "..."
		}

		// Urgency indicator.
		urgency := ":white_circle:"
		for _, label := range d.Labels {
			if label == "escalated" {
				urgency = ":rotating_light:"
				break
			}
		}

		// Build text line.
		line := fmt.Sprintf("%s `%s`", urgency, d.ID)
		if d.Assignee != "" {
			line += fmt.Sprintf(" — `%s`", d.Assignee)
		}
		line += fmt.Sprintf("\n%s", question)

		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", line, false, false),
				nil,
				slack.NewAccessory(
					slack.NewButtonBlockElement(
						"view_decision_"+d.ID,
						d.ID,
						slack.NewTextBlockObject("plain_text", "View", false, false)))))
	}

	if len(decisions) > limit {
		blocks = append(blocks,
			slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_...and %d more_", len(decisions)-limit), false, false)))
	}

	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionBlocks(blocks...))
}

// handleKillCommand processes the /kill slash command.
// Usage: /kill <agent> [--force]
//
// Without --force, sends ESC to the agent's coop and waits for a clean exit
// before closing the bead. With --force, closes the bead immediately.
func (b *Bot) handleKillCommand(ctx context.Context, cmd slack.SlashCommand) {
	args := strings.Fields(strings.TrimSpace(cmd.Text))
	if len(args) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Usage: `/kill <agent> [--force]`", false))
		return
	}

	// Extract --force flag.
	force := false
	positional := args[:0]
	for _, a := range args {
		if a == "--force" {
			force = true
		} else {
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Usage: `/kill <agent> [--force]`", false))
		return
	}

	agentName := positional[0]

	// Run kill asynchronously — graceful shutdown can take 30s+ which exceeds
	// Slack's slash command response window.
	go func() {
		if err := b.killAgent(context.Background(), agentName, force); err != nil {
			b.logger.Error("kill command: failed to kill agent", "agent", agentName, "force", force, "error", err)
			_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
				slack.MsgOptionText(fmt.Sprintf(":x: Failed to kill agent %q: %s", agentName, err.Error()), false))
			return
		}
		b.logger.Info("killed agent via Slack slash command", "agent", agentName, "force", force, "user", cmd.UserID)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":skull: Agent *%s* terminated.", agentName), false))
	}()

	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionText(fmt.Sprintf(":hourglass_flowing_sand: Killing agent *%s*…", agentName), false))
}

// handleClearThreadsCommand removes all thread→agent mappings from state.
// This is an admin escape hatch for when stale bindings prevent new agents
// from spawning in threads.
func (b *Bot) handleClearThreadsCommand(_ context.Context, cmd slack.SlashCommand) {
	if b.state == nil {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: State manager not available", false))
		return
	}

	n, err := b.state.ClearAllThreadAgents()
	if err != nil {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to clear thread mappings: %s", err), false))
		return
	}

	// Also clear the in-memory lastThreadNudge map.
	b.mu.Lock()
	b.lastThreadNudge = make(map[string]time.Time)
	b.mu.Unlock()

	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionText(fmt.Sprintf(":broom: Cleared %d thread→agent mappings.", n), false))
}

// handleRosterCommand shows the agent dashboard as an ephemeral message.
func (b *Bot) handleRosterCommand(ctx context.Context, cmd slack.SlashCommand) {
	agents, err := b.daemon.ListAgentBeads(ctx)
	if err != nil {
		b.logger.Error("failed to list agents", "error", err)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Failed to fetch agent roster", false))
		return
	}

	if len(agents) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":busts_in_silhouette: No active agents", false))
		return
	}

	// Build roster blocks.
	headerText := fmt.Sprintf(":busts_in_silhouette: *Agent Roster* — %d active", len(agents))

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", headerText, false, false),
			nil, nil),
		slack.NewDividerBlock(),
	}

	// Limit display to 20 agents.
	limit := 20
	if len(agents) < limit {
		limit = len(agents)
	}
	for _, a := range agents[:limit] {
		name := a.AgentName
		if name == "" {
			name = a.ID
		}
		line := fmt.Sprintf(":large_green_circle: *%s*", name)
		if a.Project != "" {
			line += fmt.Sprintf(" · _%s_", a.Project)
		}
		if a.Role != "" {
			line += fmt.Sprintf(" (%s/%s)", a.Mode, a.Role)
		}
		line += fmt.Sprintf("\n`%s`", a.ID)
		if a.Title != "" && a.Title != a.ID {
			line += fmt.Sprintf(" · %s", a.Title)
		}

		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", line, false, false),
				nil, nil))
	}

	if len(agents) > limit {
		blocks = append(blocks,
			slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_...and %d more_", len(agents)-limit), false, false)))
	}

	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionBlocks(blocks...))
}
