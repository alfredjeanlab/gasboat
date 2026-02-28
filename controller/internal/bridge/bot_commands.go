package bridge

import (
	"context"
	"fmt"
	"strings"

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
	case "/kill":
		b.handleKillCommand(ctx, cmd)
	case "/unreleased":
		b.handleUnreleasedCommand(ctx, cmd)
	default:
		b.logger.Debug("unhandled slash command", "command", cmd.Command)
	}
}

// handleSpawnCommand processes the /spawn slash command.
// Usage: /spawn <agent> [project|"PROMPT TEXT"] [task] [--role <role>]
// When the second argument is a quoted string, it is used as a custom prompt
// for the agent instead of a project name.
func (b *Bot) handleSpawnCommand(ctx context.Context, cmd slack.SlashCommand) {
	args := splitQuotedArgs(strings.TrimSpace(cmd.Text))
	if len(args) == 0 {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(":x: Usage: `/spawn <agent> [project|\"PROMPT TEXT\"] [task] [--role <role>]`", false))
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

	agentName := positional[0]
	if !isValidAgentName(agentName) {
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Invalid agent name %q — use lowercase letters, digits, and hyphens only", agentName), false))
		return
	}

	project := ""
	customPrompt := ""
	if len(positional) >= 2 {
		arg2 := positional[1]
		// Quoted strings are custom prompts, not project names.
		if strings.Contains(arg2, " ") {
			customPrompt = arg2
		} else {
			project = arg2
		}
	}

	taskID := ""
	if len(positional) >= 3 {
		taskID = positional[2]
	}

	// Validate project exists.
	if project != "" {
		projects, err := b.daemon.ListProjectBeads(ctx)
		if err != nil {
			b.logger.Error("failed to list projects for validation", "error", err)
		} else if _, ok := projects[project]; !ok {
			names := make([]string, 0, len(projects))
			for name := range projects {
				names = append(names, name)
			}
			_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
				slack.MsgOptionText(fmt.Sprintf(":x: Unknown project %q — available: %s", project, strings.Join(names, ", ")), false))
			return
		}
	}

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
	if err := b.killAgent(ctx, agentName, force); err != nil {
		b.logger.Error("kill command: failed to kill agent", "agent", agentName, "force", force, "error", err)
		_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
			slack.MsgOptionText(fmt.Sprintf(":x: Failed to kill agent %q: %s", agentName, err.Error()), false))
		return
	}

	b.logger.Info("killed agent via Slack slash command", "agent", agentName, "force", force, "user", cmd.UserID)
	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionText(fmt.Sprintf(":skull: Agent *%s* terminated.", agentName), false))
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
