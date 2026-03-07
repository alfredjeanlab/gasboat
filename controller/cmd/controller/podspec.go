package main

import (
	"fmt"
	"log/slog"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"gasboat/controller/internal/config"
	"gasboat/controller/internal/podmanager"
	"gasboat/controller/internal/subscriber"
)

// modeForRole returns the canonical mode for a role.
// If mode is already set, it is returned unchanged.
func modeForRole(mode, role string) string {
	if mode != "" {
		return mode
	}
	switch role {
	case "captain", "crew":
		return "crew"
	case "job", "polecat":
		return "job"
	default:
		return "crew"
	}
}

// BuildSpecFromBeadInfo constructs an AgentPodSpec from config and bead identity,
// Used by the reconciler to produce specs identical to those created by
// handleEvent, using controller config for all metadata.
func BuildSpecFromBeadInfo(cfg *config.Config, project, mode, role, agentName string, metadata map[string]string) podmanager.AgentPodSpec {
	mode = modeForRole(mode, role)
	image := cfg.CoopImage
	if img := metadata["image"]; img != "" {
		image = img
	}
	spec := podmanager.AgentPodSpec{
		Project:   project,
		Mode:      mode,
		Role:      role,
		AgentName: agentName,
		TaskID:    metadata["task_id"],
		Image:     image,
		Namespace: cfg.Namespace,
		Env: map[string]string{
			"BEADS_GRPC_ADDR": cfg.BeadsGRPCAddr,
			"BEADS_HTTP_ADDR": cfg.BeadsHTTPAddr,
		},
	}

	defaults := podmanager.DefaultPodDefaults(mode)
	podmanager.ApplyDefaults(&spec, defaults)

	// Apply project-level overrides from project bead metadata.
	// Hold read lock on ProjectCache to prevent concurrent map read/write
	// with refreshProjectCache running in the periodic sync goroutine.
	cfg.ProjectCacheMu.RLock()
	applyProjectDefaults(cfg, &spec)
	applyCommonConfig(cfg, &spec)
	cfg.ProjectCacheMu.RUnlock()

	// Agent-level RTK override.
	if metadata["rtk_enabled"] == "false" {
		delete(spec.Env, "RTK_ENABLED")
	} else if metadata["rtk_enabled"] == "true" {
		spec.Env["RTK_ENABLED"] = "true"
	}

	// Mock mode: override BOAT_COMMAND to run claudeless with a scenario file.
	if scenario := metadata["mock_scenario"]; scenario != "" {
		spec.Env["BOAT_COMMAND"] = fmt.Sprintf("claudeless --scenario /scenarios/%s.toml --dangerously-skip-permissions", scenario)
	}

	// Custom prompt: pass through to entrypoint for initial nudge message.
	if prompt := metadata["prompt"]; prompt != "" {
		spec.Env["BOAT_PROMPT"] = prompt
	}

	// Slack thread metadata.
	if ch := metadata["slack_thread_channel"]; ch != "" {
		spec.Env["SLACK_THREAD_CHANNEL"] = ch
	}
	if ts := metadata["slack_thread_ts"]; ts != "" {
		spec.Env["SLACK_THREAD_TS"] = ts
	}

	// Prewarmed agents: block in entrypoint standby mode (BOAT_STANDBY=true)
	// until the pool manager assigns work by transitioning agent_state from
	// "prewarmed" to "assigning". The entrypoint polls agent_state and starts
	// Claude only after assignment, preventing stampede from self-selection.
	if metadata["agent_state"] == "prewarmed" {
		spec.Env["BOAT_AGENT_STATE"] = "prewarmed"
		spec.Env["BOAT_STANDBY"] = "true"
		spec.Prewarmed = true
	}

	return spec
}

