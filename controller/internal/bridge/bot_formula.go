package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack"
)

// formulaVarDef matches the kbeads FormulaVarDef type.
type formulaVarDef struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Type        string   `json:"type,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// formulaStep matches the kbeads FormulaStep type.
type formulaStep struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Description     string   `json:"description,omitempty"`
	Type            string   `json:"type,omitempty"`
	Priority        *int     `json:"priority,omitempty"`
	Labels          []string `json:"labels,omitempty"`
	DependsOn       []string `json:"depends_on,omitempty"`
	Assignee        string   `json:"assignee,omitempty"`
	Condition       string   `json:"condition,omitempty"`
	Role            string   `json:"role,omitempty"`              // target role for this step (empty = inherit molecule context)
	Project         string   `json:"project,omitempty"`           // target project (empty = inherit molecule project)
	SuggestNewAgent bool     `json:"suggest_new_agent,omitempty"` // hint that a dedicated agent should handle this step
}

// formulaFields holds the parsed vars and steps from a formula bead's fields.
type formulaFields struct {
	Vars  []formulaVarDef `json:"vars"`
	Steps []formulaStep   `json:"steps"`
}

// handleFormulaCommand processes the /formula slash command.
//
// Usage:
//
//	/formula                              — list available formulas
//	/formula list                         — list available formulas
//	/formula show <id-or-name>            — show formula details
//	/formula pour <id-or-name> [--var k=v ...] — instantiate as persistent molecule
//	/formula wisp <id-or-name> [--var k=v ...] — instantiate as ephemeral wisp
func (b *Bot) handleFormulaCommand(ctx context.Context, cmd slack.SlashCommand) {
	args := splitQuotedArgs(strings.TrimSpace(cmd.Text))

	subcommand := "list"
	if len(args) > 0 {
		subcommand = strings.ToLower(args[0])
	}

	switch subcommand {
	case "list", "ls":
		b.handleFormulaList(ctx, cmd)
	case "show":
		if len(args) < 2 {
			b.postEphemeral(cmd, ":x: Usage: `/formula show <id-or-name>`")
			return
		}
		b.handleFormulaShow(ctx, cmd, args[1])
	case "pour":
		if len(args) < 2 {
			b.postEphemeral(cmd, ":x: Usage: `/formula pour <id-or-name> [--var key=value ...]`")
			return
		}
		b.handleFormulaPour(ctx, cmd, args[1:], false)
	case "wisp":
		if len(args) < 2 {
			b.postEphemeral(cmd, ":x: Usage: `/formula wisp <id-or-name> [--var key=value ...]`")
			return
		}
		b.handleFormulaPour(ctx, cmd, args[1:], true)
	case "help":
		b.postEphemeral(cmd, strings.Join([]string{
			":test_tube: */formula* — manage reusable work formulas",
			"",
			"`/formula` or `/formula list` — list available formulas",
			"`/formula show <id>` — show formula details (vars, steps)",
			"`/formula pour <id> [--var k=v ...]` — instantiate as persistent molecule",
			"`/formula wisp <id> [--var k=v ...]` — instantiate as ephemeral wisp",
			"`/formula help` — show this help",
		}, "\n"))
	default:
		b.postEphemeral(cmd, fmt.Sprintf(":x: Unknown subcommand %q. Use `/formula help` for usage.", subcommand))
	}
}

// handleFormulaList lists available formulas.
func (b *Bot) handleFormulaList(ctx context.Context, cmd slack.SlashCommand) {
	result, err := b.daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"formula"},
		Statuses: []string{"open"},
		Limit:    25,
	})
	if err != nil {
		b.logger.Error("formula list: failed to list formulas", "error", err)
		b.postEphemeral(cmd, ":x: Failed to list formulas")
		return
	}

	if result.Total == 0 {
		b.postEphemeral(cmd, ":test_tube: No formulas found. Create one with `kd formula create`.")
		return
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf(":test_tube: *%d Formula%s*", result.Total, plural(result.Total)),
				false, false),
			nil, nil),
		slack.NewDividerBlock(),
	}

	for _, f := range result.Beads {
		// Parse vars/steps counts from fields.
		ff := parseFormulaFields(f)
		line := fmt.Sprintf("*%s*\n`%s`", f.Title, f.ID)
		if len(ff.Vars) > 0 || len(ff.Steps) > 0 {
			line += fmt.Sprintf(" · %d var%s, %d step%s",
				len(ff.Vars), plural(len(ff.Vars)),
				len(ff.Steps), plural(len(ff.Steps)))
		}
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject("mrkdwn", line, false, false),
				nil, nil))
	}

	if result.Total > len(result.Beads) {
		blocks = append(blocks,
			slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_...and %d more_", result.Total-len(result.Beads)),
					false, false)))
	}

	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionBlocks(blocks...))
}

// handleFormulaShow shows details of a formula.
func (b *Bot) handleFormulaShow(ctx context.Context, cmd slack.SlashCommand, idOrName string) {
	formula, err := b.resolveFormula(ctx, idOrName)
	if err != nil {
		b.postEphemeral(cmd, fmt.Sprintf(":x: %s", err.Error()))
		return
	}

	ff := parseFormulaFields(formula)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(":test_tube: *%s*\n`%s`\n", formula.Title, formula.ID))

	if formula.Description != "" {
		desc := formula.Description
		if len(desc) > 200 {
			desc = desc[:197] + "..."
		}
		sb.WriteString(fmt.Sprintf("\n%s\n", desc))
	}

	if len(ff.Vars) > 0 {
		sb.WriteString("\n*Variables:*\n")
		for _, v := range ff.Vars {
			req := ""
			if v.Required {
				req = " _(required)_"
			}
			def := ""
			if v.Default != "" {
				def = fmt.Sprintf(" [default: `%s`]", v.Default)
			}
			enum := ""
			if len(v.Enum) > 0 {
				enum = fmt.Sprintf(" {%s}", strings.Join(v.Enum, ", "))
			}
			desc := ""
			if v.Description != "" {
				desc = " — " + v.Description
			}
			sb.WriteString(fmt.Sprintf("  `{{%s}}`%s%s%s%s\n", v.Name, req, def, enum, desc))
		}
	}

	if len(ff.Steps) > 0 {
		sb.WriteString("\n*Steps:*\n")
		for _, s := range ff.Steps {
			typ := s.Type
			if typ == "" {
				typ = "task"
			}
			deps := ""
			if len(s.DependsOn) > 0 {
				deps = fmt.Sprintf(" (after: %s)", strings.Join(s.DependsOn, ", "))
			}
			transition := ""
			if s.Project != "" {
				transition += " → project:" + s.Project
			}
			if s.Role != "" {
				if transition == "" {
					transition += " →"
				} else {
					transition += ","
				}
				transition += " role:" + s.Role
			}
			if s.SuggestNewAgent {
				transition += " :zap:new agent"
			}
			sb.WriteString(fmt.Sprintf("  `%s` %s [%s]%s%s\n", s.ID, s.Title, typ, deps, transition))
		}
	}

	b.postEphemeral(cmd, sb.String())
}

// handleFormulaPour instantiates a formula as a molecule (pour) or wisp.
func (b *Bot) handleFormulaPour(ctx context.Context, cmd slack.SlashCommand, args []string, ephemeral bool) {
	idOrName := args[0]

	// Parse --var flags from remaining args.
	varPairs := make(map[string]string)
	for i := 1; i < len(args); i++ {
		if (args[i] == "--var" || args[i] == "-v") && i+1 < len(args) {
			i++
			k, v, ok := splitVarPair(args[i])
			if !ok {
				b.postEphemeral(cmd, fmt.Sprintf(":x: Invalid --var %q: expected key=value", args[i]))
				return
			}
			varPairs[k] = v
		} else if strings.HasPrefix(args[i], "--var=") {
			pair := strings.TrimPrefix(args[i], "--var=")
			k, v, ok := splitVarPair(pair)
			if !ok {
				b.postEphemeral(cmd, fmt.Sprintf(":x: Invalid --var %q: expected key=value", pair))
				return
			}
			varPairs[k] = v
		}
	}

	// Resolve the formula.
	formula, err := b.resolveFormula(ctx, idOrName)
	if err != nil {
		b.postEphemeral(cmd, fmt.Sprintf(":x: %s", err.Error()))
		return
	}

	ff := parseFormulaFields(formula)
	if len(ff.Steps) == 0 {
		b.postEphemeral(cmd, fmt.Sprintf(":x: Formula `%s` has no steps", formula.ID))
		return
	}

	// Apply defaults and validate required vars.
	for _, vd := range ff.Vars {
		if _, ok := varPairs[vd.Name]; !ok {
			if vd.Default != "" {
				varPairs[vd.Name] = vd.Default
			} else if vd.Required {
				b.postEphemeral(cmd, fmt.Sprintf(":x: Required variable `{{%s}}` not provided. Use `--var %s=<value>`", vd.Name, vd.Name))
				return
			}
		}
		// Validate enum constraint.
		if len(vd.Enum) > 0 {
			if val, ok := varPairs[vd.Name]; ok {
				valid := false
				for _, e := range vd.Enum {
					if val == e {
						valid = true
						break
					}
				}
				if !valid {
					b.postEphemeral(cmd, fmt.Sprintf(":x: Variable `{{%s}}` value %q not in allowed values: %v", vd.Name, val, vd.Enum))
					return
				}
			}
		}
	}

	// Expand steps: substitute variables, evaluate conditions.
	type expandedStep struct {
		formulaStep
		skip bool
	}

	expanded := make([]expandedStep, 0, len(ff.Steps))
	for _, s := range ff.Steps {
		es := expandedStep{formulaStep: s}
		if s.Condition != "" {
			if !formulaEvalCondition(s.Condition, varPairs) {
				es.skip = true
			}
		}
		es.Title = formulaSubstituteVars(s.Title, varPairs)
		es.Description = formulaSubstituteVars(s.Description, varPairs)
		es.Assignee = formulaSubstituteVars(s.Assignee, varPairs)
		es.Role = formulaSubstituteVars(s.Role, varPairs)
		es.Project = formulaSubstituteVars(s.Project, varPairs)
		expanded = append(expanded, es)
	}

	// Filter skipped steps.
	skipped := make(map[string]bool)
	for _, es := range expanded {
		if es.skip {
			skipped[es.ID] = true
		}
	}

	var active []expandedStep
	for _, es := range expanded {
		if es.skip {
			continue
		}
		var filteredDeps []string
		for _, dep := range es.DependsOn {
			if !skipped[dep] {
				filteredDeps = append(filteredDeps, dep)
			}
		}
		es.DependsOn = filteredDeps
		active = append(active, es)
	}

	if len(active) == 0 {
		b.postEphemeral(cmd, ":x: All steps were filtered by conditions; nothing to create")
		return
	}

	phase := "molecule"
	if ephemeral {
		phase = "wisp"
	}

	// Collect active steps for instantiation.
	var activeSteps []formulaStep
	for _, es := range active {
		activeSteps = append(activeSteps, es.formulaStep)
	}

	// Resolve project from channel.
	project := b.projectFromChannel(ctx, cmd.ChannelID)

	// Run instantiation asynchronously — creating many beads can be slow.
	go func() {
		molID, stepCount, err := b.instantiateFormulaSteps(
			context.Background(), formula, activeSteps, varPairs, ephemeral, project)
		if err != nil {
			b.logger.Error("formula pour: instantiation failed", "formula", formula.ID, "error", err)
			b.postEphemeral(cmd, fmt.Sprintf(":x: Failed to create %s: %s", phase, err.Error()))
			return
		}
		b.logger.Info("formula poured via Slack",
			"formula", formula.ID, "molecule", molID, "steps", stepCount,
			"phase", phase, "user", cmd.UserID)
		b.postEphemeral(cmd, fmt.Sprintf(
			":bubbles: Created %s `%s` from formula *%s*\n%d step%s instantiated. Use `kd mol show %s` for details.",
			phase, molID, formula.Title, stepCount, plural(stepCount), molID))
	}()

	b.postEphemeral(cmd, fmt.Sprintf(":hourglass_flowing_sand: Pouring formula *%s* (%d steps)...", formula.Title, len(active)))
}

// instantiateFormulaSteps creates the molecule bead and child step beads.
func (b *Bot) instantiateFormulaSteps(
	ctx context.Context,
	formula *beadsapi.BeadDetail,
	steps []formulaStep,
	vars map[string]string,
	ephemeral bool,
	project string,
) (string, int, error) {
	// Create root molecule bead.
	molTitle := formulaSubstituteVars(formula.Title, vars)
	appliedVarsJSON, _ := json.Marshal(vars)
	molFields := map[string]any{
		"formula_id":   formula.ID,
		"applied_vars": json.RawMessage(appliedVarsJSON),
	}
	if ephemeral {
		molFields["ephemeral"] = true
	}
	molFieldsJSON, _ := json.Marshal(molFields)

	var labels []string
	if project != "" {
		labels = []string{"project:" + project}
	}

	molID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       molTitle,
		Description: formulaSubstituteVars(formula.Description, vars),
		Type:        "molecule",
		Priority:    formula.Priority,
		Labels:      labels,
		Fields:      json.RawMessage(molFieldsJSON),
	})
	if err != nil {
		return "", 0, fmt.Errorf("creating %s: %w", molTitle, err)
	}

	// Create child beads for each step.
	stepBeadIDs := make(map[string]string, len(steps))
	crossProjectSteps := make(map[string]map[string]string) // stepID → {"project": X, "bead_id": Y}
	for _, s := range steps {
		typ := s.Type
		if typ == "" {
			typ = "task"
		}
		pri := formula.Priority
		if s.Priority != nil {
			pri = *s.Priority
		}

		// Determine step-level project: use step's project override or molecule's project.
		stepProject := project
		if s.Project != "" {
			stepProject = s.Project
		}

		// Build step labels with correct project scope.
		stepLabels := formulaBuildStepLabels(labels, s.Labels, stepProject, project)

		// Add role label if step specifies a target role.
		if s.Role != "" {
			stepLabels = append(stepLabels, "role:"+s.Role)
		}

		// Build step-level fields for suggest_new_agent hint.
		var stepFields json.RawMessage
		if s.SuggestNewAgent {
			sf, _ := json.Marshal(map[string]any{"suggest_new_agent": true})
			stepFields = sf
		}

		stepID, err := b.daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
			Title:       s.Title,
			Description: s.Description,
			Type:        typ,
			Priority:    pri,
			Labels:      stepLabels,
			Assignee:    s.Assignee,
			Fields:      stepFields,
		})
		if err != nil {
			return molID, len(stepBeadIDs), fmt.Errorf("creating step %q: %w", s.ID, err)
		}
		stepBeadIDs[s.ID] = stepID

		// Track cross-project steps for molecule metadata.
		if s.Project != "" && s.Project != project {
			crossProjectSteps[s.ID] = map[string]string{
				"project": s.Project,
				"bead_id": stepID,
			}
		}

		// Link step to molecule as parent-child.
		if err := b.daemon.AddDependency(ctx, stepID, molID, "parent-child", "slack-bridge"); err != nil {
			b.logger.Warn("formula pour: failed to add parent-child dep",
				"step", s.ID, "stepBead", stepID, "molecule", molID, "error", err)
		}
	}

	// Store cross-project step references on the molecule for tracking.
	if len(crossProjectSteps) > 0 {
		xStepsJSON, _ := json.Marshal(crossProjectSteps)
		if err := b.daemon.UpdateBeadFields(ctx, molID, map[string]string{
			"cross_project_steps": string(xStepsJSON),
		}); err != nil {
			b.logger.Warn("formula pour: failed to store cross-project steps",
				"molecule", molID, "error", err)
		}
	}

	// Add blocks dependencies between steps.
	for _, s := range steps {
		for _, depStepID := range s.DependsOn {
			depBeadID, ok := stepBeadIDs[depStepID]
			if !ok {
				continue
			}
			if err := b.daemon.AddDependency(ctx, stepBeadIDs[s.ID], depBeadID, "blocks", "slack-bridge"); err != nil {
				b.logger.Warn("formula pour: failed to add blocks dep",
					"step", s.ID, "depends_on", depStepID, "error", err)
			}
		}
	}

	return molID, len(stepBeadIDs), nil
}

// resolveFormula finds a formula by ID or by name search.
func (b *Bot) resolveFormula(ctx context.Context, idOrName string) (*beadsapi.BeadDetail, error) {
	// Try direct ID lookup first.
	if strings.HasPrefix(idOrName, "kd-") {
		bead, err := b.daemon.GetBead(ctx, idOrName)
		if err != nil {
			return nil, fmt.Errorf("formula %q not found: %s", idOrName, err.Error())
		}
		if bead.Type != "formula" && bead.Type != "template" {
			return nil, fmt.Errorf("bead `%s` is type %q, not formula", idOrName, bead.Type)
		}
		return bead, nil
	}

	// Search by name.
	result, err := b.daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"formula"},
		Statuses: []string{"open"},
		Search:   idOrName,
		Limit:    5,
	})
	if err != nil {
		return nil, fmt.Errorf("searching formulas: %s", err.Error())
	}
	if result.Total == 0 {
		return nil, fmt.Errorf("no formula found matching %q", idOrName)
	}
	if result.Total > 1 {
		var names []string
		for _, f := range result.Beads {
			names = append(names, fmt.Sprintf("`%s` (%s)", f.ID, f.Title))
		}
		return nil, fmt.Errorf("multiple formulas match %q: %s — use the ID instead", idOrName, strings.Join(names, ", "))
	}
	return result.Beads[0], nil
}

// parseFormulaFields extracts vars and steps from a formula bead's fields.
func parseFormulaFields(bead *beadsapi.BeadDetail) formulaFields {
	var ff formulaFields
	if bead.Fields == nil {
		return ff
	}

	// Fields is map[string]string — vars and steps are JSON strings.
	if raw, ok := bead.Fields["vars"]; ok {
		_ = json.Unmarshal([]byte(raw), &ff.Vars)
	}
	if raw, ok := bead.Fields["steps"]; ok {
		_ = json.Unmarshal([]byte(raw), &ff.Steps)
	}
	return ff
}

// formulaBuildStepLabels builds the label set for a step bead, handling
// project overrides and per-step label merging.
func formulaBuildStepLabels(molLabels, stepLabels []string, stepProject, molProject string) []string {
	seen := make(map[string]bool)
	var merged []string

	// Start with molecule labels, but replace project label if step overrides it.
	for _, l := range molLabels {
		if strings.HasPrefix(l, "project:") && stepProject != molProject {
			continue // skip molecule's project label; we'll add the step's project
		}
		seen[l] = true
		merged = append(merged, l)
	}

	// Add step-specific project label if overridden.
	if stepProject != molProject && stepProject != "" {
		projLabel := "project:" + stepProject
		if !seen[projLabel] {
			seen[projLabel] = true
			merged = append(merged, projLabel)
		}
	}

	// Merge per-step labels.
	for _, l := range stepLabels {
		if !seen[l] {
			seen[l] = true
			merged = append(merged, l)
		}
	}

	return merged
}

// formulaVarPattern matches {{variable}} placeholders.
var formulaVarPattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// formulaSubstituteVars replaces {{name}} placeholders with values from vars.
func formulaSubstituteVars(s string, vars map[string]string) string {
	return formulaVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-2]
		if val, ok := vars[name]; ok {
			return val
		}
		return match
	})
}

// formulaEvalCondition evaluates a simple condition string against variables.
func formulaEvalCondition(cond string, vars map[string]string) bool {
	cond = strings.TrimSpace(cond)

	if strings.HasPrefix(cond, "!") {
		return !formulaEvalCondition(strings.TrimPrefix(cond, "!"), vars)
	}

	for _, op := range []string{"!=", "=="} {
		if parts := strings.SplitN(cond, op, 2); len(parts) == 2 {
			left := formulaSubstituteVars(strings.TrimSpace(parts[0]), vars)
			right := strings.TrimSpace(parts[1])
			if op == "==" {
				return left == right
			}
			return left != right
		}
	}

	resolved := formulaSubstituteVars(cond, vars)
	if resolved != cond {
		return resolved != "" && resolved != "false" && resolved != "0"
	}
	if formulaVarPattern.MatchString(cond) {
		return false
	}
	return resolved != ""
}

// splitVarPair splits "key=value" into (key, value, true) or ("", "", false).
func splitVarPair(s string) (string, string, bool) {
	idx := strings.IndexByte(s, '=')
	if idx < 1 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}

// plural returns "s" if n != 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// postEphemeral is a convenience wrapper for sending ephemeral messages.
func (b *Bot) postEphemeral(cmd slack.SlashCommand, text string) {
	_, _ = b.api.PostEphemeral(cmd.ChannelID, cmd.UserID,
		slack.MsgOptionText(text, false))
}
