package config

import (
	"testing"
	"time"
)

// ── envOr ──────────────────────────────────────────────────────────────────

func TestEnvOr_Fallback(t *testing.T) {
	if got := envOr("CONFIG_TEST_UNSET_VAR_XYZ", "default"); got != "default" {
		t.Errorf("envOr = %q, want default", got)
	}
}

func TestEnvOr_Set(t *testing.T) {
	t.Setenv("CONFIG_TEST_SET_VAR", "custom")
	if got := envOr("CONFIG_TEST_SET_VAR", "default"); got != "custom" {
		t.Errorf("envOr = %q, want custom", got)
	}
}

// ── envIntOr ───────────────────────────────────────────────────────────────

func TestEnvIntOr_Fallback(t *testing.T) {
	if got := envIntOr("CONFIG_TEST_UNSET_INT", 42); got != 42 {
		t.Errorf("envIntOr = %d, want 42", got)
	}
}

func TestEnvIntOr_Valid(t *testing.T) {
	t.Setenv("CONFIG_TEST_INT", "10")
	if got := envIntOr("CONFIG_TEST_INT", 42); got != 10 {
		t.Errorf("envIntOr = %d, want 10", got)
	}
}

func TestEnvIntOr_Invalid(t *testing.T) {
	t.Setenv("CONFIG_TEST_INT_BAD", "not-a-number")
	if got := envIntOr("CONFIG_TEST_INT_BAD", 42); got != 42 {
		t.Errorf("envIntOr = %d, want 42 (fallback on invalid)", got)
	}
}

// ── envBoolOr ──────────────────────────────────────────────────────────────

func TestEnvBoolOr_Fallback(t *testing.T) {
	if got := envBoolOr("CONFIG_TEST_UNSET_BOOL", true); !got {
		t.Error("envBoolOr = false, want true (fallback)")
	}
}

func TestEnvBoolOr_True(t *testing.T) {
	t.Setenv("CONFIG_TEST_BOOL", "true")
	if got := envBoolOr("CONFIG_TEST_BOOL", false); !got {
		t.Error("envBoolOr = false, want true")
	}
}

func TestEnvBoolOr_False(t *testing.T) {
	t.Setenv("CONFIG_TEST_BOOL_F", "false")
	if got := envBoolOr("CONFIG_TEST_BOOL_F", true); got {
		t.Error("envBoolOr = true, want false")
	}
}

func TestEnvBoolOr_Invalid(t *testing.T) {
	t.Setenv("CONFIG_TEST_BOOL_BAD", "not-bool")
	if got := envBoolOr("CONFIG_TEST_BOOL_BAD", true); !got {
		t.Error("envBoolOr = false, want true (fallback on invalid)")
	}
}

// ── envDurationOr ──────────────────────────────────────────────────────────

func TestEnvDurationOr_Fallback(t *testing.T) {
	if got := envDurationOr("CONFIG_TEST_UNSET_DUR", 30*time.Second); got != 30*time.Second {
		t.Errorf("envDurationOr = %v, want 30s", got)
	}
}

func TestEnvDurationOr_Valid(t *testing.T) {
	t.Setenv("CONFIG_TEST_DUR", "5m")
	if got := envDurationOr("CONFIG_TEST_DUR", time.Minute); got != 5*time.Minute {
		t.Errorf("envDurationOr = %v, want 5m", got)
	}
}

func TestEnvDurationOr_Invalid(t *testing.T) {
	t.Setenv("CONFIG_TEST_DUR_BAD", "not-a-duration")
	if got := envDurationOr("CONFIG_TEST_DUR_BAD", time.Minute); got != time.Minute {
		t.Errorf("envDurationOr = %v, want 1m (fallback on invalid)", got)
	}
}

// ── hostname ───────────────────────────────────────────────────────────────

func TestHostname(t *testing.T) {
	h := hostname()
	if h == "" {
		t.Error("hostname() returned empty string")
	}
}

// ── Parse ──────────────────────────────────────────────────────────────────

