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

	// Resolve from config beads (seeded by EnsureConfigs on startup).
	prompt := resolveNudgeFromConfig(promptType, vars)
	if prompt != "" {
		fmt.Fprint(os.Stdout, prompt)
		return nil
	}

	return fmt.Errorf("no nudge-prompts config bead found for type %q — run EnsureConfigs or seed nudge-prompts config beads", promptType)
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
	Role         string
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

	// Resolve task ID: prefer env var, then resolve from agent bead dependencies.
	taskID := os.Getenv("BOAT_TASK_ID")
	if taskID == "" {
		taskID = resolveTaskFromDeps()
	}
	var taskHint string
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
		Role:         os.Getenv("BOAT_ROLE"),
		ProjectHint:  projectHint,
		TaskHint:     taskHint,
		MonorepoHint: monorepoHint,
		BoatPrompt:   os.Getenv("BOAT_PROMPT"),
	}
}

// resolveTaskFromDeps resolves a pre-assigned task ID from the agent bead's
// dependencies (type "assigned"). This replaces the entrypoint's bash-based
// dependency resolution, moving it into Go for reliability.
func resolveTaskFromDeps() string {
	agentBeadID := os.Getenv("BOAT_AGENT_BEAD_ID")
	if agentBeadID == "" || daemon == nil {
		return ""
	}
	ctx := context.Background()
	deps, err := daemon.GetDependencies(ctx, agentBeadID)
	if err != nil {
		return ""
	}
	for _, d := range deps {
		if d.Type == "assigned" {
			return d.DependsOnID
		}
	}
	return ""
}

// resolveNudgeFromConfig queries nudge-prompts config beads and returns
// the resolved prompt for the given type. Returns "" if no config beads exist.
func resolveNudgeFromConfig(promptType string, vars nudgeVars) string {
	ctx := context.Background()

	role := os.Getenv("BOAT_ROLE")
	project := defaultGBProject()

	subs := []string{"global"}
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
		"{{.Role}}", vars.Role,
		"{{.ProjectHint}}", vars.ProjectHint,
		"{{.TaskHint}}", vars.TaskHint,
		"{{.MonorepoHint}}", vars.MonorepoHint,
		"{{.BoatPrompt}}", vars.BoatPrompt,
	)
	return r.Replace(tmpl)
}

