// Package config provides controller configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"sync"
	"time"

	"gasboat/controller/internal/beadsapi"
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

	// BeadsE2EHTTPAddr is the beads daemon HTTP address for the e2e-isolated
	// namespace (env: BEADS_E2E_HTTP_ADDR). Passed to agent pods so e2e tests
	// create spawn events in an isolated beads instance.
	BeadsE2EHTTPAddr string

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
	// simultaneously (env: COOP_MAX_PODS). 0 means unlimited. Default: 30.
	// When the limit is reached, new pods are queued until existing ones finish.
	CoopMaxPods int

	// CoopRateLimitWindow is the sliding window for pod creation rate limiting
	// (env: COOP_RATE_LIMIT_WINDOW). Default: 5m.
	CoopRateLimitWindow time.Duration

	// CoopRateLimitMax is the max pods that can be created within the rate limit
	// window before the reconciler pauses creation (env: COOP_RATE_LIMIT_MAX).
	// 0 means no rate limiting. Default: 20.
	CoopRateLimitMax int

	// CoopCircuitBreakerThreshold is the max pod creations within
	// CoopCircuitBreakerWindow before the circuit breaker trips and halts
	// all creation until cooldown elapses (env: COOP_CIRCUIT_BREAKER_THRESHOLD).
	// 0 means disabled. Default: 20.
	CoopCircuitBreakerThreshold int

	// CoopCircuitBreakerWindow is the rolling window over which creations
	// are counted for the circuit breaker (env: COOP_CIRCUIT_BREAKER_WINDOW).
	// Default: 2m.
	CoopCircuitBreakerWindow time.Duration

	// CoopCircuitBreakerCooldown is how long the circuit breaker stays open
	// after tripping before auto-resetting (env: COOP_CIRCUIT_BREAKER_COOLDOWN).
	// Default: 5m.
	CoopCircuitBreakerCooldown time.Duration

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

	// ClaudeModel is the Claude model ID for agent pods (env: CLAUDE_MODEL).
	// Injected as CLAUDE_MODEL env var. When empty, Claude Code uses its default.
	ClaudeModel string

	// ClaudeTeamsEnabled enables Claude Code Agent Teams mode for agent pods
	// (env: CLAUDE_TEAMS_ENABLED). When true, the team lead session can spawn
	// and coordinate teammate sessions within the same pod.
	ClaudeTeamsEnabled bool

	// ClaudeTeammateMode is the teammate display mode (env: CLAUDE_TEAMMATE_MODE).
	// Valid values: "tmux" (each teammate in a tmux pane) or "in-process".
	// Default: "tmux".
	ClaudeTeammateMode string

	// ClaudeTeamsMaxTeammates is the maximum number of teammate sessions per pod
	// (env: CLAUDE_TEAMS_MAX_TEAMMATES). 0 means use Claude Code's built-in default.
	ClaudeTeamsMaxTeammates int

	// ClaudeTeams resource overrides: when teams mode is enabled, agent pods
	// need more memory and CPU because each teammate runs its own Claude Code
	// session (Node.js process + context window). These override the default
	// agent pod resources only when ClaudeTeamsEnabled is true.
	ClaudeTeamsCPURequest    string // env: CLAUDE_TEAMS_CPU_REQUEST
	ClaudeTeamsCPULimit      string // env: CLAUDE_TEAMS_CPU_LIMIT
	ClaudeTeamsMemoryRequest string // env: CLAUDE_TEAMS_MEMORY_REQUEST
	ClaudeTeamsMemoryLimit   string // env: CLAUDE_TEAMS_MEMORY_LIMIT

	// --- Secrets & Credentials ---
	//
	// Per-project secrets (Claude OAuth, git credentials, GitHub/GitLab tokens,
	// RWX token, Mezmo key, AWS credentials, etc.) are declared on project beads
	// via the "secrets" JSON field and auto-wired by applyCommonConfig +
	// secretreconciler. Only controller-level secrets remain here.

	// ClaudeOAuthSecret is the K8s secret containing Claude OAuth credentials (env: CLAUDE_OAUTH_SECRET).
	// Mounted as ~/.claude/.credentials.json in agent pods for Max/Corp accounts.
	// This is a volume mount (not SecretEnv), so it cannot be handled by config beads.
	ClaudeOAuthSecret string

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

	// --- ExternalSecret Reconciliation ---

	// ExternalSecretStoreName is the SecretStore name for auto-reconciled ExternalSecrets
	// (env: EXTERNAL_SECRET_STORE_NAME). Default: "secretstore".
	ExternalSecretStoreName string

	// ExternalSecretStoreKind is the SecretStore kind for auto-reconciled ExternalSecrets
	// (env: EXTERNAL_SECRET_STORE_KIND). Default: "ClusterSecretStore".
	ExternalSecretStoreKind string

	// ExternalSecretRefreshInterval is the refresh interval for auto-reconciled ExternalSecrets
	// (env: EXTERNAL_SECRET_REFRESH_INTERVAL). Default: "15m".
	ExternalSecretRefreshInterval string

	// --- Prewarmed Agent Pool ---
	//
	// Pool size, role, mode, and enabled/disabled are configured per-project
	// via the "prewarmed_pool" JSON field on project beads (see beadsapi.PrewarmedPoolConfig).
	// Only the reconcile interval is controller-level config.

	// PrewarmedPoolInterval is how often the multi-pool reconciler runs
	// (env: PREWARMED_POOL_INTERVAL). Default: 30s.
	PrewarmedPoolInterval time.Duration

	// --- Upgrade Drain ---

	// UpgradeDrainTimeout is how long to wait for an agent to reach idle state
	// after being nudged before force-deleting the pod (env: COOP_UPGRADE_DRAIN_TIMEOUT).
	// Default: 5m.
	UpgradeDrainTimeout time.Duration

	// --- Slack Bridge ---

	// SlackBridgeURL is the URL of the slack-bridge HTTP service (env: SLACK_BRIDGE_URL).
	// Injected as SLACK_BRIDGE_URL in agent pods so gb slack commands can reach the bridge.
	SlackBridgeURL string

	// --- Controller ---

	// LogLevel controls log verbosity: debug, info, warn, error (env: LOG_LEVEL).
	LogLevel string

	// --- Runtime (not from env) ---

	// ProjectCacheMu protects ProjectCache for concurrent read/write access.
	// The periodic sync goroutine writes via refreshProjectCache while the
	// event loop goroutine reads via buildAgentPodSpec.
	ProjectCacheMu sync.RWMutex

	// ProjectCache maps project name → metadata, populated at runtime from project beads
	// in the daemon. Not parsed from env. Protected by ProjectCacheMu.
	ProjectCache map[string]ProjectCacheEntry
}

