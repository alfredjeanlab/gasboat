package main

import (
	"fmt"
	"log/slog"
	"strings"

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
	case "job":
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

	return spec
}

// buildAgentPodSpec constructs a full AgentPodSpec from an event and config.
// It applies role-specific defaults, then overlays event metadata.
func buildAgentPodSpec(cfg *config.Config, event subscriber.Event) podmanager.AgentPodSpec {
	ns := namespaceFromEvent(event, cfg.Namespace)
	mode := modeForRole(event.Mode, event.Role)

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
	if entry.RTKEnabled {
		spec.Env["RTK_ENABLED"] = "true"
	}
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
	// CLAUDE_CODE_OAUTH_TOKEN: preferred auth method — coop auto-writes
	// .credentials.json when this env var is set. Takes priority over the
	// static credentials secret mount.
	if cfg.ClaudeOAuthTokenSecret != "" {
		spec.SecretEnv = append(spec.SecretEnv, podmanager.SecretEnvSource{
			EnvName:    "CLAUDE_CODE_OAUTH_TOKEN",
			SecretName: cfg.ClaudeOAuthTokenSecret,
			SecretKey:  "token",
		})
	}
	// ANTHROPIC_API_KEY: fallback when OAuth is unavailable.
	if cfg.AnthropicApiKeySecret != "" {
		spec.SecretEnv = append(spec.SecretEnv, podmanager.SecretEnvSource{
			EnvName:    "ANTHROPIC_API_KEY",
			SecretName: cfg.AnthropicApiKeySecret,
			SecretKey:  "key",
		})
	}
	if cfg.BeadsTokenSecret != "" {
		spec.DaemonTokenSecret = cfg.BeadsTokenSecret
	}

	// Git credentials: inject GIT_USERNAME and GIT_TOKEN from secret for clone/push.
	// Also pass the secret name to init-clone container for private repo clones.
	if cfg.GitCredentialsSecret != "" {
		spec.GitCredentialsSecret = cfg.GitCredentialsSecret
		spec.SecretEnv = append(spec.SecretEnv,
			podmanager.SecretEnvSource{
				EnvName:    "GIT_USERNAME",
				SecretName: cfg.GitCredentialsSecret,
				SecretKey:  "username",
			},
			podmanager.SecretEnvSource{
				EnvName:    "GIT_TOKEN",
				SecretName: cfg.GitCredentialsSecret,
				SecretKey:  "token",
			},
		)
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

	// GitHub token for gh CLI (releases, GHCR push) inside agent pods.
	if cfg.GithubTokenSecret != "" {
		spec.SecretEnv = append(spec.SecretEnv, podmanager.SecretEnvSource{
			EnvName:    "GITHUB_TOKEN",
			SecretName: cfg.GithubTokenSecret,
			SecretKey:  "token",
		})
	}

	// GitLab token for glab CLI (GLAB_TOKEN) and git clone/push (GITLAB_TOKEN).
	// Also passed to init-clone for authenticated GitLab repo cloning.
	if cfg.GitlabTokenSecret != "" {
		spec.GitlabTokenSecret = cfg.GitlabTokenSecret
		spec.SecretEnv = append(spec.SecretEnv,
			podmanager.SecretEnvSource{
				EnvName:    "GLAB_TOKEN",
				SecretName: cfg.GitlabTokenSecret,
				SecretKey:  "token",
			},
			podmanager.SecretEnvSource{
				EnvName:    "GITLAB_TOKEN",
				SecretName: cfg.GitlabTokenSecret,
				SecretKey:  "token",
			},
		)
	}

	// RWX access token for RWX API calls (dispatches, triggers) inside agent pods.
	if cfg.RwxAccessTokenSecret != "" {
		spec.SecretEnv = append(spec.SecretEnv, podmanager.SecretEnvSource{
			EnvName:    "RWX_ACCESS_TOKEN",
			SecretName: cfg.RwxAccessTokenSecret,
			SecretKey:  "token",
		})
	}

	// Per-project secret overrides: merge project secrets on top of globals.
	// Matching env names replace the global entry; new env names are additive.
	// Secrets must be named "{project}-*" to prevent cross-project access.
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
