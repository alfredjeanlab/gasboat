package beadsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SecretEntry maps a K8s Secret key to a pod environment variable.
// Used in per-project secret overrides on project beads.
type SecretEntry struct {
	Env    string `json:"env"`    // env var name in the pod
	Secret string `json:"secret"` // K8s Secret name
	Key    string `json:"key"`    // key within the Secret
}

// EnvEntry maps a plain environment variable name to a value.
// Used for non-secret configuration on project beads.
type EnvEntry struct {
	Name  string `json:"name"`  // env var name in the pod
	Value string `json:"value"` // plain text value
}

// RepoEntry declares a repository to clone into the agent workspace.
type RepoEntry struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
	Role   string `json:"role,omitempty"` // "primary" or "reference"
	Name   string `json:"name,omitempty"`
}

// ProjectInfo represents a registered project from daemon project beads.
type ProjectInfo struct {
	Name           string // Project name (from bead title)
	Prefix         string // Beads prefix (e.g., "kd", "bot")
	GitURL         string // Repository URL
	DefaultBranch  string // Default branch (e.g., "main")
	Image          string // Per-project agent image override
	StorageClass   string // Per-project PVC storage class override
	ServiceAccount string // Per-project K8s ServiceAccount override
	RTKEnabled     bool   // Enable RTK token optimization for this project

	// Tier 1 enhancements: per-project pod resource overrides.
	CPURequest    string // Kubernetes quantity string, e.g. "500m"
	CPULimit      string // Kubernetes quantity string, e.g. "2000m"
	MemoryRequest string // Kubernetes quantity string, e.g. "512Mi"
	MemoryLimit   string // Kubernetes quantity string, e.g. "2Gi"

	// SlackChannel is the Slack channel ID associated with this project.
	// Used to infer the default project when /spawn is invoked from a channel.
	SlackChannel string

	// EnvOverrides holds extra env vars parsed from the env_json bead field.
	// Keys absent or empty in the JSON are silently skipped.
	EnvOverrides map[string]string

	Secrets        []SecretEntry // Per-project secret overrides
	EnvVars        []EnvEntry    // Per-project plain env vars
	Repos          []RepoEntry   // Multi-repo definitions
}

// ListProjectBeads queries the daemon for project beads (type=project) and extracts
// project metadata from fields. Returns a map of project name -> ProjectInfo.
func (c *Client) ListProjectBeads(ctx context.Context) (map[string]ProjectInfo, error) {
	resp, err := c.listBeads(ctx, []string{"project"}, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("listing project beads: %w", err)
	}

	rigs := make(map[string]ProjectInfo)
	for _, b := range resp.Beads {
		// Strip "Project: " prefix from title -- legacy project beads may have titles
		// like "Project: beads" instead of just "beads".
		name := strings.TrimPrefix(b.Title, "Project: ")
		fields := b.fieldsMap()
		info := ProjectInfo{
			Name:           name,
			Prefix:         fields["prefix"],
			GitURL:         fields["git_url"],
			DefaultBranch:  fields["default_branch"],
			Image:          fields["image"],
			StorageClass:   fields["storage_class"],
			ServiceAccount: fields["service_account"],
			RTKEnabled:     fields["rtk_enabled"] == "true",
			CPURequest:     fields["cpu_request"],
			CPULimit:       fields["cpu_limit"],
			MemoryRequest:  fields["memory_request"],
			MemoryLimit:    fields["memory_limit"],
			SlackChannel:   fields["slack_channel"],
		}
		// Parse per-project secrets from JSON field.
		if raw := fields["secrets"]; raw != "" {
			var secrets []SecretEntry
			if json.Unmarshal([]byte(raw), &secrets) == nil {
				info.Secrets = secrets
			}
		}
		// Parse per-project plain env vars from JSON field.
		if raw := fields["env"]; raw != "" {
			var envVars []EnvEntry
			if json.Unmarshal([]byte(raw), &envVars) == nil {
				info.EnvVars = envVars
			}
		}
		// Parse multi-repo definitions from JSON field.
		if raw := fields["repos"]; raw != "" {
			var repos []RepoEntry
			if json.Unmarshal([]byte(raw), &repos) == nil {
				info.Repos = repos
			}
		}
		// Parse env_json field.
		if raw := fields["env_json"]; raw != "" {
			var envMap map[string]string
			if err := json.Unmarshal([]byte(raw), &envMap); err != nil {
				// Log and skip malformed env_json rather than failing the whole refresh.
				_ = fmt.Errorf("project %q: malformed env_json (skipped): %w", name, err)
			} else {
				info.EnvOverrides = envMap
			}
		}
		if name != "" {
			rigs[name] = info
		}
	}

	return rigs, nil
}
