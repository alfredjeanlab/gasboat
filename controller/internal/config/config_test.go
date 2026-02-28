package config

import (
	"os"
	"testing"
	"time"
)

// setEnvs sets multiple environment variables and returns a cleanup function.
func setEnvs(t *testing.T, envs map[string]string) {
	t.Helper()
	for k, v := range envs {
		t.Setenv(k, v)
	}
}

// --- envOr tests ---

func TestEnvOr_Set(t *testing.T) {
	t.Setenv("TEST_ENV_OR", "custom")
	if got := envOr("TEST_ENV_OR", "default"); got != "custom" {
		t.Errorf("envOr = %s, want custom", got)
	}
}

func TestEnvOr_Unset(t *testing.T) {
	os.Unsetenv("TEST_ENV_OR_UNSET")
	if got := envOr("TEST_ENV_OR_UNSET", "fallback"); got != "fallback" {
		t.Errorf("envOr = %s, want fallback", got)
	}
}

func TestEnvOr_Empty(t *testing.T) {
	t.Setenv("TEST_ENV_OR_EMPTY", "")
	if got := envOr("TEST_ENV_OR_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("envOr with empty value = %s, want fallback", got)
	}
}

// --- envIntOr tests ---

func TestEnvIntOr_ValidInt(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	if got := envIntOr("TEST_INT", 0); got != 42 {
		t.Errorf("envIntOr = %d, want 42", got)
	}
}

func TestEnvIntOr_InvalidInt(t *testing.T) {
	t.Setenv("TEST_INT_BAD", "notanumber")
	if got := envIntOr("TEST_INT_BAD", 5); got != 5 {
		t.Errorf("envIntOr with invalid = %d, want 5", got)
	}
}

func TestEnvIntOr_Unset(t *testing.T) {
	os.Unsetenv("TEST_INT_UNSET")
	if got := envIntOr("TEST_INT_UNSET", 10); got != 10 {
		t.Errorf("envIntOr unset = %d, want 10", got)
	}
}

func TestEnvIntOr_Zero(t *testing.T) {
	t.Setenv("TEST_INT_ZERO", "0")
	if got := envIntOr("TEST_INT_ZERO", 99); got != 0 {
		t.Errorf("envIntOr zero = %d, want 0", got)
	}
}

func TestEnvIntOr_Negative(t *testing.T) {
	t.Setenv("TEST_INT_NEG", "-3")
	if got := envIntOr("TEST_INT_NEG", 0); got != -3 {
		t.Errorf("envIntOr negative = %d, want -3", got)
	}
}

// --- envBoolOr tests ---

func TestEnvBoolOr_True(t *testing.T) {
	t.Setenv("TEST_BOOL", "true")
	if got := envBoolOr("TEST_BOOL", false); !got {
		t.Error("envBoolOr = false, want true")
	}
}

func TestEnvBoolOr_False(t *testing.T) {
	t.Setenv("TEST_BOOL_F", "false")
	if got := envBoolOr("TEST_BOOL_F", true); got {
		t.Error("envBoolOr = true, want false")
	}
}

func TestEnvBoolOr_One(t *testing.T) {
	t.Setenv("TEST_BOOL_1", "1")
	if got := envBoolOr("TEST_BOOL_1", false); !got {
		t.Error("envBoolOr(1) = false, want true")
	}
}

func TestEnvBoolOr_Invalid(t *testing.T) {
	t.Setenv("TEST_BOOL_BAD", "yes")
	if got := envBoolOr("TEST_BOOL_BAD", true); !got {
		t.Error("envBoolOr with invalid should return fallback true")
	}
}

func TestEnvBoolOr_Unset(t *testing.T) {
	os.Unsetenv("TEST_BOOL_UNSET")
	if got := envBoolOr("TEST_BOOL_UNSET", true); !got {
		t.Error("envBoolOr unset should return fallback true")
	}
}

// --- envDurationOr tests ---

func TestEnvDurationOr_Valid(t *testing.T) {
	t.Setenv("TEST_DUR", "30s")
	if got := envDurationOr("TEST_DUR", time.Minute); got != 30*time.Second {
		t.Errorf("envDurationOr = %v, want 30s", got)
	}
}

func TestEnvDurationOr_Minutes(t *testing.T) {
	t.Setenv("TEST_DUR_M", "5m")
	if got := envDurationOr("TEST_DUR_M", time.Second); got != 5*time.Minute {
		t.Errorf("envDurationOr = %v, want 5m", got)
	}
}