// buildAgentPodSpec constructs a full AgentPodSpec from an event and config.
// It applies role-specific defaults, then overlays event metadata.
func buildAgentPodSpec(cfg *config.Config, event subscriber.Event) podmanager.AgentPodSpec {
	ns := namespaceFromEvent(event, cfg.Namespace)
	mode := modeForRole(event.Mode, event.Role)

	// Thread-spawned agents default to job mode (no PVC, restartPolicy: Never)
	// unless the bead explicitly sets a different mode.
	if event.Metadata["spawn_source"] == "slack-thread" && event.Mode == "" {
		mode = "job"
	}

	spec := podmanager.AgentPodSpec{
		Project:   event.Project,
		Mode:      mode,
		Role:      event.Role,
		AgentName: event.AgentName,
		BeadID:    event.BeadID,
		TaskID:    event.Metadata["task_id"],
		Image:     event.Metadata["image"],
		Namespace: ns,
		Env: map[string]string{
			"BEADS_GRPC_ADDR": metadataOr(event, "beads_grpc_addr", cfg.BeadsGRPCAddr),
			"BEADS_HTTP_ADDR": cfg.BeadsHTTPAddr,
		},
	}

	// Apply mode-specific defaults (workspace storage, resources).
	defaults := podmanager.DefaultPodDefaults(mode)
	podmanager.ApplyDefaults(&spec, defaults)

	// Apply project-level overrides and common config under read lock
	// to prevent concurrent map read/write with refreshProjectCache.
	cfg.ProjectCacheMu.RLock()
	applyProjectDefaults(cfg, &spec)
	applyCommonConfig(cfg, &spec)
	cfg.ProjectCacheMu.RUnlock()

	// Overlay event metadata for optional fields.
	if sa := event.Metadata["service_account"]; sa != "" {
		spec.ServiceAccountName = sa
	}
	if cm := event.Metadata["configmap"]; cm != "" {
		spec.ConfigMapName = cm
	}

	// Agent-level RTK override: force-disable RTK for this agent even if the
	// project has it enabled. Set rtk_enabled=false on the agent bead to opt out.
	if event.Metadata["rtk_enabled"] == "false" {
		delete(spec.Env, "RTK_ENABLED")
	} else if event.Metadata["rtk_enabled"] == "true" {
		spec.Env["RTK_ENABLED"] = "true"
	}

	// Mock mode: override BOAT_COMMAND to run claudeless with a scenario file.
	if scenario := event.Metadata["mock_scenario"]; scenario != "" {
		spec.Env["BOAT_COMMAND"] = fmt.Sprintf("claudeless --scenario /scenarios/%s.toml --dangerously-skip-permissions", scenario)
	}

	// Custom prompt: pass through to entrypoint for initial nudge message.
	if prompt := event.Metadata["prompt"]; prompt != "" {
		spec.Env["BOAT_PROMPT"] = prompt
	}

	// Slack thread metadata: inject thread coordinates so agents spawned
	// from a Slack thread can read/reply via the slack-bridge HTTP API.
	if ch := event.Metadata["slack_thread_channel"]; ch != "" {
		spec.Env["SLACK_THREAD_CHANNEL"] = ch
	}
	if ts := event.Metadata["slack_thread_ts"]; ts != "" {
		spec.Env["SLACK_THREAD_TS"] = ts
	}

	// Prewarmed agents: block in entrypoint standby mode (BOAT_STANDBY=true)
	// until the pool manager assigns work by transitioning agent_state from
	// "prewarmed" to "assigning". The entrypoint polls agent_state and starts
	// Claude only after assignment, preventing stampede from self-selection.
	if event.Metadata["agent_state"] == "prewarmed" {
		spec.Env["BOAT_AGENT_STATE"] = "prewarmed"
		spec.Env["BOAT_STANDBY"] = "true"
		spec.Prewarmed = true
	}

	return spec
}

