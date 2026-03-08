package main

// gb spawn — Spawn a new agent with auto-generated name.
// gb start — Start a named agent (explicit name required).
//
// Usage:
//
//	gb spawn [flags]                            (auto-name, project from env)
//	gb spawn --task <bead-id>                   (auto-name from task title)
//	gb start <name> <project> [flags]           (explicit name)
//	gb start <project> --task <bead-id>         (task-first: auto-generate name)
//
// Flags:
//
//	--role <role>       Agent role (default: crew)
//	--task <bead-id>    Pre-assign a task bead
//	--prompt <text>     Custom prompt injected at startup

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var spawnCmd = &cobra.Command{
	Use:   "spawn [flags]",
	Short: "Spawn a new agent (auto-named)",
	Long: `Spawn a new agent with an auto-generated name. The project is taken from
BOAT_PROJECT or inferred from the task's labels.

  gb spawn                                    # auto-name, project from env
  gb spawn --task kd-abc123                   # name from task title
  gb spawn --task kd-abc123 --role engineer   # with role
  gb spawn --prompt 'Review PR #42'           # with custom prompt

Use 'gb start' if you need to specify an explicit agent name.`,
	GroupID: "agent",
	Args:    cobra.NoArgs,
	RunE:    runSpawn,
}

var startCmd = &cobra.Command{
	Use:   "start <name> <project> [flags]",
	Short: "Start a named agent",
	Long: `Start a new agent with an explicit name. This is the power-user variant
of 'gb spawn' for when you need control over the agent name.

  gb start fix-auth monorepo --role engineer --task kd-abc123
  gb start review-pr gasboat --prompt 'Review PR #42 for security issues'

Task-first mode (auto-generate agent name from task title):
  gb start gasboat --task kd-abc123

When --task is a ticket reference (e.g. PE-1234), it is resolved to a bead ID
and the project is inferred from the ticket's labels if not specified.`,
	GroupID: "agent",
	Args:    cobra.RangeArgs(1, 2),
	RunE:    runStart,
}

func init() {
	spawnCmd.Flags().String("role", "crew", "Agent role(s), comma-separated (e.g. crew, thread,crew)")
	spawnCmd.Flags().String("task", "", "Pre-assign a task bead (bead ID or ticket reference)")
	spawnCmd.Flags().String("prompt", "", "Custom prompt injected at agent startup")

	startCmd.Flags().String("role", "crew", "Agent role(s), comma-separated (e.g. crew, thread,crew)")
	startCmd.Flags().String("task", "", "Pre-assign a task bead (bead ID or ticket reference)")
	startCmd.Flags().String("prompt", "", "Custom prompt injected at agent startup")
}

// runSpawn handles 'gb spawn' — auto-generates agent name, project from env/task.
func runSpawn(cmd *cobra.Command, _ []string) error {
	role, _ := cmd.Flags().GetString("role")
	taskID, _ := cmd.Flags().GetString("task")
	customPrompt, _ := cmd.Flags().GetString("prompt")

	ctx := cmd.Context()

	// Resolve ticket references (e.g. PE-1234) to bead IDs.
	var taskProject string
	if taskID != "" && !strings.HasPrefix(taskID, "kd-") {
		if spawnTicketRefRe.MatchString(taskID) {
			bead, err := daemon.ResolveTicket(ctx, taskID)
			if err != nil {
				return fmt.Errorf("resolving ticket %q: %w", taskID, err)
			}
			taskID = bead.ID
			taskProject = spawnProjectFromLabels(bead.Labels)
		}
	}

	// Auto-generate agent name.
	agentName := ""
	if taskID != "" {
		taskBead, err := daemon.GetBead(ctx, taskID)
		if err != nil {
			return fmt.Errorf("looking up task %q: %w", taskID, err)
		}
		agentName = spawnGenerateAgentName(taskBead.Title)
	}

	// Resolve project.
	project := taskProject
	if project == "" {
		project = defaultProject()
	}
	if project == "" {
		return fmt.Errorf("project is required: set BOAT_PROJECT or use --task with a project-labeled bead")
	}

	// Fall back to project-based name if no task.
	if agentName == "" {
		agentName = spawnGenerateSpawnName(project)
	}

	return spawnAndPrint(cmd, agentName, project, taskID, role, customPrompt)
}

