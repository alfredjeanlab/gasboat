// Command gb is the Gasboat agent orchestration CLI — a thin client that
// manages agent lifecycle, decisions, gates, hooks, mail, and session control.
//
// gb is to agents what kd is to beads: kd handles data (CRUD, queries, types),
// gb handles orchestration (spawn, gate, decide, prime, mail).
//
// All data access goes through the kbeads HTTP API via the shared beadsapi client.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var (
	httpURL    string
	jsonOutput bool
	actor      string

	daemon *beadsapi.Client
)

func defaultActor() string {
	if s := os.Getenv("KD_ACTOR"); s != "" {
		return s
	}
	out, err := exec.Command("git", "config", "user.name").Output()
	if err == nil {
		name := strings.TrimSpace(string(out))
		if name != "" {
			return name
		}
	}
	return "unknown"
}

// defaultProject returns the default project from environment variables.
// Precedence: KD_PROJECT > BOAT_PROJECT > "" (none).
func defaultProject() string {
	if p := os.Getenv("KD_PROJECT"); p != "" {
		return p
	}
	if p := os.Getenv("BOAT_PROJECT"); p != "" {
		return p
	}
	return ""
}

func defaultHTTPURL() string {
	if s := os.Getenv("BEADS_HTTP_URL"); s != "" {
		return s
	}
	if s := os.Getenv("BEADS_HTTP_ADDR"); s != "" {
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			return "http://" + s
		}
		return s
	}
	return "http://localhost:8080"
}

var rootCmd = &cobra.Command{
	Use:   "gb <command>",
	Short: "Gasboat agent orchestration CLI",
	Long: `gb manages agent lifecycle, decisions, gates, hooks, mail, and session control.

It is a client of the kbeads HTTP API — all data access goes through kd serve.
Use kd for bead CRUD (create, show, list, close); use gb for agent orchestration.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		c, err := beadsapi.New(beadsapi.Config{
			HTTPAddr: httpURL,
			Token:    os.Getenv("BEADS_DAEMON_TOKEN"),
		})
		if err != nil {
			return fmt.Errorf("failed to connect to beads daemon: %w", err)
		}
		daemon = c
		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if daemon != nil {
			daemon.Close()
		}
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&httpURL, "http-url", defaultHTTPURL(), "kbeads HTTP server URL")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	rootCmd.PersistentFlags().StringVar(&actor, "actor", defaultActor(), "actor name for operations")

	rootCmd.AddGroup(
		&cobra.Group{ID: "agent", Title: "Agent Lifecycle:"},
		&cobra.Group{ID: "orchestration", Title: "Orchestration:"},
		&cobra.Group{ID: "session", Title: "Session Control:"},
	)

	cobra.EnableCommandSorting = false

	// Agent Lifecycle
	rootCmd.AddCommand(agentCmd)

	// Orchestration
	rootCmd.AddCommand(gateCmd)
	rootCmd.AddCommand(decisionCmd)
	rootCmd.AddCommand(busCmd)
	rootCmd.AddCommand(hookCmd)
	rootCmd.AddCommand(mailCmd)
	rootCmd.AddCommand(inboxCmd)
	rootCmd.AddCommand(newsCmd)
	rootCmd.AddCommand(adviceCmd)
	rootCmd.AddCommand(attachmentsCmd)
	rootCmd.AddCommand(mrCmd)
	rootCmd.AddCommand(pipelineCmd)
	rootCmd.AddCommand(slackCmd)

	// Session Control
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(yieldCmd)
	rootCmd.AddCommand(readyCmd)
	rootCmd.AddCommand(primeCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(workspaceCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
