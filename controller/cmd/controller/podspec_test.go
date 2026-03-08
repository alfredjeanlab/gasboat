package main

import (
	"testing"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"
)

func TestModeForRole(t *testing.T) {
	tests := []struct {
		mode string
		role string
		want string
	}{
		{"", "captain", "crew"},
		{"", "crew", "crew"},
		{"", "job", "job"},
		{"", "polecat", "job"},
		{"", "unknown", "crew"},
		{"crew", "polecat", "crew"},       // explicit mode takes precedence
		{"job", "crew", "job"},            // explicit mode takes precedence
		{"", "thread,crew", "crew"},       // multi-role: first role determines mode
		{"", "job,crew", "job"},           // multi-role: first role is job
		{"", "captain,thread", "crew"},    // multi-role: first role is captain
		{"crew", "job,polecat", "crew"},   // explicit mode takes precedence over multi-role
	}
	for _, tc := range tests {
		got := modeForRole(tc.mode, tc.role)
		if got != tc.want {
			t.Errorf("modeForRole(%q, %q) = %q, want %q", tc.mode, tc.role, got, tc.want)
		}
	}
}

func TestOverrideOrAppendSecretEnv_OverridesExisting(t *testing.T) {
	envs := []podmanager.SecretEnvSource{
		{EnvName: "GITHUB_TOKEN", SecretName: "global-gh", SecretKey: "token"},
		{EnvName: "OTHER_SECRET", SecretName: "other", SecretKey: "key"},
	}
	src := podmanager.SecretEnvSource{
		EnvName: "GITHUB_TOKEN", SecretName: "project-gh", SecretKey: "my-token",
	}
	overrideOrAppendSecretEnv(&envs, src)

	if len(envs) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(envs))
	}
	if envs[0].SecretName != "project-gh" {
		t.Errorf("expected SecretName project-gh, got %s", envs[0].SecretName)
	}
	if envs[0].SecretKey != "my-token" {
		t.Errorf("expected SecretKey my-token, got %s", envs[0].SecretKey)
	}
}