// ProjectCacheEntry holds project metadata from daemon project beads.
type ProjectCacheEntry struct {
	Prefix        string // e.g., "kd", "bot"
	GitURL        string // e.g., "https://github.com/groblegark/kbeads.git"
	DefaultBranch string // e.g., "main"

	// Per-project pod customization (from project bead labels).
	Image          string // Override agent image for this project
	StorageClass   string // Override PVC storage class
	ServiceAccount string // Override K8s ServiceAccount for this project's agents
	RTKEnabled     bool   // Enable RTK token optimization for this project's agents

	// Tier 1: resource overrides (Kubernetes quantity strings, e.g. "500m", "1Gi").
	// Zero value means "use the global default".
	CPURequest    string
	CPULimit      string
	MemoryRequest string
	MemoryLimit   string

	// EnvOverrides holds extra env vars to inject into agent pods for this project.
	// Applied before controller-level config; pod-level metadata takes precedence.
	EnvOverrides map[string]string

	// Per-project secret overrides (merged with globals at pod creation).
	Secrets []beadsapi.SecretEntry
	// Per-project plain env vars (non-secret config like JIRA_BASE_URL).
	EnvVars []beadsapi.EnvEntry
	// Multi-repo definitions (primary + reference repos).
	Repos []beadsapi.RepoEntry
	// Per-project prewarmed agent pool config (nil = disabled for this project).
	PrewarmedPool *beadsapi.PrewarmedPoolConfig
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
		BeadsE2EHTTPAddr: os.Getenv("BEADS_E2E_HTTP_ADDR"),
		BeadsTokenSecret: os.Getenv("BEADS_TOKEN_SECRET"),

		// NATS Event Bus (passed to agent pods, not used by the controller itself)
		NatsURL:         os.Getenv("NATS_URL"),
		NatsTokenSecret: os.Getenv("NATS_TOKEN_SECRET"),

		// Agent Pods
		CoopImage:          os.Getenv("COOP_IMAGE"),
		CoopServiceAccount: os.Getenv("COOP_SERVICE_ACCOUNT"),
		CoopMaxPods:         envIntOr("COOP_MAX_PODS", 30),
		CoopBurstLimit:      envIntOr("COOP_BURST_LIMIT", 3),
		CoopRateLimitWindow: envDurationOr("COOP_RATE_LIMIT_WINDOW", 5*time.Minute),
		CoopRateLimitMax:    envIntOr("COOP_RATE_LIMIT_MAX", 20),
		CoopCircuitBreakerThreshold: envIntOr("COOP_CIRCUIT_BREAKER_THRESHOLD", 20),
		CoopCircuitBreakerWindow:    envDurationOr("COOP_CIRCUIT_BREAKER_WINDOW", 2*time.Minute),
		CoopCircuitBreakerCooldown:  envDurationOr("COOP_CIRCUIT_BREAKER_COOLDOWN", 5*time.Minute),
		CoopSyncInterval:   envDurationOr("COOP_SYNC_INTERVAL", 60*time.Second),
		AgentStorageClass:  os.Getenv("AGENT_STORAGE_CLASS"),
		ClaudeModel:             os.Getenv("CLAUDE_MODEL"),
		ClaudeTeamsEnabled:      envBoolOr("CLAUDE_TEAMS_ENABLED", false),
		ClaudeTeammateMode:      envOr("CLAUDE_TEAMMATE_MODE", "tmux"),
		ClaudeTeamsMaxTeammates:  envIntOr("CLAUDE_TEAMS_MAX_TEAMMATES", 0),
		ClaudeTeamsCPURequest:    os.Getenv("CLAUDE_TEAMS_CPU_REQUEST"),
		ClaudeTeamsCPULimit:      os.Getenv("CLAUDE_TEAMS_CPU_LIMIT"),
		ClaudeTeamsMemoryRequest: os.Getenv("CLAUDE_TEAMS_MEMORY_REQUEST"),
		ClaudeTeamsMemoryLimit:   os.Getenv("CLAUDE_TEAMS_MEMORY_LIMIT"),

		// Secrets & Credentials (only controller-level; per-project secrets via config beads)
		ClaudeOAuthSecret: os.Getenv("CLAUDE_OAUTH_SECRET"),

		// Coopmux
		CoopmuxURL:         os.Getenv("COOPMUX_URL"),
		CoopmuxTokenSecret: os.Getenv("COOPMUX_TOKEN_SECRET"),

		// Leader Election
		LeaderElection:         envBoolOr("ENABLE_LEADER_ELECTION", false),
		LeaderElectionID:       envOr("LEADER_ELECTION_ID", "agents-leader"),
		LeaderElectionIdentity: envOr("POD_NAME", hostname()),

		// Slack config removed — handled by standalone slack-bridge (bd-8x8fy).

		// Slack Bridge
		SlackBridgeURL: os.Getenv("SLACK_BRIDGE_URL"),

		// Prewarmed Agent Pool (per-project config via project beads; only interval is controller-level)
		PrewarmedPoolInterval: envDurationOr("PREWARMED_POOL_INTERVAL", 30*time.Second),

		// Upgrade Drain
		UpgradeDrainTimeout: envDurationOr("COOP_UPGRADE_DRAIN_TIMEOUT", 5*time.Minute),

		// ExternalSecret Reconciliation
		ExternalSecretStoreName:       envOr("EXTERNAL_SECRET_STORE_NAME", "secretstore"),
		ExternalSecretStoreKind:       envOr("EXTERNAL_SECRET_STORE_KIND", "ClusterSecretStore"),
		ExternalSecretRefreshInterval: envOr("EXTERNAL_SECRET_REFRESH_INTERVAL", "15m"),

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