func TestEnvDurationOr_Invalid(t *testing.T) {
	t.Setenv("TEST_DUR_BAD", "notaduration")
	if got := envDurationOr("TEST_DUR_BAD", 2*time.Minute); got != 2*time.Minute {
		t.Errorf("envDurationOr with invalid = %v, want 2m", got)
	}
}

func TestEnvDurationOr_Unset(t *testing.T) {
	os.Unsetenv("TEST_DUR_UNSET")
	if got := envDurationOr("TEST_DUR_UNSET", time.Hour); got != time.Hour {
		t.Errorf("envDurationOr unset = %v, want 1h", got)
	}
}

// --- hostname tests ---

func TestHostname_ReturnsNonEmpty(t *testing.T) {
	h := hostname()
	if h == "" {
		t.Error("hostname() returned empty string")
	}
}

// --- Parse tests ---

func TestParse_Defaults(t *testing.T) {
	// Clear all relevant env vars to get defaults.
	for _, key := range []string{
		"NAMESPACE", "KUBECONFIG", "BEADS_GRPC_ADDR", "BEADS_HTTP_ADDR",
		"COOP_IMAGE", "COOP_BURST_LIMIT", "COOP_MAX_PODS", "COOP_SYNC_INTERVAL",
		"ENABLE_LEADER_ELECTION", "LEADER_ELECTION_ID", "LOG_LEVEL",
		"EXTERNAL_SECRET_STORE_NAME", "EXTERNAL_SECRET_STORE_KIND",
		"EXTERNAL_SECRET_REFRESH_INTERVAL",
	} {
		os.Unsetenv(key)
	}

	cfg := Parse()

	if cfg.Namespace != "gasboat" {
		t.Errorf("Namespace = %s, want gasboat", cfg.Namespace)
	}
	if cfg.BeadsGRPCAddr != "localhost:9090" {
		t.Errorf("BeadsGRPCAddr = %s, want localhost:9090", cfg.BeadsGRPCAddr)
	}
	if cfg.BeadsHTTPAddr != "localhost:8080" {
		t.Errorf("BeadsHTTPAddr = %s, want localhost:8080", cfg.BeadsHTTPAddr)
	}
	if cfg.CoopBurstLimit != 3 {
		t.Errorf("CoopBurstLimit = %d, want 3", cfg.CoopBurstLimit)
	}
	if cfg.CoopMaxPods != 0 {
		t.Errorf("CoopMaxPods = %d, want 0", cfg.CoopMaxPods)
	}
	if cfg.CoopSyncInterval != 60*time.Second {
		t.Errorf("CoopSyncInterval = %v, want 60s", cfg.CoopSyncInterval)
	}
	if cfg.LeaderElection {
		t.Error("LeaderElection should default to false")
	}
	if cfg.LeaderElectionID != "agents-leader" {
		t.Errorf("LeaderElectionID = %s, want agents-leader", cfg.LeaderElectionID)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %s, want info", cfg.LogLevel)
	}
	if cfg.ExternalSecretStoreName != "secretstore" {
		t.Errorf("ExternalSecretStoreName = %s, want secretstore", cfg.ExternalSecretStoreName)
	}
	if cfg.ExternalSecretStoreKind != "ClusterSecretStore" {
		t.Errorf("ExternalSecretStoreKind = %s, want ClusterSecretStore", cfg.ExternalSecretStoreKind)
	}
	if cfg.ExternalSecretRefreshInterval != "15m" {
		t.Errorf("ExternalSecretRefreshInterval = %s, want 15m", cfg.ExternalSecretRefreshInterval)
	}
}