// applyProjectDefaults applies per-project overrides from project bead metadata.
// Applied after mode defaults, before controller common config.
func applyProjectDefaults(cfg *config.Config, spec *podmanager.AgentPodSpec) {
	entry, ok := cfg.ProjectCache[spec.Project]
	if !ok {
		return
	}
	if entry.Image != "" {
		spec.Image = entry.Image
	}
	if entry.StorageClass != "" && spec.WorkspaceStorage != nil {
		spec.WorkspaceStorage.StorageClassName = entry.StorageClass
	}
	if entry.ServiceAccount != "" {
		spec.ServiceAccountName = entry.ServiceAccount
	}

	// Apply per-project resource overrides. Individual fields override the
	// corresponding resource value while preserving other defaults.
	if entry.CPURequest != "" || entry.CPULimit != "" || entry.MemoryRequest != "" || entry.MemoryLimit != "" {
		if spec.Resources == nil {
			spec.Resources = &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{},
				Limits:   corev1.ResourceList{},
			}
		}
		applyResourceOverride(spec.Resources.Requests, corev1.ResourceCPU, entry.CPURequest)
		applyResourceOverride(spec.Resources.Limits, corev1.ResourceCPU, entry.CPULimit)
		applyResourceOverride(spec.Resources.Requests, corev1.ResourceMemory, entry.MemoryRequest)
		applyResourceOverride(spec.Resources.Limits, corev1.ResourceMemory, entry.MemoryLimit)
	}

	// RTK: set env var if project has RTK enabled
	if entry.RTKEnabled {
		if spec.Env == nil {
			spec.Env = make(map[string]string)
		}
		spec.Env["RTK_ENABLED"] = "true"
	}

	// Apply per-project env overrides (additive; project values take precedence).
	for k, v := range entry.EnvOverrides {
		if spec.Env == nil {
			spec.Env = make(map[string]string)
		}
		spec.Env[k] = v
	}

	// Inject per-project plain env vars. Project values do not override
	// env vars that are already set (e.g., from controller config).
	for _, ev := range entry.EnvVars {
		if _, exists := spec.Env[ev.Name]; !exists {
			spec.Env[ev.Name] = ev.Value
		}
	}
}

// applyResourceOverride sets a resource quantity in a ResourceList if the value
// is non-empty and parses as a valid Kubernetes quantity.
func applyResourceOverride(list corev1.ResourceList, name corev1.ResourceName, value string) {
	if value == "" {
		return
	}
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return // silently skip invalid values
	}
	list[name] = q
}

