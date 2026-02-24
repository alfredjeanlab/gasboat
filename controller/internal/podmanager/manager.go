// Package podmanager handles K8s pod CRUD for Gasboat agents.
// It translates beads lifecycle decisions into pod create/delete operations.
// The pod manager never makes lifecycle decisions — it executes them.
package podmanager

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

const (
	// Label keys for agent pods.
	LabelApp     = "app.kubernetes.io/name"
	LabelProject = "gasboat.io/project"
	LabelRole    = "gasboat.io/role"
	LabelAgent   = "gasboat.io/agent"
	LabelMode    = "gasboat.io/mode"

	// AnnotationBeadID is the canonical bead ID for this pod. When set,
	// the status reporter uses it instead of constructing an ID from labels.
	// This is required for pods whose bead ID doesn't follow the
	// {mode}-{project}-{role}-{agent} pattern.
	AnnotationBeadID = "gasboat.io/bead-id"

	// LabelAppValue is the app label value for all gasboat pods.
	LabelAppValue = "gasboat"

	// Default resource values.
	DefaultCPURequest    = "2"
	DefaultCPULimit      = "4"
	DefaultMemoryRequest = "1Gi"
	DefaultMemoryLimit   = "4Gi"

	// Volume names.
	VolumeWorkspace   = "workspace"
	VolumeTmp         = "tmp"
	VolumeBeadsConfig = "beads-config"
	VolumeClaudeCreds = "claude-creds"

	// Mount paths.
	MountWorkspace   = "/home/agent/bot"
	MountTmp         = "/tmp"
	MountBeadsConfig = "/etc/agent-pod"
	MountClaudeCreds = "/tmp/claude-credentials"

	// Session persistence: state dir on the workspace PVC.
	MountStateDir = "/home/agent/bot/.state"

	// Claude state: subPath mount so .claude/ is backed by the workspace PVC.
	MountClaudeState   = "/home/agent/.claude"
	SubPathClaudeState = ".state/claude"

	// Container constants.
	ContainerName = "agent"
	AgentUID      = int64(1000)
	AgentGID      = int64(1000)

	// Init container constants.
	InitCloneName  = "init-clone"
	InitCloneImage = "public.ecr.aws/docker/library/alpine:3.20"

	// Coop port constants.
	CoopDefaultPort       = 8080
	CoopDefaultHealthPort = 9090
)

// SecretEnvSource maps a K8s Secret key to a pod environment variable.
type SecretEnvSource struct {
	EnvName    string // env var name in the pod
	SecretName string // K8s Secret name
	SecretKey  string // key within the Secret
}

// AgentPodSpec describes the desired pod for an agent.
type AgentPodSpec struct {
	Project   string
	Mode      string
	Role      string // functional role from role bead (e.g., "devops", "qa")
	AgentName string
	BeadID    string // canonical bead ID (written to gasboat.io/bead-id annotation)
	Image     string
	Namespace string
	Env       map[string]string

	// Resources sets compute requests/limits. If nil, defaults are used.
	Resources *corev1.ResourceRequirements

	// SecretEnv injects environment variables from K8s Secrets.
	// Used for git credentials and other secret env vars.
	SecretEnv []SecretEnvSource

	// ConfigMapName is the name of a ConfigMap to mount at MountBeadsConfig.
	// Contains agent configuration (role, project, daemon connection, etc.).
	ConfigMapName string

	// ServiceAccountName for the pod. Empty uses the namespace default.
	ServiceAccountName string

	// NodeSelector constrains pod scheduling.
	NodeSelector map[string]string

	// Tolerations for the pod.
	Tolerations []corev1.Toleration

	// Affinity rules for pod scheduling (e.g. prefer compute-optimized nodes).
	Affinity *corev1.Affinity

	// WorkspaceStorage configures a PVC for persistent workspace.
	// If nil, an EmptyDir is used.
	WorkspaceStorage *WorkspaceStorageSpec

	// CredentialsSecret is the K8s Secret name containing Claude OAuth credentials.
	// The "credentials.json" key is mounted at ~/.claude/.credentials.json.
	// Used for Claude Max/Corp accounts (no API key needed).
	CredentialsSecret string

	// DaemonTokenSecret is the K8s Secret name containing BEADS_DAEMON_TOKEN.
	// The "token" key is injected as the BEADS_DAEMON_TOKEN env var.
	DaemonTokenSecret string

	// GitURL is the upstream repository URL (e.g., "https://github.com/...").
	// When set, an init container clones from this URL into the workspace.
	GitURL string

	// GitDefaultBranch is the branch to checkout after cloning (default: "main").
	GitDefaultBranch string

	// GitCredentialsSecret is the K8s Secret name containing git credentials.
	// The "username" and "token" keys are injected as env vars in the init-clone
	// container for authenticated git clone of private repositories.
	GitCredentialsSecret string
}

