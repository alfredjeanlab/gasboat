package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a bare remote + clone with one initial commit.
// Returns (workdir, cleanup).
func initGitRepo(t *testing.T) (string, func()) {
	t.Helper()
	tmp := t.TempDir()

	bare := filepath.Join(tmp, "remote.git")
	work := filepath.Join(tmp, "work")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create a bare remote and clone it.
	run(tmp, "git", "init", "--bare", "-b", "main", bare)
	run(tmp, "git", "clone", bare, work)

	// Initial commit so main branch exists.
	f := filepath.Join(work, "README.md")
	if err := os.WriteFile(f, []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "git", "add", "README.md")
	run(work, "git", "commit", "-m", "initial commit")
	run(work, "git", "push", "-u", "origin", "main")

	return work, func() {} // t.TempDir handles cleanup
}

func TestCurrentBranch(t *testing.T) {
	work, cleanup := initGitRepo(t)
	defer cleanup()

	got := currentBranch(work)
	if got != "main" {
		t.Errorf("currentBranch = %q, want %q", got, "main")
	}

	// Switch to a feature branch.
	cmd := exec.Command("git", "-C", work, "checkout", "-b", "feat/test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}
	got = currentBranch(work)
	if got != "feat/test" {
		t.Errorf("currentBranch = %q, want %q", got, "feat/test")
	}
}

func TestCurrentBranch_NotARepo(t *testing.T) {
	got := currentBranch(t.TempDir())
	if got != "unknown" {
		t.Errorf("currentBranch(non-repo) = %q, want %q", got, "unknown")
	}
}

func TestCheckDelivery_NothingToDeliver(t *testing.T) {
	work, cleanup := initGitRepo(t)
	defer cleanup()

	t.Setenv("WORKSPACE", work)

	// Everything pushed — checkDelivery should pass.
	if err := checkDelivery(); err != nil {
		t.Errorf("checkDelivery() = %v, want nil (all pushed)", err)
	}
}

func TestCheckDelivery_UnpushedCommitsOnMain(t *testing.T) {
	work, cleanup := initGitRepo(t)
	defer cleanup()

	t.Setenv("WORKSPACE", work)

	// Make an unpushed commit on main.
	f := filepath.Join(work, "new.txt")
	if err := os.WriteFile(f, []byte("change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "-C", work, "add", "new.txt")
	run("git", "-C", work, "commit", "-m", "local only")

	err := checkDelivery()
	if err == nil {
		t.Fatal("checkDelivery() = nil, want error for unpushed commits on main")
	}
	if got := err.Error(); !strings.Contains(got, "unpushed") {
		t.Errorf("error = %q, want it to mention 'unpushed'", got)
	}
}

func TestCheckDelivery_UnpushedBranch(t *testing.T) {
	work, cleanup := initGitRepo(t)
	defer cleanup()

	t.Setenv("WORKSPACE", work)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Create a feature branch with a commit, don't push.
	run("git", "-C", work, "checkout", "-b", "feat/unpushed")
	f := filepath.Join(work, "feat.txt")
	if err := os.WriteFile(f, []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "-C", work, "add", "feat.txt")
	run("git", "-C", work, "commit", "-m", "feature work")

	err := checkDelivery()
	if err == nil {
		t.Fatal("checkDelivery() = nil, want error for unpushed branch")
	}
	if got := err.Error(); !strings.Contains(got, "feat/unpushed") {
		t.Errorf("error = %q, want it to mention branch name", got)
	}
}

func TestCheckDelivery_PushedBranch(t *testing.T) {
	work, cleanup := initGitRepo(t)
	defer cleanup()

	t.Setenv("WORKSPACE", work)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Create and push a feature branch.
	run("git", "-C", work, "checkout", "-b", "feat/pushed")
	f := filepath.Join(work, "feat.txt")
	if err := os.WriteFile(f, []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "-C", work, "add", "feat.txt")
	run("git", "-C", work, "commit", "-m", "feature work")
	run("git", "-C", work, "push", "-u", "origin", "feat/pushed")

	// All pushed — should pass.
	if err := checkDelivery(); err != nil {
		t.Errorf("checkDelivery() = %v, want nil (branch pushed)", err)
	}
}

func TestCheckDelivery_NotAGitRepo(t *testing.T) {
	t.Setenv("WORKSPACE", t.TempDir())

	// No git repo — should pass silently.
	if err := checkDelivery(); err != nil {
		t.Errorf("checkDelivery() = %v, want nil (not a git repo)", err)
	}
}

func TestProcessWrapUp_ValidJSON(t *testing.T) {
	raw := `{"accomplishments":"Closed 3 bugs","blockers":"API key pending","handoff_notes":"Check PR #42"}`

	w, comment, err := processWrapUp(raw)
	if err != nil {
		t.Fatalf("processWrapUp: %v", err)
	}
	if w.Accomplishments != "Closed 3 bugs" {
		t.Errorf("Accomplishments = %q, want %q", w.Accomplishments, "Closed 3 bugs")
	}
	if w.Blockers != "API key pending" {
		t.Errorf("Blockers = %q, want %q", w.Blockers, "API key pending")
	}
	if comment == "" {
		t.Error("comment should not be empty")
	}
	if !strings.Contains(comment, "Closed 3 bugs") {
		t.Errorf("comment missing accomplishments: %q", comment)
	}
}

func TestProcessWrapUp_InvalidJSON(t *testing.T) {
	_, _, err := processWrapUp("not json")
	if err == nil {
		t.Fatal("processWrapUp should fail on invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid --wrapup JSON") {
		t.Errorf("error = %q, want it to mention 'invalid --wrapup JSON'", err.Error())
	}
}

func TestProcessWrapUp_EmptyObject(t *testing.T) {
	w, _, err := processWrapUp("{}")
	if err != nil {
		t.Fatalf("processWrapUp: %v", err)
	}
	if w.Accomplishments != "" {
		t.Errorf("Accomplishments = %q, want empty", w.Accomplishments)
	}
}

func TestProcessWrapUp_WithBeadsAndPRs(t *testing.T) {
	raw := `{"accomplishments":"work done","beads_closed":["kd-1","kd-2"],"pull_requests":["https://github.com/org/repo/pull/1"]}`

	w, comment, err := processWrapUp(raw)
	if err != nil {
		t.Fatalf("processWrapUp: %v", err)
	}
	if len(w.BeadsClosed) != 2 {
		t.Errorf("BeadsClosed = %v, want 2 entries", w.BeadsClosed)
	}
	if len(w.PullRequests) != 1 {
		t.Errorf("PullRequests = %v, want 1 entry", w.PullRequests)
	}
	if !strings.Contains(comment, "kd-1") {
		t.Errorf("comment missing bead ID: %q", comment)
	}
}