// applyCommonConfig wires controller-level config into an AgentPodSpec.
// Shared by both BuildSpecFromBeadInfo (reconciler) and buildAgentPodSpec (events).
func applyCommonConfig(cfg *config.Config, spec *podmanager.AgentPodSpec) {
	if spec.ServiceAccountName == "" && cfg.CoopServiceAccount != "" {
		spec.ServiceAccountName = cfg.CoopServiceAccount
	}
	if cfg.ClaudeOAuthSecret != "" {
		spec.CredentialsSecret = cfg.ClaudeOAuthSecret
	}
	if cfg.BeadsTokenSecret != "" {
		spec.DaemonTokenSecret = cfg.BeadsTokenSecret
	}

	// Wire git info from project cache (multi-repo aware).
	if entry, ok := cfg.ProjectCache[spec.Project]; ok {
		if len(entry.Repos) > 0 {
			for _, r := range entry.Repos {
				if r.Role == "primary" {
					spec.GitURL = r.URL
					if r.Branch != "" {
						spec.GitDefaultBranch = r.Branch
					}
				} else {
					name := r.Name
					if name == "" {
						name = repoNameFromURL(r.URL)
					}
					branch := r.Branch
					spec.ReferenceRepos = append(spec.ReferenceRepos, podmanager.RepoRef{
						URL: r.URL, Branch: branch, Name: name,
					})
				}
			}
		} else {
			// Legacy single-repo fallback.
			if entry.GitURL != "" {
				spec.GitURL = entry.GitURL
			}
			if entry.DefaultBranch != "" {
				spec.GitDefaultBranch = entry.DefaultBranch
			}
		}
	}

	// Build BOAT_REFERENCE_REPOS env var for the entrypoint (fallback cloning).
	if len(spec.ReferenceRepos) > 0 {
		var entries []string
		for _, r := range spec.ReferenceRepos {
			b := r.Branch
			if b == "" {
				b = "main"
			}
			entries = append(entries, fmt.Sprintf("%s=%s:%s", r.Name, r.URL, b))
		}
		spec.Env["BOAT_REFERENCE_REPOS"] = strings.Join(entries, ",")
	}

	// Build BOAT_PROJECTS env var from project cache for entrypoint project registration.
	if len(cfg.ProjectCache) > 0 {
		var projectEntries []string
		for name, entry := range cfg.ProjectCache {
			if entry.GitURL != "" && entry.Prefix != "" {
				projectEntries = append(projectEntries, fmt.Sprintf("%s=%s:%s", name, entry.GitURL, entry.Prefix))
			}
		}
		if len(projectEntries) > 0 {
			spec.Env["BOAT_PROJECTS"] = strings.Join(projectEntries, ",")
		}
	}

	// Wire NATS config to all agents for beads decisions, coop events, and bus emit.
	if cfg.NatsURL != "" {
		spec.Env["BEADS_NATS_URL"] = cfg.NatsURL
		spec.Env["COOP_NATS_URL"] = cfg.NatsURL
	}
	if cfg.NatsTokenSecret != "" {
		spec.SecretEnv = append(spec.SecretEnv, podmanager.SecretEnvSource{
			EnvName:    "COOP_NATS_TOKEN",
			SecretName: cfg.NatsTokenSecret,
			SecretKey:  "token",
		})
	}

	// Default storage class for agent workspace PVCs. Applied only if no project
	// bead override already set it, so project-level config takes precedence.
	if cfg.AgentStorageClass != "" && spec.WorkspaceStorage != nil && spec.WorkspaceStorage.StorageClassName == "" {
		spec.WorkspaceStorage.StorageClassName = cfg.AgentStorageClass
	}

	// Default Claude model for agent pods (e.g., "claude-opus-4-6").
	if cfg.ClaudeModel != "" {
		spec.Env["CLAUDE_MODEL"] = cfg.ClaudeModel
	}

	// Claude Agent Teams: enable team lead → teammate coordination.
	if cfg.ClaudeTeamsEnabled {
		spec.Env["CLAUDE_TEAMS_ENABLED"] = "true"

		// Apply teams-mode resource overrides. Each teammate runs its own
		// Claude Code session (Node.js process + context window), so pods
		// need more memory and CPU than single-session mode.
		if cfg.ClaudeTeamsCPURequest != "" || cfg.ClaudeTeamsCPULimit != "" ||
			cfg.ClaudeTeamsMemoryRequest != "" || cfg.ClaudeTeamsMemoryLimit != "" {
			if spec.Resources == nil {
				spec.Resources = &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{},
					Limits:   corev1.ResourceList{},
				}
			}
			applyResourceOverride(spec.Resources.Requests, corev1.ResourceCPU, cfg.ClaudeTeamsCPURequest)
			applyResourceOverride(spec.Resources.Limits, corev1.ResourceCPU, cfg.ClaudeTeamsCPULimit)
			applyResourceOverride(spec.Resources.Requests, corev1.ResourceMemory, cfg.ClaudeTeamsMemoryRequest)
			applyResourceOverride(spec.Resources.Limits, corev1.ResourceMemory, cfg.ClaudeTeamsMemoryLimit)
		}
	}
	if cfg.ClaudeTeammateMode != "" {
		spec.Env["CLAUDE_TEAMMATE_MODE"] = cfg.ClaudeTeammateMode
	}
	if cfg.ClaudeTeamsMaxTeammates > 0 {
		spec.Env["CLAUDE_TEAMS_MAX_TEAMMATES"] = fmt.Sprintf("%d", cfg.ClaudeTeamsMaxTeammates)
	}

	// E2E beads address: isolated beads instance for e2e tests so spawn
	// events don't hit the production agents controller.
	if cfg.BeadsE2EHTTPAddr != "" {
		spec.Env["BEADS_E2E_HTTP_ADDR"] = cfg.BeadsE2EHTTPAddr
	}

	// Wire coopmux registration config. The agent runs coop directly (builtin)
	// so it gets COOP_BROKER_URL/TOKEN as env vars.
	if cfg.CoopmuxURL != "" {
		spec.Env["COOP_BROKER_URL"] = cfg.CoopmuxURL
		spec.Env["COOP_MUX_URL"] = cfg.CoopmuxURL
	}
	if cfg.CoopmuxTokenSecret != "" {
		spec.SecretEnv = append(spec.SecretEnv, podmanager.SecretEnvSource{
			EnvName:    "COOP_BROKER_TOKEN",
			SecretName: cfg.CoopmuxTokenSecret,
			SecretKey:  "token",
		})
		if cfg.CoopmuxURL != "" {
			spec.SecretEnv = append(spec.SecretEnv, podmanager.SecretEnvSource{
				EnvName:    "COOP_MUX_TOKEN",
				SecretName: cfg.CoopmuxTokenSecret,
				SecretKey:  "token",
			})
		}
	}

	// Slack bridge URL for agent gb slack commands.
	if cfg.SlackBridgeURL != "" {
		spec.Env["SLACK_BRIDGE_URL"] = cfg.SlackBridgeURL
	}

	// Per-project secrets: inject secret env vars declared on project beads.
	// Secrets must be named "{project}-*" to prevent cross-project access.
	// Also updates init-clone credential refs for git-related secrets.
	if entry, ok := cfg.ProjectCache[spec.Project]; ok {
		for _, ps := range entry.Secrets {
			if !strings.HasPrefix(ps.Secret, spec.Project+"-") {
				slog.Warn("skipping secret with invalid prefix",
					"secret", ps.Secret, "project", spec.Project)
				continue
			}
			src := podmanager.SecretEnvSource{
				EnvName: ps.Env, SecretName: ps.Secret, SecretKey: ps.Key,
			}
			overrideOrAppendSecretEnv(&spec.SecretEnv, src)

			// Update init container credential refs for git-related overrides.
			switch ps.Env {
			case "GIT_TOKEN", "GIT_USERNAME":
				spec.GitCredentialsSecret = ps.Secret
			case "GITLAB_TOKEN":
				spec.GitlabTokenSecret = ps.Secret
			}
		}

		// Per-project plain env vars: inject non-secret config like
		// JIRA_BASE_URL, JIRA_EMAIL, GIT_AUTHOR_EMAIL, etc.
		if spec.Env == nil {
			spec.Env = make(map[string]string)
		}
		for _, ev := range entry.EnvVars {
			spec.Env[ev.Name] = ev.Value
		}
	}
}

// overrideOrAppendSecretEnv replaces an existing SecretEnvSource with the
// same EnvName, or appends if no match exists.
func overrideOrAppendSecretEnv(envs *[]podmanager.SecretEnvSource, src podmanager.SecretEnvSource) {
	for i, e := range *envs {
		if e.EnvName == src.EnvName {
			(*envs)[i] = src
			return
		}
	}
	*envs = append(*envs, src)
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

// namespaceFromEvent returns the namespace from event metadata or a default.
func namespaceFromEvent(event subscriber.Event, defaultNS string) string {
	if ns := event.Metadata["namespace"]; ns != "" {
		return ns
	}
	return defaultNS
}

// metadataOr returns the event metadata value for key, or fallback if empty.
func metadataOr(event subscriber.Event, key, fallback string) string {
	if v := event.Metadata[key]; v != "" {
		return v
	}
	return fallback
}