// WorkspaceStorageSpec configures a PVC-backed workspace volume.
type WorkspaceStorageSpec struct {
	// ClaimName is the PVC name. If empty, derived from pod name.
	ClaimName string

	// Size is the requested storage (e.g., "10Gi").
	Size string

	// StorageClassName is the storage class (e.g., "gp3").
	StorageClassName string
}

// PodName returns the canonical pod name: {mode}-{project}-{role}-{name}.
func (s *AgentPodSpec) PodName() string {
	return fmt.Sprintf("%s-%s-%s-%s", s.Mode, s.Project, s.Role, s.AgentName)
}

// Labels returns the standard label set for this agent pod.
func (s *AgentPodSpec) Labels() map[string]string {
	return map[string]string{
		LabelApp:     LabelAppValue,
		LabelProject: s.Project,
		LabelMode:    s.Mode,
		LabelRole:    s.Role,
		LabelAgent:   s.AgentName,
	}
}

// Manager creates, deletes, and lists agent pods in K8s.
type Manager interface {
	CreateAgentPod(ctx context.Context, spec AgentPodSpec) error
	DeleteAgentPod(ctx context.Context, name, namespace string) error
	ListAgentPods(ctx context.Context, namespace string, labelSelector map[string]string) ([]corev1.Pod, error)
	GetAgentPod(ctx context.Context, name, namespace string) (*corev1.Pod, error)
}

// K8sManager implements Manager using client-go.
type K8sManager struct {
	client kubernetes.Interface
	logger *slog.Logger
}

// New creates a pod manager backed by a K8s client.
func New(client kubernetes.Interface, logger *slog.Logger) *K8sManager {
	return &K8sManager{client: client, logger: logger}
}

// CreateAgentPod creates a pod for the given agent spec.
// If the spec includes WorkspaceStorage, a PVC is created first (idempotent).
func (m *K8sManager) CreateAgentPod(ctx context.Context, spec AgentPodSpec) error {
	// Ensure PVC exists before creating the pod.
	if spec.WorkspaceStorage != nil {
		if err := m.ensurePVC(ctx, spec); err != nil {
			return fmt.Errorf("ensuring workspace PVC: %w", err)
		}
	}

	pod := m.buildPod(spec)
	m.logger.Info("creating agent pod",
		"pod", pod.Name, "project", spec.Project, "role", spec.Role, "agent", spec.AgentName)

	_, err := m.client.CoreV1().Pods(spec.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating pod %s: %w", pod.Name, err)
	}
	return nil
}

// ensurePVC creates the workspace PVC if it does not already exist.
func (m *K8sManager) ensurePVC(ctx context.Context, spec AgentPodSpec) error {
	ws := spec.WorkspaceStorage
	claimName := ws.ClaimName
	if claimName == "" {
		claimName = spec.PodName() + "-workspace"
	}

	size := ws.Size
	if size == "" {
		size = "10Gi"
	}
	storageClass := ws.StorageClassName

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimName,
			Namespace: spec.Namespace,
			Labels:    spec.Labels(),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
		},
	}
	if storageClass != "" {
		pvc.Spec.StorageClassName = &storageClass
	}

	_, err := m.client.CoreV1().PersistentVolumeClaims(spec.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		m.logger.Info("workspace PVC already exists", "pvc", claimName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating PVC %s: %w", claimName, err)
	}
	m.logger.Info("created workspace PVC", "pvc", claimName, "size", size, "storageClass", storageClass)
	return nil
}

// DeleteAgentPod deletes a pod by name and namespace.
func (m *K8sManager) DeleteAgentPod(ctx context.Context, name, namespace string) error {
	m.logger.Info("deleting agent pod", "pod", name, "namespace", namespace)
	return m.client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// ListAgentPods lists pods matching the given labels.
func (m *K8sManager) ListAgentPods(ctx context.Context, namespace string, labelSelector map[string]string) ([]corev1.Pod, error) {
	sel := labels.Set(labelSelector).String()
	list, err := m.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: sel,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods with selector %s: %w", sel, err)
	}
	return list.Items, nil
}

