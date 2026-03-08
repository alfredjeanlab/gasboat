package main

// agent_k8s_workspace.go — K8s agent workspace setup.
//
// Handles: platform version, git global config, git credentials, workspace git
// init / stale-branch reset, daemon config, PVC persistence (symlinks), Claude
// user settings.json, hook materialization, CLAUDE.md generation, and
// onboarding skip.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// setupWorkspace performs all one-time workspace setup that runs before coop starts.
func setupWorkspace(cfg k8sConfig) error {
	// 1. Platform version.
	if data, err := os.ReadFile("/etc/platform-version"); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			os.Setenv("BEADS_PLATFORM_VERSION", v)
			fmt.Printf("[gb agent start] platform version: %s\n", v)
		}
	}

	// 2. Git global config.
	authorName := envOr("GIT_AUTHOR_NAME", cfg.role)
	runGitGlobal("config", "--global", "user.name", authorName)
	runGitGlobal("config", "--global", "user.email", cfg.role+"@gasboat.local")
	runGitGlobal("config", "--global", "--add", "safe.directory", "*")
	fmt.Printf("[gb agent start] git global config set (user: %s)\n", authorName)

	// 3. Git credentials — derive hosts from BOAT_PROJECTS repo URLs.
	gitUser := os.Getenv("GIT_USERNAME")
	gitToken := os.Getenv("GIT_TOKEN")
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	if gitUser != "" && gitToken != "" || gitlabToken != "" {
		hosts := repoHosts(os.Getenv("BOAT_PROJECTS"))
		if err := writeGitCredentials(gitUser, gitToken, gitlabToken, hosts); err != nil {
			fmt.Printf("[gb agent start] warning: git credentials: %v\n", err)
		} else {
			fmt.Printf("[gb agent start] git credentials configured for %v\n", hosts)
		}
	}

	// 4. Workspace git init or stale-branch reset.
	if _, err := os.Stat(filepath.Join(cfg.workspace, ".git")); os.IsNotExist(err) {
		fmt.Printf("[gb agent start] initialising git repo in %s\n", cfg.workspace)
		if err := runGitIn(cfg.workspace, "init", "-q"); err != nil {
			fmt.Printf("[gb agent start] warning: git init: %v\n", err)
		}
		_ = runGitIn(cfg.workspace, "config", "user.name", authorName)
		_ = runGitIn(cfg.workspace, "config", "user.email", cfg.role+"@gasboat.local")
	} else {
		fmt.Printf("[gb agent start] git repo already exists in %s\n", cfg.workspace)
		resetStaleBranch(cfg.workspace)
	}

	// 5. Daemon config (.beads/config.yaml).
	if host := os.Getenv("BEADS_DAEMON_HOST"); host != "" {
		port := envOr("BEADS_DAEMON_HTTP_PORT", "9080")
		daemonURL := fmt.Sprintf("http://%s:%s", host, port)
		fmt.Printf("[gb agent start] configuring daemon at %s\n", daemonURL)
		if err := writeDaemonConfig(cfg.workspace, daemonURL); err != nil {
			fmt.Printf("[gb agent start] warning: daemon config: %v\n", err)
		}
	}

	// 6. Clone project repos (source of truth: project bead, fallback: BOAT_PROJECTS env).
	if err := runSetupRepos(context.Background(), cfg.workspace); err != nil {
		fmt.Printf("[gb agent start] warning: repo clone: %v\n", err)
	}

	return nil
}

// setupPVC creates the .state/{claude,coop} directories on the PVC and
// symlinks ~/.claude into .state/claude so Claude state survives pod restarts.
// Returns the resolved claude state directory path — this is ~/.claude when it
// is a direct PVC subPath mount, or .state/claude when using symlinks.
func setupPVC(cfg k8sConfig) (string, error) {
	stateDir := filepath.Join(cfg.workspace, ".state")
	claudeState := filepath.Join(stateDir, "claude")
	coopState := filepath.Join(stateDir, "coop")

	for _, d := range []string{claudeState, coopState} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	claudeDir := filepath.Join(homeDir(), ".claude")
	if isMountpoint(claudeDir) {
		fmt.Printf("[gb agent start] %s is a mount point (subPath) — already PVC-backed\n", claudeDir)
		// When ~/.claude is a direct mount, session logs live there, not in .state/claude.
		claudeState = claudeDir
	} else {
		os.RemoveAll(claudeDir)
		if err := os.Symlink(claudeState, claudeDir); err != nil {
			return "", fmt.Errorf("symlink %s -> %s: %w", claudeDir, claudeState, err)
		}
		fmt.Printf("[gb agent start] linked %s -> %s\n", claudeDir, claudeState)
	}

	os.Setenv("XDG_STATE_HOME", stateDir)
	fmt.Printf("[gb agent start] XDG_STATE_HOME=%s\n", stateDir)

	// Dev tools PATH.
	if _, err := os.Stat("/usr/local/go/bin"); err == nil {
		os.Setenv("PATH", "/usr/local/go/bin:"+os.Getenv("PATH"))
		fmt.Printf("[gb agent start] added /usr/local/go/bin to PATH\n")
	}

	return claudeState, nil
}

