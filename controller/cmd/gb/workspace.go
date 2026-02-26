package main

// workspace.go — gb workspace subcommands for per-bead git worktree isolation.
//
// Problem: Agents carry over git state between beads when the shared PVC
// workspace has a stale feature branch. This creates MRs that bundle
// unrelated changes.
//
// Solution: Each bead gets its own git worktree at:
//   {workspace}/.beads/worktrees/{bead-id}/
//
// Phase 1: setup, teardown, list, audit + Fields metadata, dep-chain base.
// Phase 2: sync (stacked rebase), reaper, PVC monitoring.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// WorkspaceInfo is stored as a JSON string in Bead.Fields["workspace"].
type WorkspaceInfo struct {
	Branch      string `json:"branch"`
	WorktreePath string `json:"worktree_path"`
	BaseBranch  string `json:"base_branch"`
}

var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Short:   "Per-bead git worktree isolation",
	GroupID: "session",
}

// ── gb workspace setup ────────────────────────────────────────────────────

var workspaceSetupCmd = &cobra.Command{
	Use:   "setup <bead-id>",
	Short: "Create a git worktree for a bead",
	Long: `Creates a git worktree at {workspace}/.beads/worktrees/{bead-id}/ with a
new branch derived from the bead title (or --branch). The base branch is
auto-resolved from the dep chain (or --base).

Workspace info is stored in Bead.Fields["workspace"] so agents can recover
the worktree path after a pod restart.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		beadID := args[0]
		workspace := workspaceFlagOrCwd(cmd)
		baseBranch, _ := cmd.Flags().GetString("base")
		branchName, _ := cmd.Flags().GetString("branch")
		ctx := cmd.Context()

		// Fetch bead to get title for branch derivation.
		bead, err := daemon.GetBead(ctx, beadID)
		if err != nil {
			return fmt.Errorf("fetching bead %s: %w", beadID, err)
		}

		if branchName == "" {
			branchName = deriveBranchName(bead.Title, beadID)
		}
		if baseBranch == "" {
			baseBranch = resolveBaseBranch(ctx, beadID)
		}

		worktreePath := filepath.Join(workspace, ".beads", "worktrees", beadID)
		relPath := filepath.Join(".beads", "worktrees", beadID)

		// Check if worktree already exists.
		if _, err := os.Stat(worktreePath); err == nil {
			fmt.Fprintf(os.Stderr, "[workspace] worktree already exists at %s\n", worktreePath)
			return nil
		}

		// Fetch latest from remote so base is current.
		if out, err := exec.CommandContext(ctx, "git", "-C", workspace, "fetch", "origin").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "[workspace] warning: git fetch: %s\n", strings.TrimSpace(string(out)))
		}

		// Create worktree with new branch from base.
		out, err := exec.CommandContext(ctx, "git", "-C", workspace,
			"worktree", "add", "-b", branchName, worktreePath, baseBranch).CombinedOutput()
		if err != nil {
			// Branch may already exist locally — try without -b.
			out2, err2 := exec.CommandContext(ctx, "git", "-C", workspace,
				"worktree", "add", worktreePath, branchName).CombinedOutput()
			if err2 != nil {
				return fmt.Errorf("creating worktree: %s\n%s", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
			}
		}

		// Store workspace info in bead fields for recovery after pod restart.
		info := WorkspaceInfo{
			Branch:       branchName,
			WorktreePath: relPath,
			BaseBranch:   baseBranch,
		}
		if err := storeWorkspaceInfo(ctx, beadID, info); err != nil {
			fmt.Fprintf(os.Stderr, "[workspace] warning: storing workspace info: %v\n", err)
		}

		fmt.Printf("[workspace] created worktree at %s\n  branch:  %s\n  base:    %s\n", worktreePath, branchName, baseBranch)
		fmt.Printf("[workspace] switch to worktree: cd %s\n", worktreePath)
		return nil
	},
}

// ── gb workspace teardown ─────────────────────────────────────────────────

var workspaceTeardownCmd = &cobra.Command{
	Use:   "teardown <bead-id>",
	Short: "Remove the git worktree for a bead",
	Long:  `Removes the git worktree and clears workspace metadata from the bead.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		beadID := args[0]
		workspace := workspaceFlagOrCwd(cmd)
		ctx := cmd.Context()

		worktreePath := filepath.Join(workspace, ".beads", "worktrees", beadID)

		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "[workspace] no worktree found at %s\n", worktreePath)
			return nil
		}

		// Remove via git worktree remove (handles .git cleanup).
		out, err := exec.CommandContext(ctx, "git", "-C", workspace,
			"worktree", "remove", "--force", worktreePath).CombinedOutput()
		if err != nil {
			// Fallback: manual removal if git doesn't know about it.
			fmt.Fprintf(os.Stderr, "[workspace] git worktree remove: %s — falling back to rm\n", strings.TrimSpace(string(out)))
			if err2 := os.RemoveAll(worktreePath); err2 != nil {
				return fmt.Errorf("removing worktree %s: %w", worktreePath, err2)
			}
		}

		// Prune stale worktree refs.
		_ = exec.CommandContext(ctx, "git", "-C", workspace, "worktree", "prune").Run()

		// Clear workspace info from bead fields.
		if err := storeWorkspaceInfo(ctx, beadID, WorkspaceInfo{}); err != nil {
			fmt.Fprintf(os.Stderr, "[workspace] warning: clearing workspace info: %v\n", err)
		}

		fmt.Printf("[workspace] removed worktree at %s\n", worktreePath)
		return nil
	},
}

