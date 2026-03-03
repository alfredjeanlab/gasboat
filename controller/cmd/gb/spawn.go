package main

// gb spawn — CLI equivalent of Slack /spawn.
//
// Usage:
//
//	gb spawn <name> <project> [flags]
//	gb spawn <project> --task <bead-id>      (task-first: auto-generate name)
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
	Use:   "spawn <name> <project> [flags]",
	Short: "Spawn a new agent",
	Long: `Spawn a new agent by creating an agent bead. The reconciler picks it up
and schedules a K8s pod.

  gb spawn fix-auth monorepo --role engineer --task kd-abc123
  gb spawn review-pr gasboat --prompt 'Review PR #42 for security issues'
  gb spawn repro-bug monorepo --role engineer --task kd-xyz789

Task-first mode (auto-generate agent name from task title):
  gb spawn gasboat --task kd-abc123

When --task is a ticket reference (e.g. PE-1234), it is resolved to a bead ID
and the project is inferred from the ticket's labels if not specified.`,
	GroupID: "agent",
	Args:    cobra.RangeArgs(1, 2),
	RunE:    runSpawn,
}

func init() {
	spawnCmd.Flags().String("role", "crew", "Agent role (e.g. crew, captain, engineer)")
	spawnCmd.Flags().String("task", "", "Pre-assign a task bead (bead ID or ticket reference)")
	spawnCmd.Flags().String("prompt", "", "Custom prompt injected at agent startup")
}

func runSpawn(cmd *cobra.Command, args []string) error {
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
		// gb spawn <name> <project>
		agentName = args[0]
		project = args[1]
	case 1:
		if taskID != "" {
			// Task-first mode: gb spawn <project> --task <id>
			project = args[0]
			// Auto-generate agent name from task title.
			taskBead, err := daemon.GetBead(ctx, taskID)
			if err != nil {
				return fmt.Errorf("looking up task %q: %w", taskID, err)
			}
			agentName = spawnGenerateAgentName(taskBead.Title)
		} else {
			// gb spawn <name> — project from env.
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

	beadID, err := daemon.SpawnAgent(ctx, agentName, project, taskID, role, customPrompt)
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
