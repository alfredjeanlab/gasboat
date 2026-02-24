package main

import (
	"fmt"
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
	case "captain", "mate":
		return "crew"
	case "deckhand":
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
		Image:     image,
		Namespace: cfg.Namespace,
		Env: map[string]string{
			"BEADS_GRPC_ADDR": cfg.BeadsGRPCAddr,
			"BEADS_HTTP_ADDR": cfg.BeadsHTTPAddr,
			"BEADS_SERVER":    cfg.BeadsGRPCAddr,
		},
	}

	// Model selection (default: opus).
	model := "opus"
	if m := metadata["model"]; m != "" {
		model = m
	}
	spec.Env["CLAUDE_MODEL"] = model

	defaults := podmanager.DefaultPodDefaults(mode)
	podmanager.ApplyDefaults(&spec, defaults)

	// Apply project-level overrides from project bead metadata.
	applyProjectDefaults(cfg, &spec)

	applyCommonConfig(cfg, &spec)

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
		Image:     event.Metadata["image"],
		Namespace: ns,
		Env: map[string]string{
			"BEADS_GRPC_ADDR": metadataOr(event, "beads_grpc_addr", cfg.BeadsGRPCAddr),
			"BEADS_HTTP_ADDR": cfg.BeadsHTTPAddr,
			"BEADS_SERVER":    metadataOr(event, "beads_grpc_addr", cfg.BeadsGRPCAddr),
		},
	}

	// Apply mode-specific defaults (workspace storage, resources).
	defaults := podmanager.DefaultPodDefaults(mode)
	podmanager.ApplyDefaults(&spec, defaults)

	// Apply project-level overrides from project bead metadata.
	applyProjectDefaults(cfg, &spec)

	// Overlay event metadata for optional fields.
	if sa := event.Metadata["service_account"]; sa != "" {
		spec.ServiceAccountName = sa
	}
	if cm := event.Metadata["configmap"]; cm != "" {
		spec.ConfigMapName = cm
	}

	// Model selection (default: opus).
	model := metadataOr(event, "model", "opus")
	spec.Env["CLAUDE_MODEL"] = model

	// Apply common config (credentials, daemon token, coop, NATS).
	applyCommonConfig(cfg, &spec)

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

	// Wire git info from project cache.
	if entry, ok := cfg.ProjectCache[spec.Project]; ok {
		if entry.GitURL != "" {
			spec.GitURL = entry.GitURL
		}
		if entry.DefaultBranch != "" {
			spec.GitDefaultBranch = entry.DefaultBranch
		}
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

	// GitLab token for glab CLI and git clone/push to GitLab inside agent pods.
	if cfg.GitlabTokenSecret != "" {
		spec.SecretEnv = append(spec.SecretEnv, podmanager.SecretEnvSource{
			EnvName:    "GITLAB_TOKEN",
			SecretName: cfg.GitlabTokenSecret,
			SecretKey:  "token",
		})
	}
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