// ── gb workspace list ─────────────────────────────────────────────────────

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show active git worktrees",
	RunE: func(cmd *cobra.Command, args []string) error {
		workspace := workspaceFlagOrCwd(cmd)
		out, err := exec.CommandContext(cmd.Context(), "git", "-C", workspace, "worktree", "list").Output()
		if err != nil {
			return fmt.Errorf("listing worktrees: %w", err)
		}
		fmt.Print(string(out))
		return nil
	},
}

// ── gb workspace audit ────────────────────────────────────────────────────

var workspaceAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Scan worktrees for dirty state (Phase 1: warn only)",
	Long: `Scans all per-bead worktrees under {workspace}/.beads/worktrees/.
Warns about dirty worktrees (uncommitted changes or untracked files).
Exits 0 even when dirty — non-blocking by design for SessionStart hooks.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		workspace := workspaceFlagOrCwd(cmd)
		wtDir := filepath.Join(workspace, ".beads", "worktrees")

		entries, err := os.ReadDir(wtDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // No worktrees yet — OK.
			}
			return fmt.Errorf("reading worktrees dir: %w", err)
		}

		dirty := 0
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			wtPath := filepath.Join(wtDir, e.Name())
			if isDirtyWorktree(wtPath) {
				fmt.Printf("<system-reminder>[workspace] WARNING: worktree %s has uncommitted changes (bead: %s)</system-reminder>\n",
					wtPath, e.Name())
				dirty++
			}
		}

		if dirty > 0 {
			fmt.Printf("<system-reminder>[workspace] %d worktree(s) have uncommitted changes — commit or stash before switching beads</system-reminder>\n", dirty)
		}

		return nil
	},
}

func init() {
	for _, sub := range []*cobra.Command{workspaceSetupCmd, workspaceTeardownCmd, workspaceListCmd, workspaceAuditCmd} {
		sub.Flags().String("workspace", os.Getenv("KD_WORKSPACE"), "workspace root directory")
	}
	workspaceSetupCmd.Flags().String("base", "", "base branch (default: auto-resolve from dep chain, fallback: origin/main)")
	workspaceSetupCmd.Flags().String("branch", "", "branch name (default: derived from bead title)")

	workspaceCmd.AddCommand(workspaceSetupCmd)
	workspaceCmd.AddCommand(workspaceTeardownCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceAuditCmd)
}

// ── helpers ───────────────────────────────────────────────────────────────

var issueRe = regexp.MustCompile(`\[([A-Z]+-\d+)\]`)

// deriveBranchName returns a branch name like "fix/pe-6916" from the bead title.
// Falls back to "fix/{bead-id}" if no issue number is found.
func deriveBranchName(title, beadID string) string {
	if m := issueRe.FindStringSubmatch(title); len(m) >= 2 {
		return "fix/" + strings.ToLower(m[1])
	}
	// Sanitize bead ID to a valid branch name component.
	safe := strings.ToLower(strings.ReplaceAll(beadID, " ", "-"))
	return "fix/" + safe
}

// resolveBaseBranch walks the direct dependencies of beadID looking for a
// parent that already has workspace metadata. Returns that parent's branch
// so stacked worktrees are based on each other. Falls back to "origin/main".
func resolveBaseBranch(ctx context.Context, beadID string) string {
	deps, err := daemon.GetDependencies(ctx, beadID)
	if err != nil || len(deps) == 0 {
		return "origin/main"
	}
	for _, dep := range deps {
		parent, err := daemon.GetBead(ctx, dep.DependsOnID)
		if err != nil {
			continue
		}
		if wsStr, ok := parent.Fields["workspace"]; ok && wsStr != "" {
			var ws WorkspaceInfo
			if err := json.Unmarshal([]byte(wsStr), &ws); err == nil && ws.Branch != "" {
				fmt.Fprintf(os.Stderr, "[workspace] resolved base from dep %s: %s\n", dep.DependsOnID, ws.Branch)
				return ws.Branch
			}
		}
	}
	return "origin/main"
}

// storeWorkspaceInfo writes WorkspaceInfo (as a JSON string) into Bead.Fields["workspace"].
// An empty WorkspaceInfo clears the field.
func storeWorkspaceInfo(ctx context.Context, beadID string, info WorkspaceInfo) error {
	var value string
	if info.Branch != "" {
		b, err := json.Marshal(info)
		if err != nil {
			return err
		}
		value = string(b)
	}
	return daemon.UpdateBeadFields(ctx, beadID, map[string]string{"workspace": value})
}

// workspaceFlagOrCwd returns the --workspace flag value or the current directory.
func workspaceFlagOrCwd(cmd *cobra.Command) string {
	if ws, _ := cmd.Flags().GetString("workspace"); ws != "" {
		return ws
	}
	cwd, _ := os.Getwd()
	return cwd
}

// isDirtyWorktree returns true if the given worktree path has uncommitted
// changes or untracked files.
func isDirtyWorktree(path string) bool {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}