// symlinkClaudeExtensions symlinks .claude/agents, .claude/skills, and
// .claude/commands from the project repo checkout into the agent workspace.
// This makes custom Claude Code agents, skills, and commands discoverable
// when the agent workspace is separate from the project repo (K8s layout).
func symlinkClaudeExtensions(workspace string) {
	project := os.Getenv("BOAT_PROJECT")
	if project == "" {
		return
	}

	// Project repo is cloned by init-clone to /home/agent/bot/{project}/work.
	projectWork := filepath.Join("/home/agent/bot", project, "work")
	if _, err := os.Stat(projectWork); os.IsNotExist(err) {
		return
	}

	claudeDir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		fmt.Printf("[gb agent start] warning: create .claude dir: %v\n", err)
		return
	}

	for _, sub := range []string{"agents", "skills", "commands"} {
		src := filepath.Join(projectWork, ".claude", sub)
		dst := filepath.Join(claudeDir, sub)

		// Source must exist in the project repo.
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}

		// Skip if destination already exists (file, dir, or symlink).
		if _, err := os.Lstat(dst); err == nil {
			continue
		}

		if err := os.Symlink(src, dst); err != nil {
			fmt.Printf("[gb agent start] warning: symlink %s -> %s: %v\n", dst, src, err)
		} else {
			fmt.Printf("[gb agent start] linked %s -> %s\n", dst, src)
		}
	}
}


// writeOnboardingSkip writes ~/.claude.json to bypass the onboarding wizard.
func writeOnboardingSkip() {
	data := []byte(`{"hasCompletedOnboarding":true,"lastOnboardingVersion":"2.1.37","preferredTheme":"dark","bypassPermissionsModeAccepted":true}` + "\n")
	path := filepath.Join(homeDir(), ".claude.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fmt.Printf("[gb agent start] warning: write .claude.json: %v\n", err)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func runGitGlobal(args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func runGitIn(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// resetStaleBranch resets the main workspace to main/master if it is on a
// stale feature branch — but only when no per-bead worktrees are present.
// When worktrees exist the main repo acts as a coordinator and should not be
// reset: agents work inside individual worktrees under .beads/worktrees/.
func resetStaleBranch(workspace string) {
	out, err := exec.Command("git", "-C", workspace, "branch", "--show-current").Output()
	if err != nil {
		return
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "main" || branch == "master" {
		return
	}

	// If per-bead worktrees exist, warn but do not destroy the main repo state.
	// Agents working in worktrees manage their own branches independently.
	wtDir := filepath.Join(workspace, ".beads", "worktrees")
	if entries, err := os.ReadDir(wtDir); err == nil && len(entries) > 0 {
		fmt.Printf("[gb agent start] workspace on branch '%s' with %d active worktree(s) — skipping reset\n",
			branch, len(entries))
		return
	}

	fmt.Printf("[gb agent start] WARNING: workspace on stale branch '%s', resetting to main\n", branch)
	_ = runGitIn(workspace, "checkout", "--", ".")
	_ = runGitIn(workspace, "clean", "-fd")
	if runGitIn(workspace, "checkout", "main") != nil {
		_ = runGitIn(workspace, "checkout", "-b", "main")
	}
	out, _ = exec.Command("git", "-C", workspace, "branch", "--show-current").Output()
	fmt.Printf("[gb agent start] workspace now on branch: %s\n", strings.TrimSpace(string(out)))
}

// repoHosts extracts unique hostnames from BOAT_PROJECTS.
// Format: "name=https://host/path:prefix,name2=https://host2/path:prefix"
func repoHosts(boatProjects string) []string {
	seen := map[string]bool{}
	for _, entry := range strings.Split(boatProjects, ",") {
		entry = strings.TrimSpace(entry)
		// name=https://host/path:prefix — URL is between '=' and last ':'
		eqIdx := strings.Index(entry, "=")
		if eqIdx < 0 {
			continue
		}
		rawURL := entry[eqIdx+1:]
		// Strip trailing :prefix if present.
		if lastColon := strings.LastIndex(rawURL, ":"); lastColon > strings.Index(rawURL, "//") {
			rawURL = rawURL[:lastColon]
		}
		// Extract host from URL.
		if strings.HasPrefix(rawURL, "https://") {
			host := strings.SplitN(rawURL[len("https://"):], "/", 2)[0]
			if host != "" {
				seen[host] = true
			}
		}
	}
	var hosts []string
	for h := range seen {
		hosts = append(hosts, h)
	}
	return hosts
}

func writeGitCredentials(user, token, gitlabToken string, hosts []string) error {
	home := homeDir()
	credFile := filepath.Join(home, ".git-credentials")
	var lines string
	for _, host := range hosts {
		// Use GITLAB_TOKEN for gitlab.com, GIT_USERNAME/GIT_TOKEN for everything else.
		if strings.Contains(host, "gitlab") && gitlabToken != "" {
			lines += fmt.Sprintf("https://oauth2:%s@%s\n", gitlabToken, host)
		} else if user != "" && token != "" {
			lines += fmt.Sprintf("https://%s:%s@%s\n", user, token, host)
		}
	}
	if lines == "" {
		return nil
	}
	if err := os.WriteFile(credFile, []byte(lines), 0o600); err != nil {
		return err
	}
	return exec.Command("git", "config", "--global", "credential.helper",
		"store --file="+credFile).Run()
}

func writeDaemonConfig(workspace, daemonURL string) error {
	dir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("daemon-host: %q\ndaemon-token: %q\n",
		daemonURL, os.Getenv("BEADS_DAEMON_TOKEN"))
	return os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600)
}

// isMountpoint checks /proc/mounts to see if path is a mount point.
func isMountpoint(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == abs {
			return true
		}
	}
	return false
}


func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return "/home/agent"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