func TestOverrideOrAppendSecretEnv_AppendsNew(t *testing.T) {
	envs := []podmanager.SecretEnvSource{
		{EnvName: "GITHUB_TOKEN", SecretName: "global-gh", SecretKey: "token"},
	}
	src := podmanager.SecretEnvSource{
		EnvName: "JIRA_API_TOKEN", SecretName: "proj-jira", SecretKey: "api-token",
	}
	overrideOrAppendSecretEnv(&envs, src)

	if len(envs) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(envs))
	}
	if envs[1].EnvName != "JIRA_API_TOKEN" {
		t.Errorf("expected JIRA_API_TOKEN, got %s", envs[1].EnvName)
	}
}

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/org/my-repo.git", "my-repo"},
		{"https://github.com/org/my-repo", "my-repo"},
		{"https://gitlab.com/PiHealth/CoreFICS/monorepo", "monorepo"},
		{"https://gitlab.com/PiHealth/CoreFICS/monorepo.git", "monorepo"},
		{"repo", "repo"},
	}
	for _, tc := range tests {
		got := repoNameFromURL(tc.url)
		if got != tc.want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestApplyCommonConfig_PerProjectSecrets(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Secrets: []beadsapi.SecretEntry{
					{Env: "GITHUB_TOKEN", Secret: "myproject-gh-token", Key: "my-token"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	found := false
	for _, se := range spec.SecretEnv {
		if se.EnvName == "GITHUB_TOKEN" {
			found = true
			if se.SecretName != "myproject-gh-token" {
				t.Errorf("expected SecretName myproject-gh-token, got %s", se.SecretName)
			}
			if se.SecretKey != "my-token" {
				t.Errorf("expected SecretKey my-token, got %s", se.SecretKey)
			}
		}
	}
	if !found {
		t.Error("GITHUB_TOKEN not found in SecretEnv")
	}
}

func TestApplyCommonConfig_PerProjectMultipleSecrets(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Secrets: []beadsapi.SecretEntry{
					{Env: "GITHUB_TOKEN", Secret: "myproject-gh-token", Key: "token"},
					{Env: "JIRA_API_TOKEN", Secret: "myproject-jira", Key: "api-token"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	envNames := map[string]bool{}
	for _, se := range spec.SecretEnv {
		envNames[se.EnvName] = true
	}
	if !envNames["GITHUB_TOKEN"] {
		t.Error("expected GITHUB_TOKEN from project config")
	}
	if !envNames["JIRA_API_TOKEN"] {
		t.Error("expected JIRA_API_TOKEN from project config")
	}
}

func TestApplyCommonConfig_GitCredentialFromProjectBead(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Secrets: []beadsapi.SecretEntry{
					{Env: "GIT_TOKEN", Secret: "myproject-git-creds", Key: "token"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.GitCredentialsSecret != "myproject-git-creds" {
		t.Errorf("expected GitCredentialsSecret myproject-git-creds, got %s", spec.GitCredentialsSecret)
	}
}

func TestApplyCommonConfig_NoProjectSecrets(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if len(spec.SecretEnv) != 0 {
		t.Errorf("expected 0 SecretEnv, got %d", len(spec.SecretEnv))
	}
}

func TestApplyCommonConfig_MultiRepo(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Repos: []beadsapi.RepoEntry{
					{URL: "https://github.com/org/main-repo.git", Branch: "develop", Role: "primary"},
					{URL: "https://github.com/org/shared-lib.git", Role: "reference", Name: "shared-lib"},
					{URL: "https://github.com/org/other.git", Role: "reference"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.GitURL != "https://github.com/org/main-repo.git" {
		t.Errorf("expected primary GitURL, got %s", spec.GitURL)
	}
	if spec.GitDefaultBranch != "develop" {
		t.Errorf("expected develop branch, got %s", spec.GitDefaultBranch)
	}
	if len(spec.ReferenceRepos) != 2 {
		t.Fatalf("expected 2 reference repos, got %d", len(spec.ReferenceRepos))
	}
	if spec.ReferenceRepos[0].Name != "shared-lib" {
		t.Errorf("expected shared-lib, got %s", spec.ReferenceRepos[0].Name)
	}
	if spec.ReferenceRepos[1].Name != "other" {
		t.Errorf("expected other (derived from URL), got %s", spec.ReferenceRepos[1].Name)
	}

	refRepos := spec.Env["BOAT_REFERENCE_REPOS"]
	if refRepos == "" {
		t.Fatal("expected BOAT_REFERENCE_REPOS to be set")
	}
}

func TestApplyCommonConfig_LegacySingleRepo(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				GitURL:        "https://github.com/org/legacy.git",
				DefaultBranch: "master",
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.GitURL != "https://github.com/org/legacy.git" {
		t.Errorf("expected legacy GitURL, got %s", spec.GitURL)
	}
	if spec.GitDefaultBranch != "master" {
		t.Errorf("expected master branch, got %s", spec.GitDefaultBranch)
	}
	if len(spec.ReferenceRepos) != 0 {
		t.Errorf("expected no reference repos, got %d", len(spec.ReferenceRepos))
	}
}

func TestApplyCommonConfig_RejectsSecretWithWrongPrefix(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Secrets: []beadsapi.SecretEntry{
					{Env: "VALID_TOKEN", Secret: "myproject-creds", Key: "token"},
					{Env: "INVALID_TOKEN", Secret: "pihealth-jira", Key: "api-token"},
					{Env: "BAD_SECRET", Secret: "shared-secret", Key: "key"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	envNames := map[string]bool{}
	for _, se := range spec.SecretEnv {
		envNames[se.EnvName] = true
	}
	if !envNames["VALID_TOKEN"] {
		t.Error("expected VALID_TOKEN to be present (valid prefix)")
	}
	if envNames["INVALID_TOKEN"] {
		t.Error("expected INVALID_TOKEN to be skipped (wrong prefix)")
	}
	if envNames["BAD_SECRET"] {
		t.Error("expected BAD_SECRET to be skipped (wrong prefix)")
	}
}

func TestApplyCommonConfig_ReferenceOnlyRepos(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Repos: []beadsapi.RepoEntry{
					{URL: "https://github.com/org/ref1.git", Role: "reference", Name: "ref1"},
					{URL: "https://github.com/org/ref2.git", Role: "reference", Name: "ref2"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.GitURL != "" {
		t.Errorf("expected empty GitURL, got %s", spec.GitURL)
	}
	if len(spec.ReferenceRepos) != 2 {
		t.Fatalf("expected 2 reference repos, got %d", len(spec.ReferenceRepos))
	}
}

func TestApplyCommonConfig_SlackBridgeURL(t *testing.T) {
	cfg := &config.Config{
		SlackBridgeURL: "http://gasboat-slack-bridge:8090",
		ProjectCache:   map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	if spec.Env["SLACK_BRIDGE_URL"] != "http://gasboat-slack-bridge:8090" {
		t.Errorf("expected SLACK_BRIDGE_URL, got %q", spec.Env["SLACK_BRIDGE_URL"])
	}
}

func TestBuildSpecFromBeadInfo_SlackThreadMetadata(t *testing.T) {
	cfg := &config.Config{
		Namespace:    "test",
		CoopImage:    "agent:latest",
		ProjectCache: map[string]config.ProjectCacheEntry{},
	}
	metadata := map[string]string{
		"slack_thread_channel": "C-help",
		"slack_thread_ts":     "9999.0001",
	}
	spec := BuildSpecFromBeadInfo(cfg, "gasboat", "crew", "crew", "agent1", metadata)

	if spec.Env["SLACK_THREAD_CHANNEL"] != "C-help" {
		t.Errorf("expected SLACK_THREAD_CHANNEL=C-help, got %q", spec.Env["SLACK_THREAD_CHANNEL"])
	}
	if spec.Env["SLACK_THREAD_TS"] != "9999.0001" {
		t.Errorf("expected SLACK_THREAD_TS=9999.0001, got %q", spec.Env["SLACK_THREAD_TS"])
	}
}
