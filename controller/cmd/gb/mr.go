package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/bridge"

	"github.com/spf13/cobra"
)

var (
	mrGitLabURL   string
	mrGitLabToken string
)

var mrCmd = &cobra.Command{
	Use:     "mr",
	Short:   "GitLab merge request commands",
	GroupID: "orchestration",
}

var mrStatusCmd = &cobra.Command{
	Use:   "status <bead-id-or-mr-url>",
	Short: "Show MR state, pipeline status, and merge details",
	Args:  cobra.ExactArgs(1),
	RunE:  runMRStatus,
}

var mrListCmd = &cobra.Command{
	Use:   "list",
	Short: "List beads that have an MR URL and their merge status",
	RunE:  runMRList,
}

var mrEnvCmd = &cobra.Command{
	Use:   "env <bead-id-or-mr-url>",
	Short: "Resolve deploy-mr environment URLs from CI artifacts",
	Long: `Finds the deploy-mr job in the MR's latest pipeline and downloads its
.urls/environment.env artifact to extract the actual deployment URLs.

This is the authoritative source for MR deployment URLs — it reflects what
the deploy job actually created, avoiding issues with local slug computation.

Output variables: TENANT_ID, UNITY_URL, HARMONY_URL, BASE_URL, KEYCLOAK_URL, etc.`,
	Args: cobra.ExactArgs(1),
	RunE: runMREnv,
}

func init() {
	mrCmd.PersistentFlags().StringVar(&mrGitLabURL, "gitlab-url", os.Getenv("GITLAB_BASE_URL"), "GitLab base URL")
	mrCmd.PersistentFlags().StringVar(&mrGitLabToken, "gitlab-token", os.Getenv("GITLAB_API_TOKEN"), "GitLab API token")

	mrEnvCmd.Flags().Bool("export", false, "Output as export VAR=value (shell-eval friendly)") // OK: "eval" is in help text, not code execution

	mrCmd.AddCommand(mrStatusCmd)
	mrCmd.AddCommand(mrListCmd)
	mrCmd.AddCommand(mrEnvCmd)
}

// resolveMRURL resolves a bead ID or MR URL argument to an MR URL string.
func resolveMRURL(cmd *cobra.Command, arg string) (string, error) {
	// If it looks like a URL, use it directly.
	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		return arg, nil
	}
	// Otherwise treat as bead ID.
	bead, err := daemon.GetBead(cmd.Context(), arg)
	if err != nil {
		return "", fmt.Errorf("fetching bead %s: %w", arg, err)
	}
	mrURL := bead.Fields["mr_url"]
	if mrURL == "" {
		return "", fmt.Errorf("bead %s has no mr_url field", arg)
	}
	return mrURL, nil
}

func newMRGitLabClient() (*bridge.GitLabClient, error) {
	if mrGitLabURL == "" {
		return nil, fmt.Errorf("--gitlab-url or GITLAB_BASE_URL is required")
	}
	return bridge.NewGitLabClient(bridge.GitLabClientConfig{
		BaseURL: mrGitLabURL,
		Token:   mrGitLabToken,
	}), nil
}

func runMRStatus(cmd *cobra.Command, args []string) error {
	mrURL, err := resolveMRURL(cmd, args[0])
	if err != nil {
		return err
	}

	ref := bridge.ParseMRURL(mrURL)
	if ref == nil {
		return fmt.Errorf("could not parse MR URL: %s", mrURL)
	}

	client, err := newMRGitLabClient()
	if err != nil {
		return err
	}

	mr, err := client.GetMergeRequestByPath(cmd.Context(), ref.ProjectPath, ref.IID)
	if err != nil {
		return fmt.Errorf("fetching MR !%d: %w", ref.IID, err)
	}

	if jsonOutput {
		printJSON(mr)
		return nil
	}

	cmd.Printf("MR:       !%d\n", mr.IID)
	cmd.Printf("Title:    %s\n", mr.Title)
	cmd.Printf("State:    %s\n", mr.State)
	if mr.Author != nil {
		cmd.Printf("Author:   %s\n", mr.Author.Username)
	}
	cmd.Printf("Project:  %s\n", ref.ProjectPath)
	cmd.Printf("URL:      %s\n", mr.WebURL)
	if mr.SHA != "" {
		cmd.Printf("SHA:      %s\n", mr.SHA)
	}
	if mr.HeadPipeline != nil && mr.HeadPipeline.ID != 0 {
		cmd.Printf("Pipeline: #%d (%s)\n", mr.HeadPipeline.ID, mr.HeadPipeline.Status)
		if mr.HeadPipeline.WebURL != "" {
			cmd.Printf("          %s\n", mr.HeadPipeline.WebURL)
		}
	}

	// Show bead-side approval info if the arg was a bead ID.
	if !strings.HasPrefix(args[0], "http://") && !strings.HasPrefix(args[0], "https://") {
		if bead, err := daemon.GetBead(cmd.Context(), args[0]); err == nil {
			if approved := bead.Fields["mr_approved"]; approved != "" {
				cmd.Printf("Approved: %s\n", approved)
			}
			if approvers := bead.Fields["mr_approvers"]; approvers != "" {
				cmd.Printf("Approvers: %s\n", approvers)
			}
		}
	}
	return nil
}