// GetAgentPod gets a single pod by name.
func (m *K8sManager) GetAgentPod(ctx context.Context, name, namespace string) (*corev1.Pod, error) {
	return m.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (m *K8sManager) buildPod(spec AgentPodSpec) *corev1.Pod {
	container := m.buildContainer(spec)
	volumes := m.buildVolumes(spec)

	containers := []corev1.Container{container}

	var initContainers []corev1.Container
	if ic := m.buildInitCloneContainer(spec); ic != nil {
		initContainers = append(initContainers, *ic)
	}

	podSpec := corev1.PodSpec{
		InitContainers: initContainers,
		Containers:     containers,
		Volumes:        volumes,
		RestartPolicy:  restartPolicyForMode(spec.Mode),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:    intPtr(AgentUID),
			RunAsGroup:   intPtr(AgentGID),
			RunAsNonRoot: boolPtr(true),
			FSGroup:      intPtr(AgentGID),
		},
	}

	if spec.ServiceAccountName != "" {
		podSpec.ServiceAccountName = spec.ServiceAccountName
	}
	if len(spec.NodeSelector) > 0 {
		podSpec.NodeSelector = spec.NodeSelector
	}
	if len(spec.Tolerations) > 0 {
		podSpec.Tolerations = spec.Tolerations
	}
	if spec.Affinity != nil {
		podSpec.Affinity = spec.Affinity
	}

	// Use a 30s termination grace period for all modes.
	gracePeriod := int64(30)
	podSpec.TerminationGracePeriodSeconds = &gracePeriod

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.PodName(),
			Namespace: spec.Namespace,
			Labels:    spec.Labels(),
			Annotations: map[string]string{
				AnnotationBeadID: spec.BeadID,
			},
		},
		Spec: podSpec,
	}
}

// buildContainer constructs the agent container with env vars, resources,
// volume mounts, and security context.
func (m *K8sManager) buildContainer(spec AgentPodSpec) corev1.Container {
	envVars := m.buildEnvVars(spec)
	mounts := m.buildVolumeMounts(spec)
	resources := m.buildResources(spec)

	c := corev1.Container{
		Name:            ContainerName,
		Image:           spec.Image,
		Env:             envVars,
		Resources:       resources,
		VolumeMounts:    mounts,
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{
			// Allow privilege escalation so agents can use sudo to install
			// packages at runtime. The agent image ships with a NOPASSWD
			// sudoers entry for the agent user.
			AllowPrivilegeEscalation: boolPtr(true),
			ReadOnlyRootFilesystem:   boolPtr(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
				Add:  []corev1.Capability{"SETUID", "SETGID"},
			},
		},
	}

	// Agent image has coop as its entrypoint (PID 1).
	// Pass port flags so coop binds to the expected ports.
	c.Args = []string{
		"--port", fmt.Sprintf("%d", CoopDefaultPort),
		"--health-port", fmt.Sprintf("%d", CoopDefaultHealthPort),
	}

	// Use HTTP probes against coop's health endpoint and expose ports.
	c.Ports = []corev1.ContainerPort{
		{Name: "api", ContainerPort: CoopDefaultPort},
		{Name: "health", ContainerPort: CoopDefaultHealthPort},
	}
	c.LivenessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/api/v1/health",
				Port: intstr.FromString("health"),
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       15,
		FailureThreshold:    3,
	}
	c.ReadinessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/api/v1/health",
				Port: intstr.FromString("health"),
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       5,
	}
	c.StartupProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/api/v1/health",
				Port: intstr.FromString("health"),
			},
		},
		FailureThreshold: 60,
		PeriodSeconds:    5,
	}

	return c
}

