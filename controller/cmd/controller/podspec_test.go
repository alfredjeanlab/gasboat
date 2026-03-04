package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"
	"gasboat/controller/internal/subscriber"
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
		{"crew", "polecat", "crew"}, // explicit mode takes precedence
		{"job", "crew", "job"},      // explicit mode takes precedence
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

func TestApplyCommonConfig_PerProjectSecretOverride(t *testing.T) {
	cfg := &config.Config{
		GithubTokenSecret: "global-gh-token",
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

	// GITHUB_TOKEN should be overridden to project-specific secret.
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

func TestApplyCommonConfig_PerProjectSecretAdditive(t *testing.T) {
	cfg := &config.Config{
		GithubTokenSecret: "global-gh-token",
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				Secrets: []beadsapi.SecretEntry{
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

	// Both GITHUB_TOKEN (global) and JIRA_API_TOKEN (project) should be present.
	envNames := map[string]bool{}
	for _, se := range spec.SecretEnv {
		envNames[se.EnvName] = true
	}
	if !envNames["GITHUB_TOKEN"] {
		t.Error("expected GITHUB_TOKEN from global config")
	}
	if !envNames["JIRA_API_TOKEN"] {
		t.Error("expected JIRA_API_TOKEN from project config")
	}
}

func TestApplyCommonConfig_GitCredentialOverride(t *testing.T) {
	cfg := &config.Config{
		GitCredentialsSecret: "global-git-creds",
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

func TestApplyCommonConfig_NoProjectOverrides(t *testing.T) {
	cfg := &config.Config{
		GithubTokenSecret: "global-gh-token",
		ProjectCache:      map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyCommonConfig(cfg, spec)

	// Should still have the global GITHUB_TOKEN.
	found := false
	for _, se := range spec.SecretEnv {
		if se.EnvName == "GITHUB_TOKEN" {
			found = true
			if se.SecretName != "global-gh-token" {
				t.Errorf("expected global-gh-token, got %s", se.SecretName)
			}
		}
	}
	if !found {
		t.Error("expected GITHUB_TOKEN from global config")
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

	// BOAT_REFERENCE_REPOS should be set.
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
					// Valid: starts with "myproject-"
					{Env: "VALID_TOKEN", Secret: "myproject-creds", Key: "token"},
					// Invalid: starts with "pihealth-" instead of "myproject-"
					{Env: "INVALID_TOKEN", Secret: "pihealth-jira", Key: "api-token"},
					// Invalid: no prefix at all
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

	// Only the valid secret should be present.
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

func TestApplyProjectDefaults_RTKEnabled(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				RTKEnabled: true,
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	if spec.Env["RTK_ENABLED"] != "true" {
		t.Errorf("expected RTK_ENABLED=true, got %q", spec.Env["RTK_ENABLED"])
	}
}

func TestApplyProjectDefaults_RTKDisabledByDefault(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	if _, ok := spec.Env["RTK_ENABLED"]; ok {
		t.Error("expected RTK_ENABLED to not be set when project has RTK disabled")
	}
}

func TestBuildAgentPodSpec_RTKAgentOverrideDisable(t *testing.T) {
	cfg := &config.Config{
		Namespace: "test",
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				RTKEnabled: true,
			},
		},
	}
	event := subscriber.Event{
		Project:   "myproject",
		Role:      "crew",
		AgentName: "agent1",
		Metadata:  map[string]string{"rtk_enabled": "false"},
	}
	spec := buildAgentPodSpec(cfg, event)

	if _, ok := spec.Env["RTK_ENABLED"]; ok {
		t.Error("expected RTK_ENABLED to be removed by agent-level override")
	}
}

func TestBuildAgentPodSpec_RTKAgentOverrideEnable(t *testing.T) {
	cfg := &config.Config{
		Namespace: "test",
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {},
		},
	}
	event := subscriber.Event{
		Project:   "myproject",
		Role:      "crew",
		AgentName: "agent1",
		Metadata:  map[string]string{"rtk_enabled": "true"},
	}
	spec := buildAgentPodSpec(cfg, event)

	if spec.Env["RTK_ENABLED"] != "true" {
		t.Errorf("expected RTK_ENABLED=true from agent override, got %q", spec.Env["RTK_ENABLED"])
	}
}

func TestApplyProjectDefaults_PlainEnvVars(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				EnvVars: []beadsapi.EnvEntry{
					{Name: "JIRA_BASE_URL", Value: "https://pihealth.atlassian.net"},
					{Name: "JIRA_PROJECT", Value: "PIH"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	if spec.Env["JIRA_BASE_URL"] != "https://pihealth.atlassian.net" {
		t.Errorf("expected JIRA_BASE_URL=https://pihealth.atlassian.net, got %q", spec.Env["JIRA_BASE_URL"])
	}
	if spec.Env["JIRA_PROJECT"] != "PIH" {
		t.Errorf("expected JIRA_PROJECT=PIH, got %q", spec.Env["JIRA_PROJECT"])
	}
}

func TestApplyProjectDefaults_PlainEnvVarsDoNotOverrideExisting(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				EnvVars: []beadsapi.EnvEntry{
					{Name: "EXISTING_VAR", Value: "from-project"},
					{Name: "NEW_VAR", Value: "added"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{"EXISTING_VAR": "keep-this"},
	}
	applyProjectDefaults(cfg, spec)

	if spec.Env["EXISTING_VAR"] != "keep-this" {
		t.Errorf("expected EXISTING_VAR=keep-this (not overridden), got %q", spec.Env["EXISTING_VAR"])
	}
	if spec.Env["NEW_VAR"] != "added" {
		t.Errorf("expected NEW_VAR=added, got %q", spec.Env["NEW_VAR"])
	}
}

func TestApplyProjectDefaults_PlainEnvVarsNoProject(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "nonexistent",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	// No env vars should be added when project doesn't exist.
	if len(spec.Env) != 0 {
		t.Errorf("expected no env vars, got %d", len(spec.Env))
	}
}

func TestApplyProjectDefaults_PlainEnvVarsAndSecretsTogether(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"myproject": {
				EnvVars: []beadsapi.EnvEntry{
					{Name: "JIRA_BASE_URL", Value: "https://pihealth.atlassian.net"},
				},
				Secrets: []beadsapi.SecretEntry{
					{Env: "JIRA_API_TOKEN", Secret: "myproject-jira", Key: "api-token"},
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	applyProjectDefaults(cfg, spec)

	// Plain env var should be set.
	if spec.Env["JIRA_BASE_URL"] != "https://pihealth.atlassian.net" {
		t.Errorf("expected JIRA_BASE_URL from env vars, got %q", spec.Env["JIRA_BASE_URL"])
	}

	// Secrets are applied by applyCommonConfig, not applyProjectDefaults,
	// so SecretEnv should be empty here.
	if len(spec.SecretEnv) != 0 {
		t.Errorf("expected no SecretEnv from applyProjectDefaults, got %d", len(spec.SecretEnv))
	}
}

func TestBuildSpecFromBeadInfo_IncludesProjectEnvVars(t *testing.T) {
	cfg := &config.Config{
		Namespace: "test",
		CoopImage: "agent:latest",
		ProjectCache: map[string]config.ProjectCacheEntry{
			"monorepo": {
				EnvVars: []beadsapi.EnvEntry{
					{Name: "JIRA_BASE_URL", Value: "https://pihealth.atlassian.net"},
					{Name: "JIRA_PROJECT", Value: "PIH"},
				},
			},
		},
	}
	spec := BuildSpecFromBeadInfo(cfg, "monorepo", "crew", "crew", "agent1", map[string]string{})

	if spec.Env["JIRA_BASE_URL"] != "https://pihealth.atlassian.net" {
		t.Errorf("expected JIRA_BASE_URL from project env vars, got %q", spec.Env["JIRA_BASE_URL"])
	}
	if spec.Env["JIRA_PROJECT"] != "PIH" {
		t.Errorf("expected JIRA_PROJECT from project env vars, got %q", spec.Env["JIRA_PROJECT"])
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

func TestApplyProjectDefaults_ResourceOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"heavy": {
				CPURequest:    "4",
				CPULimit:      "8",
				MemoryRequest: "4Gi",
				MemoryLimit:   "16Gi",
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "heavy",
		Env:     map[string]string{},
	}
	// Apply mode defaults first (sets default Resources).
	defaults := podmanager.DefaultPodDefaults("crew")
	podmanager.ApplyDefaults(spec, defaults)
	applyProjectDefaults(cfg, spec)

	if spec.Resources == nil {
		t.Fatal("expected Resources to be set")
	}
	cpuReq := spec.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "4" {
		t.Errorf("expected cpu request 4, got %s", cpuReq.String())
	}
	cpuLim := spec.Resources.Limits[corev1.ResourceCPU]
	if cpuLim.String() != "8" {
		t.Errorf("expected cpu limit 8, got %s", cpuLim.String())
	}
	memReq := spec.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "4Gi" {
		t.Errorf("expected memory request 4Gi, got %s", memReq.String())
	}
	memLim := spec.Resources.Limits[corev1.ResourceMemory]
	if memLim.String() != "16Gi" {
		t.Errorf("expected memory limit 16Gi, got %s", memLim.String())
	}
}

func TestApplyProjectDefaults_PartialResourceOverride(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"partial": {
				CPURequest: "500m",
				// Leave other resource fields empty — defaults should be preserved.
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "partial",
		Env:     map[string]string{},
	}
	defaults := podmanager.DefaultPodDefaults("crew")
	podmanager.ApplyDefaults(spec, defaults)
	applyProjectDefaults(cfg, spec)

	cpuReq := spec.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "500m" {
		t.Errorf("expected cpu request 500m, got %s", cpuReq.String())
	}
	// Memory request should still be the default.
	memReq := spec.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != podmanager.DefaultMemoryRequest {
		t.Errorf("expected default memory request %s, got %s", podmanager.DefaultMemoryRequest, memReq.String())
	}
}

func TestApplyProjectDefaults_EnvOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"envtest": {
				EnvOverrides: map[string]string{
					"FEATURE_FLAG": "true",
					"API_URL":      "https://api.example.com",
				},
			},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "envtest",
		Env:     map[string]string{"EXISTING": "keep"},
	}
	applyProjectDefaults(cfg, spec)

	if spec.Env["FEATURE_FLAG"] != "true" {
		t.Errorf("expected FEATURE_FLAG=true, got %s", spec.Env["FEATURE_FLAG"])
	}
	if spec.Env["API_URL"] != "https://api.example.com" {
		t.Errorf("expected API_URL, got %s", spec.Env["API_URL"])
	}
	if spec.Env["EXISTING"] != "keep" {
		t.Errorf("expected existing env to be preserved, got %s", spec.Env["EXISTING"])
	}
}

func TestBuildAgentPodSpec_SlackThreadMetadata(t *testing.T) {
	cfg := &config.Config{
		Namespace:    "test",
		ProjectCache: map[string]config.ProjectCacheEntry{},
	}
	event := subscriber.Event{
		Project:   "gasboat",
		Role:      "crew",
		AgentName: "thread-agent",
		Metadata: map[string]string{
			"slack_thread_channel": "C-support",
			"slack_thread_ts":     "1234.5678",
		},
	}
	spec := buildAgentPodSpec(cfg, event)

	if spec.Env["SLACK_THREAD_CHANNEL"] != "C-support" {
		t.Errorf("expected SLACK_THREAD_CHANNEL=C-support, got %q", spec.Env["SLACK_THREAD_CHANNEL"])
	}
	if spec.Env["SLACK_THREAD_TS"] != "1234.5678" {
		t.Errorf("expected SLACK_THREAD_TS=1234.5678, got %q", spec.Env["SLACK_THREAD_TS"])
	}
}

func TestBuildAgentPodSpec_SlackThreadMetadataAbsent(t *testing.T) {
	cfg := &config.Config{
		Namespace:    "test",
		ProjectCache: map[string]config.ProjectCacheEntry{},
	}
	event := subscriber.Event{
		Project:   "gasboat",
		Role:      "crew",
		AgentName: "normal-agent",
		Metadata:  map[string]string{},
	}
	spec := buildAgentPodSpec(cfg, event)

	if _, ok := spec.Env["SLACK_THREAD_CHANNEL"]; ok {
		t.Error("expected SLACK_THREAD_CHANNEL to not be set for non-thread agent")
	}
	if _, ok := spec.Env["SLACK_THREAD_TS"]; ok {
		t.Error("expected SLACK_THREAD_TS to not be set for non-thread agent")
	}
}

func TestBuildAgentPodSpec_SpawnSourceSlackThread_DefaultsToJob(t *testing.T) {
	cfg := &config.Config{
		Namespace:    "test",
		ProjectCache: map[string]config.ProjectCacheEntry{},
	}
	event := subscriber.Event{
		Project:   "gasboat",
		Role:      "crew",
		AgentName: "thread-agent",
		// Mode is empty — should default to "job" for slack-thread spawn source.
		Metadata: map[string]string{
			"spawn_source":        "slack-thread",
			"slack_thread_channel": "C-test",
			"slack_thread_ts":     "1.1",
		},
	}
	spec := buildAgentPodSpec(cfg, event)

	if spec.Mode != "job" {
		t.Errorf("expected mode=job for slack-thread spawn, got %q", spec.Mode)
	}
}

func TestBuildAgentPodSpec_SpawnSourceSlackThread_ExplicitModePreserved(t *testing.T) {
	cfg := &config.Config{
		Namespace:    "test",
		ProjectCache: map[string]config.ProjectCacheEntry{},
	}
	event := subscriber.Event{
		Project:   "gasboat",
		Mode:      "crew", // Explicit mode set — should NOT be overridden.
		Role:      "crew",
		AgentName: "thread-agent",
		Metadata: map[string]string{
			"spawn_source": "slack-thread",
		},
	}
	spec := buildAgentPodSpec(cfg, event)

	if spec.Mode != "crew" {
		t.Errorf("expected explicit mode=crew to be preserved, got %q", spec.Mode)
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
		Namespace: "test",
		CoopImage: "agent:latest",
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

func TestApplyProjectDefaults_NoOverrides(t *testing.T) {
	cfg := &config.Config{
		ProjectCache: map[string]config.ProjectCacheEntry{
			"plain": {},
		},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "plain",
		Env:     map[string]string{},
	}
	defaults := podmanager.DefaultPodDefaults("crew")
	podmanager.ApplyDefaults(spec, defaults)
	origResources := spec.Resources
	applyProjectDefaults(cfg, spec)

	// Resources should remain unchanged when no overrides are set.
	if spec.Resources != origResources {
		t.Error("expected resources to remain unchanged")
	}
}

func TestApplyCommonConfig_TeamsResourceOverrides(t *testing.T) {
	cfg := &config.Config{
		ClaudeTeamsEnabled:       true,
		ClaudeTeamsCPURequest:    "4",
		ClaudeTeamsCPULimit:      "8",
		ClaudeTeamsMemoryRequest: "24Gi",
		ClaudeTeamsMemoryLimit:   "24Gi",
		ProjectCache:             map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	defaults := podmanager.DefaultPodDefaults("crew")
	podmanager.ApplyDefaults(spec, defaults)
	applyCommonConfig(cfg, spec)

	if spec.Resources == nil {
		t.Fatal("expected Resources to be set")
	}
	cpuReq := spec.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "4" {
		t.Errorf("expected cpu request 4, got %s", cpuReq.String())
	}
	cpuLim := spec.Resources.Limits[corev1.ResourceCPU]
	if cpuLim.String() != "8" {
		t.Errorf("expected cpu limit 8, got %s", cpuLim.String())
	}
	memReq := spec.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "24Gi" {
		t.Errorf("expected memory request 24Gi, got %s", memReq.String())
	}
	memLim := spec.Resources.Limits[corev1.ResourceMemory]
	if memLim.String() != "24Gi" {
		t.Errorf("expected memory limit 24Gi, got %s", memLim.String())
	}
}

func TestApplyCommonConfig_TeamsDisabled_NoResourceOverride(t *testing.T) {
	cfg := &config.Config{
		ClaudeTeamsEnabled:       false,
		ClaudeTeamsCPURequest:    "4",
		ClaudeTeamsMemoryRequest: "24Gi",
		ProjectCache:             map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	defaults := podmanager.DefaultPodDefaults("crew")
	podmanager.ApplyDefaults(spec, defaults)
	applyCommonConfig(cfg, spec)

	// Resources should remain at defaults when teams is disabled.
	cpuReq := spec.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != podmanager.DefaultCPURequest {
		t.Errorf("expected default cpu request %s, got %s", podmanager.DefaultCPURequest, cpuReq.String())
	}
	memReq := spec.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != podmanager.DefaultMemoryRequest {
		t.Errorf("expected default memory request %s, got %s", podmanager.DefaultMemoryRequest, memReq.String())
	}
}

func TestApplyCommonConfig_TeamsPartialResourceOverride(t *testing.T) {
	cfg := &config.Config{
		ClaudeTeamsEnabled:       true,
		ClaudeTeamsMemoryRequest: "32Gi",
		ClaudeTeamsMemoryLimit:   "32Gi",
		// CPU fields empty — should keep defaults.
		ProjectCache: map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	defaults := podmanager.DefaultPodDefaults("crew")
	podmanager.ApplyDefaults(spec, defaults)
	applyCommonConfig(cfg, spec)

	// Memory should be overridden.
	memReq := spec.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "32Gi" {
		t.Errorf("expected memory request 32Gi, got %s", memReq.String())
	}
	// CPU should remain at default.
	cpuReq := spec.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != podmanager.DefaultCPURequest {
		t.Errorf("expected default cpu request %s, got %s", podmanager.DefaultCPURequest, cpuReq.String())
	}
}
