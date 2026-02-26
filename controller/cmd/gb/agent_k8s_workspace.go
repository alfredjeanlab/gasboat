package main

// agent_k8s_workspace.go — K8s agent workspace setup.
//
// Handles: platform version, git global config, git credentials, workspace git
// init / stale-branch reset, daemon config, PVC persistence (symlinks), Claude
// user settings.json, hook materialization, CLAUDE.md generation, and
// onboarding skip.

import (
	"bufio"
	"encoding/json"
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

	return nil
}

// setupPVC creates the .state/{claude,coop} directories on the PVC and
// symlinks ~/.claude into .state/claude so Claude state survives pod restarts.
func setupPVC(cfg k8sConfig) error {
	stateDir := filepath.Join(cfg.workspace, ".state")
	claudeState := filepath.Join(stateDir, "claude")
	coopState := filepath.Join(stateDir, "coop")

	for _, d := range []string{claudeState, coopState} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	claudeDir := filepath.Join(homeDir(), ".claude")
	if isMountpoint(claudeDir) {
		fmt.Printf("[gb agent start] %s is a mount point (subPath) — already PVC-backed\n", claudeDir)
	} else {
		os.RemoveAll(claudeDir)
		if err := os.Symlink(claudeState, claudeDir); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", claudeDir, claudeState, err)
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

	return nil
}

// writeClaudeSettings writes ~/.claude/settings.json with tool permissions and
// any detected LSP plugins.
func writeClaudeSettings(claudeDir string) error {
	type settings struct {
		Permissions    map[string][]string `json:"permissions"`
		EnabledPlugins map[string]bool     `json:"enabledPlugins,omitempty"`
	}

	s := settings{
		Permissions: map[string][]string{
			"allow": {"Bash(*)", "Read(*)", "Write(*)", "Edit(*)", "Glob(*)", "Grep(*)", "WebFetch(*)", "WebSearch(*)"},
			"deny":  {},
		},
	}

	plugins := map[string]bool{}
	if _, err := exec.LookPath("gopls"); err == nil {
		plugins["gopls-lsp@claude-plugins-official"] = true
		fmt.Printf("[gb agent start] enabling gopls LSP plugin\n")
	}
	if _, err := exec.LookPath("rust-analyzer"); err == nil {
		plugins["rust-analyzer-lsp@claude-plugins-official"] = true
		fmt.Printf("[gb agent start] enabling rust-analyzer LSP plugin\n")
	}
	if len(plugins) > 0 {
		s.EnabledPlugins = plugins
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(claudeDir, "settings.json"), append(data, '\n'), 0o644)
}

// writeClaudeMD writes CLAUDE.md into the workspace if it doesn't already
// exist, then appends the dev-tools table if the guard string is absent.
func writeClaudeMD(cfg k8sConfig) {
	path := filepath.Join(cfg.workspace, "CLAUDE.md")

	if _, err := os.Stat(path); os.IsNotExist(err) {
		projectLine := ""
		if cfg.project != "" {
			projectLine = " (project: " + cfg.project + ")"
		}
		content := fmt.Sprintf(`# Gasboat Agent: %s

You are the **%s** agent%s.
Agent name: %s

## Quick Reference

- `+"`"+`gb ready`+"`"+` — See your workflow steps
- `+"`"+`gb mail inbox`+"`"+` — Check messages
- `+"`"+`kd show <issue>`+"`"+` — View specific issue details

## Checkpoint Protocol (Stop Hook)

When you hit a Stop hook block, you MUST create a decision checkpoint:

1. Review what you accomplished
2. Create a decision:
   `+"```"+`bash
   gb decision create --no-wait \
     --prompt="<what you did and why these options>" \
     --options='[{"id":"opt1","short":"Option 1","label":"Full description"}]'
   `+"```"+`
3. Run `+"`"+`gb yield`+"`"+` — blocks until human responds
4. When `+"`"+`gb yield`+"`"+` returns, act on the response
`, cfg.role, cfg.role, projectLine, cfg.agent)

		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			fmt.Printf("[gb agent start] warning: write CLAUDE.md: %v\n", err)
			return
		}
	}

	// Append dev-tools section if not already present.
	if !fileContains(path, "## Development Tools") {
		devTools := `
## Development Tools

All tools are installed directly in the agent image — use them from the command line.

| Tool | Command | Notes |
|------|---------|-------|
| Go | ` + "`go build`, `go test`" + ` | + ` + "`gopls`" + ` LSP server |
| Node.js | ` + "`node`, `npm`, `npx`" + ` | |
| Python 3 | ` + "`python3`, `pip`, `python3 -m venv`" + ` | |
| Rust | ` + "`rust-analyzer`" + ` | LSP server (no compiler — use ` + "`rustup`" + ` if needed) |
| AWS CLI | ` + "`aws`" + ` | |
| Docker CLI | ` + "`docker`" + ` | Client only (no daemon) |
| kubectl | ` + "`kubectl`" + ` | |
| git | ` + "`git`" + ` | HTTPS + SSH protocols |
| Build tools | ` + "`make`, `gcc`, `g++`" + ` | |
| Utilities | ` + "`curl`, `jq`, `unzip`, `ssh`" + ` | |
`
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(devTools)
			f.Close()
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

func fileContains(path, substr string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), substr)
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
