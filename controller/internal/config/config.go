// Package config provides controller configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds controller configuration. Values come from env vars or defaults.
type Config struct {
	// --- Kubernetes ---

	// Namespace is the K8s namespace to operate in (env: NAMESPACE).
	Namespace string

	// KubeConfig is the path to kubeconfig file (env: KUBECONFIG).
	// Empty means use in-cluster config.
	KubeConfig string

	// --- Beads Daemon ---

	// BeadsGRPCAddr is the beads daemon gRPC address, host:port (env: BEADS_GRPC_ADDR).
	BeadsGRPCAddr string

	// BeadsHTTPAddr is the beads daemon HTTP address, host:port (env: BEADS_HTTP_ADDR).
	BeadsHTTPAddr string

	// BeadsTokenSecret is the K8s secret containing the daemon auth token (env: BEADS_TOKEN_SECRET).
	// The controller reads the token value from this secret at startup for its own API calls,
	// and passes the secret name to agent pods for secretKeyRef injection.
	BeadsTokenSecret string

	// --- NATS (passed to agent pods only, controller uses SSE) ---

	// NatsURL is the NATS server URL for event bus (env: NATS_URL).
	// Passed to agent pods as BEADS_NATS_URL and COOP_NATS_URL.
	NatsURL string

	// NatsTokenSecret is the K8s secret containing the NATS auth token (env: NATS_TOKEN_SECRET).
	// Injected as COOP_NATS_TOKEN in agent pods.
	NatsTokenSecret string

	// --- Agent Pods ---

	// CoopImage is the default container image for agent pods (env: COOP_IMAGE).
	CoopImage string

	// CoopServiceAccount is the K8s ServiceAccount to use for agent pods (env: COOP_SERVICE_ACCOUNT).
	// When set, all agent pods use this SA unless overridden by bead metadata.
	CoopServiceAccount string

	// CoopMaxPods is the maximum number of agent pods that can exist
	// simultaneously (env: COOP_MAX_PODS). 0 means unlimited.
	// When the limit is reached, new pods are queued until existing ones finish.
	CoopMaxPods int

	// CoopBurstLimit is the maximum number of pods to create in a single
	// reconciliation pass (env: COOP_BURST_LIMIT). Default: 3.
	// This prevents memory pressure from simultaneous pod initialization.
	CoopBurstLimit int

	// CoopSyncInterval is how often to reconcile pod statuses with beads (env: COOP_SYNC_INTERVAL).
	// Default: 60s.
	CoopSyncInterval time.Duration

	// AgentStorageClass is the default StorageClass for agent workspace PVCs
	// (env: AGENT_STORAGE_CLASS). When set, crew-mode pods use this unless
	// overridden by a project bead's storage_class label.
	AgentStorageClass string

	// --- Secrets & Credentials ---

	// ClaudeOAuthSecret is the K8s secret containing Claude OAuth credentials (env: CLAUDE_OAUTH_SECRET).
	// Mounted as ~/.claude/.credentials.json in agent pods for Max/Corp accounts.
	ClaudeOAuthSecret string

	// ClaudeOAuthTokenSecret is the K8s secret containing a Claude OAuth access token
	// (env: CLAUDE_OAUTH_TOKEN_SECRET). The "token" key is injected as
	// CLAUDE_CODE_OAUTH_TOKEN in agent pods. When set, coop auto-writes
	// .credentials.json — preferred over the static credentials secret mount.
	ClaudeOAuthTokenSecret string

	// AnthropicApiKeySecret is the K8s secret containing an Anthropic API key
	// (env: ANTHROPIC_API_KEY_SECRET). The "key" key is injected as
	// ANTHROPIC_API_KEY in agent pods. Fallback when OAuth is unavailable.
	AnthropicApiKeySecret string

	// GitCredentialsSecret is the K8s secret containing git credentials (env: GIT_CREDENTIALS_SECRET).
	// Keys "username" and "token" are injected as GIT_USERNAME and GIT_TOKEN env vars
	// in agent pods for git clone/push to GitHub.
	GitCredentialsSecret string

	// GithubTokenSecret is the K8s secret containing a GitHub token (env: GITHUB_TOKEN_SECRET).
	// Injected as GITHUB_TOKEN in agent pods for gh CLI operations (releases, GHCR push).
	GithubTokenSecret string

	// GitlabTokenSecret is the K8s secret containing a GitLab token (env: GITLAB_TOKEN_SECRET).
	// Injected as GITLAB_TOKEN in agent pods for glab CLI operations and git clone/push.
	GitlabTokenSecret string

	// --- Coopmux ---

	// CoopmuxURL is the URL of the coopmux service (env: COOPMUX_URL).
	// When set, agent pods register with coopmux for credential distribution and
	// terminal multiplexing.
	CoopmuxURL string

	// CoopmuxTokenSecret is the K8s secret containing the coopmux auth token (env: COOPMUX_TOKEN_SECRET).
	// Injected as COOP_BROKER_TOKEN and COOP_MUX_TOKEN in agent pods.
	CoopmuxTokenSecret string

	// --- Leader Election ---

	// LeaderElection enables K8s lease-based leader election (env: ENABLE_LEADER_ELECTION).
	// When true, only the leader replica reconciles; others wait passively.
	// Required for running multiple replicas safely.
	LeaderElection bool

	// LeaderElectionID is the name of the Lease resource used for leader election
	// (env: LEADER_ELECTION_ID). Default: "agents-leader".
	LeaderElectionID string

	// LeaderElectionIdentity is the unique identity of this controller instance
	// (env: POD_NAME). Typically set from the Kubernetes downward API.
	// Default: hostname.
	LeaderElectionIdentity string

	// Slack notifications are now handled by the standalone slack-bridge
	// binary (cmd/slack-bridge). Slack config fields removed — see bd-8x8fy.

	// --- Controller ---

	// LogLevel controls log verbosity: debug, info, warn, error (env: LOG_LEVEL).
	LogLevel string

	// --- Runtime (not from env) ---

	// ProjectCache maps project name → metadata, populated at runtime from project beads
	// in the daemon. Not parsed from env.
	ProjectCache map[string]ProjectCacheEntry
}