func runMREnv(cmd *cobra.Command, args []string) error {
	projectPath, pipelineID, err := resolvePipelineInfo(cmd, args[0])
	if err != nil {
		return err
	}

	client, err := newMRGitLabClient()
	if err != nil {
		return err
	}

	// Find the deploy-mr job.
	jobs, err := client.ListPipelineJobsByPath(cmd.Context(), projectPath, pipelineID)
	if err != nil {
		return fmt.Errorf("listing jobs: %w", err)
	}

	var deployJob *bridge.GitLabJob
	for i := range jobs {
		if jobs[i].Name == "deploy-mr" {
			deployJob = &jobs[i]
			break
		}
	}
	if deployJob == nil {
		return fmt.Errorf("no deploy-mr job found in pipeline #%d", pipelineID)
	}

	if deployJob.Status != "success" {
		return fmt.Errorf("deploy-mr job status is %q (expected success)", deployJob.Status)
	}

	// Download the .urls/environment.env artifact file.
	data, err := client.DownloadJobArtifactFile(cmd.Context(), projectPath, deployJob.ID, ".urls/environment.env")
	if err != nil {
		return fmt.Errorf("downloading environment.env from deploy-mr job %d: %w", deployJob.ID, err)
	}

	// Parse the env file into key=value pairs.
	type envVar struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	var vars []envVar
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			vars = append(vars, envVar{Key: k, Value: v})
		}
	}

	if jsonOutput {
		printJSON(vars)
		return nil
	}

	exportMode, _ := cmd.Flags().GetBool("export")
	for _, v := range vars {
		if exportMode {
			cmd.Printf("export %s=%s\n", v.Key, v.Value)
		} else {
			cmd.Printf("%s=%s\n", v.Key, v.Value)
		}
	}
	return nil
}

func runMRList(cmd *cobra.Command, _ []string) error {
	result, err := daemon.ListBeadsFiltered(cmd.Context(), beadsapi.ListBeadsQuery{
		Types:    []string{"task"},
		Statuses: []string{"open", "in_progress"},
		Limit:    100,
	})
	if err != nil {
		return fmt.Errorf("listing beads: %w", err)
	}

	// Filter to beads that have an mr_url field.
	type mrBead struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		MRURL    string `json:"mr_url"`
		MRMerged string `json:"mr_merged,omitempty"`
		MRState  string `json:"mr_state,omitempty"`
	}

	var beads []mrBead
	for _, b := range result.Beads {
		if u := b.Fields["mr_url"]; u != "" {
			beads = append(beads, mrBead{
				ID:       b.ID,
				Title:    b.Title,
				MRURL:    u,
				MRMerged: b.Fields["mr_merged"],
				MRState:  b.Fields["mr_state"],
			})
		}
	}

	if len(beads) == 0 {
		if jsonOutput {
			printJSON([]any{})
		} else {
			cmd.Println("No beads with MR URLs found")
		}
		return nil
	}

	if jsonOutput {
		printJSON(beads)
		return nil
	}

	cmd.Printf("%-16s %-10s %-8s %s\n", "BEAD", "MR STATE", "MERGED", "TITLE")
	cmd.Printf("%-16s %-10s %-8s %s\n",
		strings.Repeat("-", 16), strings.Repeat("-", 10), strings.Repeat("-", 8), strings.Repeat("-", 30))
	for _, b := range beads {
		state := b.MRState
		if state == "" {
			state = "-"
		}
		merged := b.MRMerged
		if merged == "" {
			merged = "-"
		}
		cmd.Printf("%-16s %-10s %-8s %s\n",
			truncateStr(b.ID, 16), state, merged, truncateStr(b.Title, 50))
	}
	return nil
}