func TestParse_CustomValues(t *testing.T) {
	setEnvs(t, map[string]string{
		"NAMESPACE":               "custom-ns",
		"BEADS_GRPC_ADDR":         "beads:9091",
		"BEADS_HTTP_ADDR":         "beads:8081",
		"COOP_IMAGE":              "my-agent:v2",
		"COOP_BURST_LIMIT":        "5",
		"COOP_MAX_PODS":           "10",
		"COOP_SYNC_INTERVAL":      "2m",
		"ENABLE_LEADER_ELECTION":  "true",
		"LEADER_ELECTION_ID":      "custom-leader",
		"LOG_LEVEL":               "debug",
		"COOP_SERVICE_ACCOUNT":    "agent-sa",
		"BEADS_TOKEN_SECRET":      "beads-token",
		"NATS_URL":                "nats://nats:4222",
		"CLAUDE_MODEL":            "claude-opus-4-6",
		"CLAUDE_OAUTH_SECRET":     "claude-oauth",
		"GIT_CREDENTIALS_SECRET":  "git-creds",
		"GITHUB_TOKEN_SECRET":     "gh-token",
		"COOPMUX_URL":             "http://coopmux:8080",
		"AGENT_STORAGE_CLASS":     "gp3",
	})

	cfg := Parse()

	if cfg.Namespace != "custom-ns" {
		t.Errorf("Namespace = %s, want custom-ns", cfg.Namespace)
	}
	if cfg.BeadsGRPCAddr != "beads:9091" {
		t.Errorf("BeadsGRPCAddr = %s, want beads:9091", cfg.BeadsGRPCAddr)
	}
	if cfg.CoopImage != "my-agent:v2" {
		t.Errorf("CoopImage = %s, want my-agent:v2", cfg.CoopImage)
	}
	if cfg.CoopBurstLimit != 5 {
		t.Errorf("CoopBurstLimit = %d, want 5", cfg.CoopBurstLimit)
	}
	if cfg.CoopMaxPods != 10 {
		t.Errorf("CoopMaxPods = %d, want 10", cfg.CoopMaxPods)
	}
	if cfg.CoopSyncInterval != 2*time.Minute {
		t.Errorf("CoopSyncInterval = %v, want 2m", cfg.CoopSyncInterval)
	}
	if !cfg.LeaderElection {
		t.Error("LeaderElection should be true")
	}
	if cfg.LeaderElectionID != "custom-leader" {
		t.Errorf("LeaderElectionID = %s, want custom-leader", cfg.LeaderElectionID)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %s, want debug", cfg.LogLevel)
	}
	if cfg.CoopServiceAccount != "agent-sa" {
		t.Errorf("CoopServiceAccount = %s, want agent-sa", cfg.CoopServiceAccount)
	}
	if cfg.BeadsTokenSecret != "beads-token" {
		t.Errorf("BeadsTokenSecret = %s, want beads-token", cfg.BeadsTokenSecret)
	}
	if cfg.NatsURL != "nats://nats:4222" {
		t.Errorf("NatsURL = %s, want nats://nats:4222", cfg.NatsURL)
	}
	if cfg.ClaudeModel != "claude-opus-4-6" {
		t.Errorf("ClaudeModel = %s, want claude-opus-4-6", cfg.ClaudeModel)
	}
	if cfg.ClaudeOAuthSecret != "claude-oauth" {
		t.Errorf("ClaudeOAuthSecret = %s, want claude-oauth", cfg.ClaudeOAuthSecret)
	}
	if cfg.GitCredentialsSecret != "git-creds" {
		t.Errorf("GitCredentialsSecret = %s, want git-creds", cfg.GitCredentialsSecret)
	}
	if cfg.GithubTokenSecret != "gh-token" {
		t.Errorf("GithubTokenSecret = %s, want gh-token", cfg.GithubTokenSecret)
	}
	if cfg.CoopmuxURL != "http://coopmux:8080" {
		t.Errorf("CoopmuxURL = %s, want http://coopmux:8080", cfg.CoopmuxURL)
	}
	if cfg.AgentStorageClass != "gp3" {
		t.Errorf("AgentStorageClass = %s, want gp3", cfg.AgentStorageClass)
	}
}

func TestParse_LeaderElectionIdentity_FromPodName(t *testing.T) {
	t.Setenv("POD_NAME", "controller-abc-xyz")
	cfg := Parse()
	if cfg.LeaderElectionIdentity != "controller-abc-xyz" {
		t.Errorf("LeaderElectionIdentity = %s, want controller-abc-xyz", cfg.LeaderElectionIdentity)
	}
}

func TestParse_LeaderElectionIdentity_DefaultsToHostname(t *testing.T) {
	os.Unsetenv("POD_NAME")
	cfg := Parse()
	expected := hostname()
	if cfg.LeaderElectionIdentity != expected {
		t.Errorf("LeaderElectionIdentity = %s, want hostname %s", cfg.LeaderElectionIdentity, expected)
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