// runStart handles 'gb start' — explicit agent name required (old 'gb spawn' behavior).
func runStart(cmd *cobra.Command, args []string) error {
	role, _ := cmd.Flags().GetString("role")
	taskID, _ := cmd.Flags().GetString("task")
	customPrompt, _ := cmd.Flags().GetString("prompt")

	ctx := cmd.Context()

	// Resolve ticket references (e.g. PE-1234) to bead IDs.
	var taskProject string
	if taskID != "" && !strings.HasPrefix(taskID, "kd-") {
		if spawnTicketRefRe.MatchString(taskID) {
			bead, err := daemon.ResolveTicket(ctx, taskID)
			if err != nil {
				return fmt.Errorf("resolving ticket %q: %w", taskID, err)
			}
			taskID = bead.ID
			taskProject = spawnProjectFromLabels(bead.Labels)
		}
	}

	var agentName, project string

	switch len(args) {
	case 2:
		// gb start <name> <project>
		agentName = args[0]
		project = args[1]
	case 1:
		if taskID != "" {
			// Task-first mode: gb start <project> --task <id>
			project = args[0]
			taskBead, err := daemon.GetBead(ctx, taskID)
			if err != nil {
				return fmt.Errorf("looking up task %q: %w", taskID, err)
			}
			agentName = spawnGenerateAgentName(taskBead.Title)
		} else {
			// gb start <name> — project from env.
			agentName = args[0]
			project = defaultProject()
		}
	}

	// If task had a project label and no project was specified, use it.
	if project == "" && taskProject != "" {
		project = taskProject
	}

	// Fall back to env project.
	if project == "" {
		project = defaultProject()
	}

	if project == "" {
		return fmt.Errorf("project is required: specify as second argument or set BOAT_PROJECT")
	}

	// Validate agent name.
	if !spawnIsValidAgentName(agentName) {
		return fmt.Errorf("invalid agent name %q — use lowercase letters, digits, and hyphens only", agentName)
	}

	return spawnAndPrint(cmd, agentName, project, taskID, role, customPrompt)
}

// spawnAndPrint creates the agent bead and prints the result.
func spawnAndPrint(cmd *cobra.Command, agentName, project, taskID, role, customPrompt string) error {
	beadID, err := daemon.SpawnAgent(cmd.Context(), agentName, project, taskID, role, customPrompt)
	if err != nil {
		return fmt.Errorf("spawning agent %q: %w", agentName, err)
	}

	if jsonOutput {
		result := map[string]string{
			"id":      beadID,
			"name":    agentName,
			"project": project,
			"role":    role,
		}
		if taskID != "" {
			result["task"] = taskID
		}
		printJSON(result)
		return nil
	}

	fmt.Printf("Spawning agent %s\n", agentName)
	fmt.Printf("  Bead:    %s\n", beadID)
	fmt.Printf("  Project: %s\n", project)
	fmt.Printf("  Role:    %s\n", role)
	if taskID != "" {
		fmt.Printf("  Task:    %s\n", taskID)
	}
	if customPrompt != "" {
		preview := customPrompt
		if len(preview) > 60 {
			preview = preview[:57] + "..."
		}
		fmt.Printf("  Prompt:  %s\n", preview)
	}
	fmt.Println("\nThe reconciler will schedule a pod shortly. Use 'gb agent roster' to check status.")

	return nil
}

// --- helpers (local to spawn, mirroring bridge/bot_commands.go) ---

// spawnTicketRefRe matches external ticket references like "PE-1234".
var spawnTicketRefRe = regexp.MustCompile(`^[A-Za-z]+-\d+$`)

// spawnIsValidAgentName reports whether s is a valid agent name.
func spawnIsValidAgentName(s string) bool {
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

// spawnGenerateAgentName creates a slug from a task title.
func spawnGenerateAgentName(title string) string {
	words := strings.Fields(strings.ToLower(title))
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
	return strings.Join(slugWords, "-") + "-" + spawnRandomSuffix(3)
}

// spawnGenerateSpawnName creates a name from a project with a random suffix.
func spawnGenerateSpawnName(project string) string {
	prefix := project
	if prefix == "" {
		prefix = "agent"
	}
	return prefix + "-" + spawnRandomSuffix(4)
}

// spawnRandomSuffix returns a random string of n lowercase alphanumeric characters.
func spawnRandomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return string(b)
}

// spawnProjectFromLabels extracts the project name from a bead's labels.
func spawnProjectFromLabels(labels []string) string {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, "project:"); ok {
			return v
		}
	}
	return ""
}
