package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var setupReposCmd = &cobra.Command{
	Use:   "repos",
	Short: "Clone project repos into the workspace",
	Long: `Queries the project bead for the current BOAT_PROJECT and clones its
repos into {workspace}/repos/{name}.

The project bead's "repos" field is the source of truth. Each repo entry
has a url, optional name, optional branch, and a role (primary or reference).
All repos are cloned; the primary repo is also available at repos/{project}.

Repos that already exist (have a .git directory) are skipped.
Uses shallow clone (--depth 1) for speed. Requires git credentials
to be configured (gb agent start or entrypoint handles this).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		workspace, _ := cmd.Flags().GetString("workspace")
		if workspace == "" {
			workspace, _ = os.Getwd()
		}
		return runSetupRepos(cmd.Context(), workspace)
	},
}

func init() {
	setupReposCmd.Flags().String("workspace", os.Getenv("KD_WORKSPACE"), "workspace directory")
	setupCmd.AddCommand(setupReposCmd)
}

// repoCloneEntry is a resolved repo ready to clone.
type repoCloneEntry struct {
	Name string
	URL  string
}

// repoNameFromURL extracts the repository name from a URL.
// "https://github.com/org/my-repo.git" → "my-repo"
func repoNameFromURL(rawURL string) string {
	u := strings.TrimSuffix(rawURL, ".git")
	parts := strings.Split(u, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "repo"
}

// resolveReposFromProjectBead queries the daemon for the project bead and
// returns the repos to clone. Returns nil if no project bead is found.
func resolveReposFromProjectBead(ctx context.Context, project string) []repoCloneEntry {
	if daemon == nil {
		return nil
	}

	projects, err := daemon.ListProjectBeads(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[setup] warning: listing project beads: %v\n", err)
		return nil
	}

	info, ok := projects[project]
	if !ok {
		fmt.Fprintf(os.Stderr, "[setup] no project bead found for %q\n", project)
		return nil
	}

	var entries []repoCloneEntry

	if len(info.Repos) > 0 {
		// Multi-repo project: use the repos field.
		for _, r := range info.Repos {
			name := r.Name
			if name == "" {
				if r.Role == "primary" {
					name = project
				} else {
					name = repoNameFromURL(r.URL)
				}
			}
			entries = append(entries, repoCloneEntry{Name: name, URL: r.URL})
		}
	} else if info.GitURL != "" {
		// Legacy single-repo project: use git_url field.
		entries = append(entries, repoCloneEntry{Name: project, URL: info.GitURL})
	}

	return entries
}

// parseBoatProjectsEnv parses BOAT_PROJECTS as a fallback when no project
// beads exist. Format: "name=https://host/path:prefix,..."
func parseBoatProjectsEnv(raw string) []repoCloneEntry {
	var entries []repoCloneEntry
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eqIdx := strings.Index(part, "=")
		if eqIdx < 0 {
			continue
		}
		name := part[:eqIdx]
		rawURL := part[eqIdx+1:]
		// Strip trailing :prefix (e.g. ":PE") if present after the URL.
		if lastColon := strings.LastIndex(rawURL, ":"); lastColon > strings.Index(rawURL, "//") {
			rawURL = rawURL[:lastColon]
		}
		if name != "" && rawURL != "" {
			entries = append(entries, repoCloneEntry{Name: name, URL: rawURL})
		}
	}
	return entries
}

func runSetupRepos(ctx context.Context, workspace string) error {
	project := os.Getenv("BOAT_PROJECT")
	if project == "" {
		project = os.Getenv("KD_PROJECT")
	}
	if project == "" {
		fmt.Fprintf(os.Stderr, "[setup] no BOAT_PROJECT set — nothing to clone\n")
		return nil
	}

	// Primary source of truth: the project bead.
	entries := resolveReposFromProjectBead(ctx, project)

	// Fallback: parse BOAT_PROJECTS env var when no project bead exists.
	if len(entries) == 0 {
		if raw := os.Getenv("BOAT_PROJECTS"); raw != "" {
			fmt.Fprintf(os.Stderr, "[setup] no project bead repos — falling back to BOAT_PROJECTS env\n")
			entries = parseBoatProjectsEnv(raw)
		}
	}

	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "[setup] no repos to clone for project %q\n", project)
		return nil
	}

	reposDir := filepath.Join(workspace, "repos")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		return fmt.Errorf("creating repos dir: %w", err)
	}

	var cloned, skipped, failed int
	for _, e := range entries {
		dest := filepath.Join(reposDir, e.Name)
		if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
			fmt.Fprintf(os.Stderr, "[setup] repo already present: %s (skipping)\n", dest)
			skipped++
			continue
		}

		fmt.Fprintf(os.Stderr, "[setup] cloning %s → %s\n", e.URL, dest)
		out, err := exec.Command("git", "clone", "--depth", "1", "--quiet", e.URL, dest).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[setup] WARNING: clone failed for %s: %s\n", e.URL, strings.TrimSpace(string(out)))
			failed++
			continue
		}
		cloned++
	}

	fmt.Fprintf(os.Stderr, "[setup] repos: %d cloned, %d skipped, %d failed\n", cloned, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d repo(s) failed to clone", failed)
	}
	return nil
}
