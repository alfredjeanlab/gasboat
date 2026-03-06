package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"gasboat/controller/internal/beadsapi"
	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"
	"gasboat/controller/internal/subscriber"
)

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

	if spec.Env["JIRA_BASE_URL"] != "https://pihealth.atlassian.net" {
		t.Errorf("expected JIRA_BASE_URL from env vars, got %q", spec.Env["JIRA_BASE_URL"])
	}

	// Secrets are applied by applyCommonConfig, not applyProjectDefaults.
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
		Mode:      "crew",
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
		ProjectCache:             map[string]config.ProjectCacheEntry{},
	}
	spec := &podmanager.AgentPodSpec{
		Project: "myproject",
		Env:     map[string]string{},
	}
	defaults := podmanager.DefaultPodDefaults("crew")
	podmanager.ApplyDefaults(spec, defaults)
	applyCommonConfig(cfg, spec)

	memReq := spec.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "32Gi" {
		t.Errorf("expected memory request 32Gi, got %s", memReq.String())
	}
	cpuReq := spec.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != podmanager.DefaultCPURequest {
		t.Errorf("expected default cpu request %s, got %s", podmanager.DefaultCPURequest, cpuReq.String())
	}
}