// buildEnvVars constructs environment variables from plain values and secret references.
func (m *K8sManager) buildEnvVars(spec AgentPodSpec) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{Name: "BOAT_ROLE", Value: spec.Role},
		{Name: "BOAT_PROJECT", Value: spec.Project},
		{Name: "BOAT_AGENT", Value: spec.AgentName},
		{Name: "BOAT_MODE", Value: spec.Mode},
		{Name: "HOME", Value: "/home/agent"},
		// Session persistence: point XDG_STATE_HOME to the PVC so Claude
		// session logs and coop session artifacts survive pod restarts.
		{Name: "XDG_STATE_HOME", Value: MountStateDir},
		// POD_IP via downward API — used by coop to advertise its reachable URL
		// to coopmux (COOP_BROKER_URL registration).
		{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
		}},
	}

	// Enable session resume for persistent roles (those with workspace PVC).
	if spec.WorkspaceStorage != nil {
		envVars = append(envVars, corev1.EnvVar{Name: "BOAT_SESSION_RESUME", Value: "1"})
	}

	// All agents get BEADS_ACTOR, GIT_AUTHOR_NAME, BEADS_AGENT_NAME, and
	// BOAT_AGENT_BEAD_ID (the agent's own bead, used by prime.sh to look up
	// hook_bead and instructions without a list+filter round-trip).
	// KD_AGENT_ID and KD_ACTOR are used by `kd bus emit` for gate identity.
	envVars = append(envVars,
		corev1.EnvVar{Name: "BEADS_ACTOR", Value: spec.AgentName},
		corev1.EnvVar{Name: "KD_ACTOR", Value: spec.AgentName},
		corev1.EnvVar{Name: "KD_AGENT_ID", Value: spec.BeadID},
		corev1.EnvVar{Name: "GIT_AUTHOR_NAME", Value: spec.AgentName},
		corev1.EnvVar{Name: "BEADS_AGENT_NAME", Value: fmt.Sprintf("%s/%s", spec.Project, spec.AgentName)},
		corev1.EnvVar{Name: "BOAT_AGENT_BEAD_ID", Value: spec.BeadID},
	)

	// Add plain env vars from spec.
	for k, v := range spec.Env {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}

	// Add secret-sourced env vars.
	for _, se := range spec.SecretEnv {
		envVars = append(envVars, corev1.EnvVar{
			Name: se.EnvName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: se.SecretName},
					Key:                  se.SecretKey,
				},
			},
		})
	}

	// Daemon token from secret for agent→daemon authentication.
	if spec.DaemonTokenSecret != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name: "BEADS_DAEMON_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: spec.DaemonTokenSecret},
					Key:                  "token",
				},
			},
		})
	}

	return envVars
}

// buildResources returns resource requirements. Uses spec.Resources if provided,
// otherwise falls back to defaults.
func (m *K8sManager) buildResources(spec AgentPodSpec) corev1.ResourceRequirements {
	if spec.Resources != nil {
		return *spec.Resources
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(DefaultCPURequest),
			corev1.ResourceMemory: resource.MustParse(DefaultMemoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(DefaultCPULimit),
			corev1.ResourceMemory: resource.MustParse(DefaultMemoryLimit),
		},
	}
}

// buildVolumes returns the volumes for the pod based on role.
func (m *K8sManager) buildVolumes(spec AgentPodSpec) []corev1.Volume {
	var volumes []corev1.Volume

	// Workspace volume: PVC for persistent roles, EmptyDir for ephemeral.
	if spec.WorkspaceStorage != nil {
		claimName := spec.WorkspaceStorage.ClaimName
		if claimName == "" {
			claimName = spec.PodName() + "-workspace"
		}
		volumes = append(volumes, corev1.Volume{
			Name: VolumeWorkspace,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
				},
			},
		})
	} else {
		volumes = append(volumes, corev1.Volume{
			Name: VolumeWorkspace,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	// Tmp volume: always EmptyDir.
	volumes = append(volumes, corev1.Volume{
		Name: VolumeTmp,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})

	// Beads config volume: ConfigMap mount if specified.
	if spec.ConfigMapName != "" {
		volumes = append(volumes, corev1.Volume{
			Name: VolumeBeadsConfig,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: spec.ConfigMapName},
				},
			},
		})
	}

	// Claude credentials volume: Secret mount for OAuth token.
	if spec.CredentialsSecret != "" {
		volumes = append(volumes, corev1.Volume{
			Name: VolumeClaudeCreds,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: spec.CredentialsSecret,
				},
			},
		})
	}

	return volumes
}