// ProjectCacheEntry holds project metadata from daemon project beads.
type ProjectCacheEntry struct {
	Prefix        string // e.g., "kd", "bot"
	GitURL        string // e.g., "https://github.com/groblegark/kbeads.git"
	DefaultBranch string // e.g., "main"

	// Per-project pod customization (from project bead labels).
	Image        string // Override agent image for this project
	StorageClass string // Override PVC storage class
}

// Parse reads configuration from environment variables.
func Parse() *Config {
	return &Config{
		// Kubernetes
		Namespace:  envOr("NAMESPACE", "gasboat"),
		KubeConfig: os.Getenv("KUBECONFIG"),

		// Beads Daemon
		BeadsGRPCAddr:    envOr("BEADS_GRPC_ADDR", "localhost:9090"),
		BeadsHTTPAddr:    envOr("BEADS_HTTP_ADDR", "localhost:8080"),
		BeadsTokenSecret: os.Getenv("BEADS_TOKEN_SECRET"),

		// NATS Event Bus (passed to agent pods, not used by the controller itself)
		NatsURL:         os.Getenv("NATS_URL"),
		NatsTokenSecret: os.Getenv("NATS_TOKEN_SECRET"),

		// Agent Pods
		CoopImage:          os.Getenv("COOP_IMAGE"),
		CoopServiceAccount: os.Getenv("COOP_SERVICE_ACCOUNT"),
		CoopMaxPods:        envIntOr("COOP_MAX_PODS", 0),
		CoopBurstLimit:     envIntOr("COOP_BURST_LIMIT", 3),
		CoopSyncInterval:   envDurationOr("COOP_SYNC_INTERVAL", 60*time.Second),
		AgentStorageClass:  os.Getenv("AGENT_STORAGE_CLASS"),

		// Secrets & Credentials
		ClaudeOAuthSecret:      os.Getenv("CLAUDE_OAUTH_SECRET"),
		ClaudeOAuthTokenSecret: os.Getenv("CLAUDE_OAUTH_TOKEN_SECRET"),
		AnthropicApiKeySecret:  os.Getenv("ANTHROPIC_API_KEY_SECRET"),
		GitCredentialsSecret:   os.Getenv("GIT_CREDENTIALS_SECRET"),
		GithubTokenSecret:      os.Getenv("GITHUB_TOKEN_SECRET"),
		GitlabTokenSecret:      os.Getenv("GITLAB_TOKEN_SECRET"),

		// Coopmux
		CoopmuxURL:         os.Getenv("COOPMUX_URL"),
		CoopmuxTokenSecret: os.Getenv("COOPMUX_TOKEN_SECRET"),

		// Leader Election
		LeaderElection:         envBoolOr("ENABLE_LEADER_ELECTION", false),
		LeaderElectionID:       envOr("LEADER_ELECTION_ID", "agents-leader"),
		LeaderElectionIdentity: envOr("POD_NAME", hostname()),

		// Slack config removed — handled by standalone slack-bridge (bd-8x8fy).

		// Controller
		LogLevel: envOr("LOG_LEVEL", "info"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envBoolOr(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return fallback
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
