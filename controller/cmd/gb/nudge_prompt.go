package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var nudgePromptCmd = &cobra.Command{
	Use:   "nudge-prompt",
	Short: "Resolve the nudge prompt for this agent's context",
	Long: `Resolves the initial nudge prompt from nudge-prompts config beads,
falling back to hardcoded defaults.

The prompt type is auto-detected from environment variables:
  - SLACK_THREAD_CHANNEL set → "thread"
  - BOAT_PROMPT set          → "adhoc"
  - BOAT_AGENT_STATE=prewarmed → "prewarmed"
  - otherwise                → "default"

Override with --type flag.

The resolved prompt has variable placeholders substituted:
  {{.Project}}, {{.TaskHint}}, {{.MonorepoHint}}, {{.ProjectHint}}, {{.BoatPrompt}}

Examples:
  gb nudge-prompt
  gb nudge-prompt --type thread
  gb nudge-prompt --type default --project myproject`,
	GroupID: "session",
	RunE:    runNudgePrompt,
}

var nudgePromptType string

func init() {
	nudgePromptCmd.Flags().StringVar(&nudgePromptType, "type", "", "prompt type: thread, adhoc, default, prewarmed (auto-detected if omitted)")
}

func runNudgePrompt(cmd *cobra.Command, args []string) error {
	promptType := nudgePromptType
	if promptType == "" {
		promptType = detectNudgeType()
	}

	// Build substitution variables from environment.
	vars := buildNudgeVars()

	// Try config beads first.
	prompt := resolveNudgeFromConfig(promptType, vars)
	if prompt != "" {
		fmt.Fprint(os.Stdout, prompt)
		return nil
	}

	// Fallback to hardcoded defaults.
	prompt = hardcodedNudge(promptType, vars)
	fmt.Fprint(os.Stdout, prompt)
	return nil
}

// detectNudgeType determines the nudge type from environment.
func detectNudgeType() string {
	if os.Getenv("BOAT_AGENT_STATE") == "prewarmed" {
		return "prewarmed"
	}
	if os.Getenv("SLACK_THREAD_CHANNEL") != "" {
		return "thread"
	}
	if os.Getenv("BOAT_PROMPT") != "" {
		return "adhoc"
	}
	return "default"
}

// nudgeVars holds substitution variables for nudge prompt templates.
type nudgeVars struct {
	Project      string
	ProjectHint  string
	TaskHint     string
	MonorepoHint string
	BoatPrompt   string
}

func buildNudgeVars() nudgeVars {
	project := defaultGBProject()

	var projectHint string
	if project != "" {
		projectHint = fmt.Sprintf(" Focus on tasks for project `%s` — skip work that belongs to a different project unless you are explicitly assigned to it.", project)
	}

	var taskHint string
	taskID := os.Getenv("BOAT_TASK_ID")
	if taskID != "" {
		taskHint = fmt.Sprintf(" You have been pre-assigned to task `%s`. Run `kd show %s` for details, then `kd claim %s` to start work on it.", taskID, taskID, taskID)
	}

	var monorepoHint string
	if os.Getenv("BOAT_REFERENCE_REPOS") != "" {
		monorepoHint = " Your workspace has additional repos cloned under repos/. Run `ls repos/` to see them. The primary repo is in your workspace root."
	}

	if project == "" {
		project = "gasboat"
	}

	return nudgeVars{
		Project:      project,
		ProjectHint:  projectHint,
		TaskHint:     taskHint,
		MonorepoHint: monorepoHint,
		BoatPrompt:   os.Getenv("BOAT_PROMPT"),
	}
}

// resolveNudgeFromConfig queries nudge-prompts config beads and returns
// the resolved prompt for the given type. Returns "" if no config beads exist.
func resolveNudgeFromConfig(promptType string, vars nudgeVars) string {
	ctx := context.Background()

	role := os.Getenv("BOAT_ROLE")
	project := defaultGBProject()

	subs := []string{"global:*"}
	if project != "" {
		subs = append(subs, "project:"+project)
	}
	if role != "" {
		subs = append(subs, "role:"+role)
	}

	merged, count := ResolveConfigBeads(ctx, daemon, "nudge-prompts", subs)
	if count == 0 {
		return ""
	}

	val, ok := merged[promptType].(string)
	if !ok || val == "" {
		return ""
	}

	return substituteNudgeVars(val, vars)
}

// substituteNudgeVars replaces {{.Field}} placeholders in the template.
func substituteNudgeVars(tmpl string, vars nudgeVars) string {
	r := strings.NewReplacer(
		"{{.Project}}", vars.Project,
		"{{.ProjectHint}}", vars.ProjectHint,
		"{{.TaskHint}}", vars.TaskHint,
		"{{.MonorepoHint}}", vars.MonorepoHint,
		"{{.BoatPrompt}}", vars.BoatPrompt,
	)
	return r.Replace(tmpl)
}

// hardcodedNudge returns the built-in default nudge prompt for the given type.
func hardcodedNudge(promptType string, vars nudgeVars) string {
	switch promptType {
	case "thread":
		return fmt.Sprintf("You are a thread-bound agent spawned from a Slack conversation. Your thread context is in your agent bead description. CRITICAL RULES: (1) Do NOT exit prematurely — if you hit an error, debug it; if you are blocked, ask a clarifying question via `gb squawk '<question>'`. Giving up silently is the worst outcome. (2) Create a tracking bead: `kd create '<short title>' --project %s` then `kd claim <id>`. (3) Post progress updates to the thread via `gb squawk '<update>'` at key milestones. (4) When done, summarize results via `gb squawk`, push to a feature branch (never main), open a PR if code changed, close your bead, then `gb done`.%s%s Now read the thread context in your description and begin working.", vars.Project, vars.MonorepoHint, vars.ProjectHint)

	case "adhoc":
		return fmt.Sprintf("You have been spawned with an ad-hoc task. Before starting: (1) Create a bead to track your work: `kd create '<short title>' --description '<your task description>' --project %s` then claim it with `kd claim <id>`. (2) Run `gb news` to check what teammates are working on — do not duplicate in-progress work. (3) When done, deliver via a feature branch + PR (never push to main).%s Here is your task: %s", vars.Project, vars.MonorepoHint, vars.BoatPrompt)

	case "prewarmed":
		return "You are a **prewarmed agent** waiting in the idle pool for work assignment. **Do NOT** seek work, run `gb ready`, or create beads. When a Slack thread mention or operator assigns you, the pool manager will inject a nudge with your task description. Wait for a nudge message."

	default:
		return fmt.Sprintf("Check `gb ready` for your workflow steps and begin working.%s%s%s IMPORTANT: (1) Run `gb news` first to see what your teammates are already working on — do not duplicate in-progress work. (2) Run `kd claim <id>` BEFORE starting any task — this atomically marks it in_progress so no other agent picks it up simultaneously.", vars.ProjectHint, vars.TaskHint, vars.MonorepoHint)
	}
}