// buildVolumeMounts returns the volume mounts for the agent container.
func (m *K8sManager) buildVolumeMounts(spec AgentPodSpec) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{Name: VolumeWorkspace, MountPath: MountWorkspace},
		{Name: VolumeTmp, MountPath: MountTmp},
	}

	if spec.ConfigMapName != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      VolumeBeadsConfig,
			MountPath: MountBeadsConfig,
			ReadOnly:  true,
		})
	}

	// Claude state: mount workspace subPath at ~/.claude so Claude Code's
	// persistent memory (.claude/projects/*.jsonl) survives pod recreation.
	// Only for roles with persistent workspace storage (bd-48ary).
	if spec.WorkspaceStorage != nil {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      VolumeWorkspace,
			MountPath: MountClaudeState,
			SubPath:   SubPathClaudeState,
		})
	}

	// Claude credentials: mount secret to staging dir; entrypoint copies to PVC.
	if spec.CredentialsSecret != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      VolumeClaudeCreds,
			MountPath: MountClaudeCreds,
			ReadOnly:  true,
		})
	}

	return mounts
}

// restartPolicyForMode returns the appropriate restart policy.
// Jobs are one-shot (Never); crew mode restarts on failure.

// buildInitCloneContainer creates an init container that clones the project's repo
// into the workspace from GitURL.
// Returns nil if no clone source is configured.
func (m *K8sManager) buildInitCloneContainer(spec AgentPodSpec) *corev1.Container {
	if spec.GitURL == "" {
		return nil
	}

	branch := spec.GitDefaultBranch
	if branch == "" {
		branch = "main"
	}

	script := fmt.Sprintf(`set -e
apk add --no-cache git
git config --global --add safe.directory '%s/%s/work'
WORK_DIR="%s/%s/work"
if [ -d "$WORK_DIR/.git" ]; then
  echo "Repo already cloned, fetching updates..."
  cd "$WORK_DIR"
  git fetch --all --prune
  git checkout %s
  git pull --ff-only || true
else
  echo "Cloning from %s..."
  mkdir -p "$(dirname "$WORK_DIR")"
  git clone -b %s %s "$WORK_DIR"
  cd "$WORK_DIR"
fi
`, MountWorkspace, spec.Project, MountWorkspace, spec.Project, branch, spec.GitURL, branch, spec.GitURL)

	// Configure git identity from agent env vars.
	script += fmt.Sprintf(`git config user.name "%s"
git config user.email "%s@gasboat"
`, spec.AgentName, spec.AgentName)

	// Run init container as root so apk can install git, then chown
	// the workspace to the agent UID/GID so the main container can write.
	script += fmt.Sprintf("chown -R %d:%d \"%s/%s\"\n", AgentUID, AgentGID, MountWorkspace, spec.Project)

	// Prepend git credential helper setup if credentials are available.
	// This allows cloning private repos in the init container.
	if spec.GitCredentialsSecret != "" {
		credSetup := `# Configure git credentials for private repos
if [ -n "$GIT_USERNAME" ] && [ -n "$GIT_TOKEN" ]; then
  git config --global credential.helper store
  echo "https://${GIT_USERNAME}:${GIT_TOKEN}@github.com" > /tmp/.git-credentials
  git config --global credential.helper 'store --file=/tmp/.git-credentials'
fi
`
		script = credSetup + script
	}

	// Build env vars for the init container.
	var initEnv []corev1.EnvVar
	if spec.GitCredentialsSecret != "" {
		initEnv = append(initEnv,
			corev1.EnvVar{
				Name: "GIT_USERNAME",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.GitCredentialsSecret},
						Key:                  "username",
					},
				},
			},
			corev1.EnvVar{
				Name: "GIT_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.GitCredentialsSecret},
						Key:                  "token",
					},
				},
			},
		)
	}

	runAsRoot := int64(0)
	runAsNonRoot := false
	return &corev1.Container{
		Name:            InitCloneName,
		Image:           InitCloneImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/bin/sh", "-c", script},
		Env:             initEnv,
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:    &runAsRoot,
			RunAsNonRoot: &runAsNonRoot,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: VolumeWorkspace, MountPath: MountWorkspace},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}
}

func restartPolicyForMode(mode string) corev1.RestartPolicy {
	if mode == "job" {
		return corev1.RestartPolicyNever
	}
	return corev1.RestartPolicyAlways
}

func intPtr(i int64) *int64 { return &i }
func boolPtr(b bool) *bool  { return &b }