func TestParse_Defaults(t *testing.T) {
	// Clear env vars that might be set in the pod environment
	// so we test actual defaults.
	t.Setenv("NAMESPACE", "")
	t.Setenv("BEADS_GRPC_ADDR", "")
	t.Setenv("BEADS_HTTP_ADDR", "")
	t.Setenv("COOP_BURST_LIMIT", "")
	t.Setenv("COOP_SYNC_INTERVAL", "")
	t.Setenv("ENABLE_LEADER_ELECTION", "")
	t.Setenv("LEADER_ELECTION_ID", "")
	t.Setenv("EXTERNAL_SECRET_STORE_NAME", "")
	t.Setenv("LOG_LEVEL", "")

	cfg := Parse()

	if cfg.Namespace != "gasboat" {
		t.Errorf("Namespace = %q, want gasboat", cfg.Namespace)
	}
	if cfg.BeadsGRPCAddr != "localhost:9090" {
		t.Errorf("BeadsGRPCAddr = %q, want localhost:9090", cfg.BeadsGRPCAddr)
	}
	if cfg.BeadsHTTPAddr != "localhost:8080" {
		t.Errorf("BeadsHTTPAddr = %q, want localhost:8080", cfg.BeadsHTTPAddr)
	}
	if cfg.CoopBurstLimit != 3 {
		t.Errorf("CoopBurstLimit = %d, want 3", cfg.CoopBurstLimit)
	}
	if cfg.CoopSyncInterval != 60*time.Second {
		t.Errorf("CoopSyncInterval = %v, want 60s", cfg.CoopSyncInterval)
	}
	if cfg.LeaderElection {
		t.Error("LeaderElection should default to false")
	}
	if cfg.LeaderElectionID != "agents-leader" {
		t.Errorf("LeaderElectionID = %q, want agents-leader", cfg.LeaderElectionID)
	}
	if cfg.ExternalSecretStoreName != "secretstore" {
		t.Errorf("ExternalSecretStoreName = %q, want secretstore", cfg.ExternalSecretStoreName)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
}

func TestParse_EnvOverrides(t *testing.T) {
	t.Setenv("NAMESPACE", "custom-ns")
	t.Setenv("COOP_IMAGE", "ghcr.io/agent:v2")
	t.Setenv("COOP_MAX_PODS", "5")
	t.Setenv("COOP_BURST_LIMIT", "10")
	t.Setenv("COOP_SYNC_INTERVAL", "30s")
	t.Setenv("ENABLE_LEADER_ELECTION", "true")
	t.Setenv("LOG_LEVEL", "debug")

	cfg := Parse()

	if cfg.Namespace != "custom-ns" {
		t.Errorf("Namespace = %q, want custom-ns", cfg.Namespace)
	}
	if cfg.CoopImage != "ghcr.io/agent:v2" {
		t.Errorf("CoopImage = %q, want ghcr.io/agent:v2", cfg.CoopImage)
	}
	if cfg.CoopMaxPods != 5 {
		t.Errorf("CoopMaxPods = %d, want 5", cfg.CoopMaxPods)
	}
	if cfg.CoopBurstLimit != 10 {
		t.Errorf("CoopBurstLimit = %d, want 10", cfg.CoopBurstLimit)
	}
	if cfg.CoopSyncInterval != 30*time.Second {
		t.Errorf("CoopSyncInterval = %v, want 30s", cfg.CoopSyncInterval)
	}
	if !cfg.LeaderElection {
		t.Error("LeaderElection should be true")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestParse_SecretEnvVars(t *testing.T) {
	t.Setenv("BEADS_TOKEN_SECRET", "beads-token")
	t.Setenv("CLAUDE_OAUTH_SECRET", "claude-oauth")
	t.Setenv("CLAUDE_OAUTH_TOKEN_SECRET", "claude-token")
	t.Setenv("ANTHROPIC_API_KEY_SECRET", "anthropic-key")
	t.Setenv("GIT_CREDENTIALS_SECRET", "git-creds")
	t.Setenv("GITHUB_TOKEN_SECRET", "gh-token")
	t.Setenv("GITLAB_TOKEN_SECRET", "gl-token")
	t.Setenv("RWX_ACCESS_TOKEN_SECRET", "rwx-token")
	t.Setenv("NATS_TOKEN_SECRET", "nats-token")
	t.Setenv("COOPMUX_TOKEN_SECRET", "coopmux-token")

	cfg := Parse()

	checks := map[string]string{
		"BeadsTokenSecret":       cfg.BeadsTokenSecret,
		"ClaudeOAuthSecret":      cfg.ClaudeOAuthSecret,
		"ClaudeOAuthTokenSecret": cfg.ClaudeOAuthTokenSecret,
		"AnthropicApiKeySecret":  cfg.AnthropicApiKeySecret,
		"GitCredentialsSecret":   cfg.GitCredentialsSecret,
		"GithubTokenSecret":      cfg.GithubTokenSecret,
		"GitlabTokenSecret":      cfg.GitlabTokenSecret,
		"RwxAccessTokenSecret":   cfg.RwxAccessTokenSecret,
		"NatsTokenSecret":        cfg.NatsTokenSecret,
		"CoopmuxTokenSecret":     cfg.CoopmuxTokenSecret,
	}
	expected := map[string]string{
		"BeadsTokenSecret":       "beads-token",
		"ClaudeOAuthSecret":      "claude-oauth",
		"ClaudeOAuthTokenSecret": "claude-token",
		"AnthropicApiKeySecret":  "anthropic-key",
		"GitCredentialsSecret":   "git-creds",
		"GithubTokenSecret":      "gh-token",
		"GitlabTokenSecret":      "gl-token",
		"RwxAccessTokenSecret":   "rwx-token",
		"NatsTokenSecret":        "nats-token",
		"CoopmuxTokenSecret":     "coopmux-token",
	}
	for name, got := range checks {
		if got != expected[name] {
			t.Errorf("%s = %q, want %q", name, got, expected[name])
		}
	}
}
