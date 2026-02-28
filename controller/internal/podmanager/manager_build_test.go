package podmanager

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/fake"
)

// --- buildPod tests ---

func TestBuildPod_BasicFields(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	pod := m.buildPod(spec)

	if pod.Name != "crew-gasboat-dev-test-1" {
		t.Errorf("pod.Name = %q, want %q", pod.Name, "crew-gasboat-dev-test-1")
	}
	if pod.Namespace != "default" {
		t.Errorf("pod.Namespace = %q, want %q", pod.Namespace, "default")
	}
	if got := pod.Annotations[AnnotationBeadID]; got != "bead-abc" {
		t.Errorf("bead-id annotation = %q, want %q", got, "bead-abc")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Errorf("RestartPolicy = %q, want Always", pod.Spec.RestartPolicy)
	}
}

func TestBuildPod_JobMode(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.Mode = "job"
	pod := m.buildPod(spec)

	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want Never for job mode", pod.Spec.RestartPolicy)
	}
}

func TestBuildPod_SecurityContext(t *testing.T) {
	m := newTestManager()
	pod := m.buildPod(minimalSpec())

	psc := pod.Spec.SecurityContext
	if psc == nil {
		t.Fatal("pod security context is nil")
	}
	if *psc.RunAsUser != AgentUID {
		t.Errorf("RunAsUser = %d, want %d", *psc.RunAsUser, AgentUID)
	}
	if *psc.RunAsGroup != AgentGID {
		t.Errorf("RunAsGroup = %d, want %d", *psc.RunAsGroup, AgentGID)
	}
	if !*psc.RunAsNonRoot {
		t.Error("RunAsNonRoot should be true")
	}
	if *psc.FSGroup != AgentGID {
		t.Errorf("FSGroup = %d, want %d", *psc.FSGroup, AgentGID)
	}
}

func TestBuildPod_TerminationGracePeriod(t *testing.T) {
	m := newTestManager()
	pod := m.buildPod(minimalSpec())

	if pod.Spec.TerminationGracePeriodSeconds == nil {
		t.Fatal("TerminationGracePeriodSeconds is nil")
	}
	if *pod.Spec.TerminationGracePeriodSeconds != 45 {
		t.Errorf("TerminationGracePeriodSeconds = %d, want 45",
			*pod.Spec.TerminationGracePeriodSeconds)
	}
}

func TestBuildPod_ServiceAccount(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.ServiceAccountName = "custom-sa"
	pod := m.buildPod(spec)

	if pod.Spec.ServiceAccountName != "custom-sa" {
		t.Errorf("ServiceAccountName = %q, want %q", pod.Spec.ServiceAccountName, "custom-sa")
	}
}

func TestBuildPod_NodeSelectorAndTolerations(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.NodeSelector = map[string]string{"kubernetes.io/arch": "amd64"}
	spec.Tolerations = []corev1.Toleration{{Key: "gpu", Effect: corev1.TaintEffectNoSchedule}}
	pod := m.buildPod(spec)

	if got := pod.Spec.NodeSelector["kubernetes.io/arch"]; got != "amd64" {
		t.Errorf("NodeSelector[arch] = %q, want amd64", got)
	}
	if len(pod.Spec.Tolerations) != 1 || pod.Spec.Tolerations[0].Key != "gpu" {
		t.Errorf("Tolerations not set correctly: %+v", pod.Spec.Tolerations)
	}
}

func TestBuildPod_NoInitContainerWithoutGitURL(t *testing.T) {
	m := newTestManager()
	pod := m.buildPod(minimalSpec())

	if len(pod.Spec.InitContainers) != 0 {
		t.Errorf("expected no init containers without GitURL, got %d", len(pod.Spec.InitContainers))
	}
}

func TestBuildPod_InitContainerWithGitURL(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.GitURL = "https://github.com/example/repo.git"
	pod := m.buildPod(spec)

	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(pod.Spec.InitContainers))
	}
	ic := pod.Spec.InitContainers[0]
	if ic.Name != InitCloneName {
		t.Errorf("init container name = %q, want %q", ic.Name, InitCloneName)
	}
	if ic.Image != InitCloneImage {
		t.Errorf("init container image = %q, want %q", ic.Image, InitCloneImage)
	}
}

// --- buildContainer tests ---

func TestBuildContainer_PreStopHook(t *testing.T) {
	m := newTestManager()
	c := m.buildContainer(minimalSpec())

	if c.Lifecycle == nil || c.Lifecycle.PreStop == nil {
		t.Fatal("container missing PreStop lifecycle hook")
	}
	if c.Lifecycle.PreStop.Exec == nil {
		t.Fatal("PreStop hook should use Exec action")
	}
	cmd := c.Lifecycle.PreStop.Exec.Command
	if len(cmd) < 3 {
		t.Fatalf("PreStop command too short: %v", cmd)
	}
	if cmd[0] != "sh" || cmd[1] != "-c" {
		t.Errorf("PreStop command prefix = %v, want [sh -c ...]", cmd[:2])
	}
}

func TestBuildContainer_Probes(t *testing.T) {
	m := newTestManager()
	c := m.buildContainer(minimalSpec())

	if c.LivenessProbe == nil {
		t.Error("missing liveness probe")
	}
	if c.ReadinessProbe == nil {
		t.Error("missing readiness probe")
	}
	if c.StartupProbe == nil {
		t.Error("missing startup probe")
	}

	// Verify probe paths.
	for name, probe := range map[string]*corev1.Probe{
		"liveness":  c.LivenessProbe,
		"readiness": c.ReadinessProbe,
		"startup":   c.StartupProbe,
	} {
		if probe.HTTPGet == nil {
			t.Errorf("%s probe should be HTTPGet", name)
			continue
		}
		if probe.HTTPGet.Path != "/api/v1/health" {
			t.Errorf("%s probe path = %q, want /api/v1/health", name, probe.HTTPGet.Path)
		}
	}
}

func TestBuildContainer_Ports(t *testing.T) {
	m := newTestManager()
	c := m.buildContainer(minimalSpec())

	if len(c.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(c.Ports))
	}

	portMap := make(map[string]int32)
	for _, p := range c.Ports {
		portMap[p.Name] = p.ContainerPort
	}
	if portMap["api"] != CoopDefaultPort {
		t.Errorf("api port = %d, want %d", portMap["api"], CoopDefaultPort)
	}
	if portMap["health"] != CoopDefaultHealthPort {
		t.Errorf("health port = %d, want %d", portMap["health"], CoopDefaultHealthPort)
	}
}

func TestBuildContainer_SecurityContext(t *testing.T) {
	m := newTestManager()
	c := m.buildContainer(minimalSpec())

	sc := c.SecurityContext
	if sc == nil {
		t.Fatal("container security context is nil")
	}
	if !*sc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation should be true")
	}
	if *sc.ReadOnlyRootFilesystem {
		t.Error("ReadOnlyRootFilesystem should be false")
	}
	if sc.Capabilities == nil {
		t.Fatal("Capabilities is nil")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Drop = %v, want [ALL]", sc.Capabilities.Drop)
	}
	addCaps := make(map[corev1.Capability]bool)
	for _, c := range sc.Capabilities.Add {
		addCaps[c] = true
	}
	if !addCaps["SETUID"] || !addCaps["SETGID"] {
		t.Errorf("Add caps = %v, want SETUID + SETGID", sc.Capabilities.Add)
	}
}

func TestBuildContainer_DefaultResources(t *testing.T) {
	m := newTestManager()
	c := m.buildContainer(minimalSpec())

	cpuReq := c.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != DefaultCPURequest {
		t.Errorf("CPU request = %s, want %s", cpuReq.String(), DefaultCPURequest)
	}
	memLimit := c.Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != DefaultMemoryLimit {
		t.Errorf("Memory limit = %s, want %s", memLimit.String(), DefaultMemoryLimit)
	}
}

func TestBuildContainer_CustomResources(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("500m"),
		},
	}
	c := m.buildContainer(spec)

	cpuReq := c.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "500m" {
		t.Errorf("CPU request = %s, want 500m", cpuReq.String())
	}
}

// --- buildEnvVars tests ---

func TestBuildEnvVars_StandardVars(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	envVars := m.buildEnvVars(spec)

	envMap := make(map[string]corev1.EnvVar)
	for _, ev := range envVars {
		envMap[ev.Name] = ev
	}

	checks := map[string]string{
		"BOAT_ROLE":    "dev",
		"BOAT_PROJECT": "gasboat",
		"BOAT_AGENT":   "test-1",
		"BOAT_MODE":    "crew",
		"HOME":         "/home/agent",
		"BEADS_ACTOR":  "test-1",
		"KD_ACTOR":     "test-1",
		"KD_AGENT_ID":  "bead-abc",
	}
	for name, want := range checks {
		ev, ok := envMap[name]
		if !ok {
			t.Errorf("missing env var %s", name)
			continue
		}
		if ev.Value != want {
			t.Errorf("env %s = %q, want %q", name, ev.Value, want)
		}
	}

	// POD_IP should use downward API.
	podIP, ok := envMap["POD_IP"]
	if !ok {
		t.Fatal("missing POD_IP env var")
	}
	if podIP.ValueFrom == nil || podIP.ValueFrom.FieldRef == nil {
		t.Error("POD_IP should use fieldRef downward API")
	}
}

func TestBuildEnvVars_SessionResume(t *testing.T) {
	m := newTestManager()

	// Without workspace storage — no BOAT_SESSION_RESUME.
	spec := minimalSpec()
	envVars := m.buildEnvVars(spec)
	for _, ev := range envVars {
		if ev.Name == "BOAT_SESSION_RESUME" {
			t.Error("BOAT_SESSION_RESUME should not be set without WorkspaceStorage")
		}
	}

	// With workspace storage — BOAT_SESSION_RESUME=1.
	spec.WorkspaceStorage = &WorkspaceStorageSpec{Size: "5Gi"}
	envVars = m.buildEnvVars(spec)
	found := false
	for _, ev := range envVars {
		if ev.Name == "BOAT_SESSION_RESUME" {
			found = true
			if ev.Value != "1" {
				t.Errorf("BOAT_SESSION_RESUME = %q, want %q", ev.Value, "1")
			}
		}
	}
	if !found {
		t.Error("BOAT_SESSION_RESUME not set with WorkspaceStorage")
	}
}

func TestBuildEnvVars_CustomEnv(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.Env = map[string]string{"CUSTOM_KEY": "custom_value"}
	envVars := m.buildEnvVars(spec)

	found := false
	for _, ev := range envVars {
		if ev.Name == "CUSTOM_KEY" && ev.Value == "custom_value" {
			found = true
		}
	}
	if !found {
		t.Error("custom env var not found")
	}
}

func TestBuildEnvVars_SecretEnv(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.SecretEnv = []SecretEnvSource{
		{EnvName: "MY_SECRET", SecretName: "my-secret", SecretKey: "key"},
	}
	envVars := m.buildEnvVars(spec)

	found := false
	for _, ev := range envVars {
		if ev.Name == "MY_SECRET" {
			found = true
			if ev.ValueFrom == nil || ev.ValueFrom.SecretKeyRef == nil {
				t.Error("MY_SECRET should use SecretKeyRef")
			} else if ev.ValueFrom.SecretKeyRef.Name != "my-secret" {
				t.Errorf("secret name = %q, want my-secret", ev.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	if !found {
		t.Error("secret env var not found")
	}
}

func TestBuildEnvVars_DaemonToken(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.DaemonTokenSecret = "daemon-token-secret"
	envVars := m.buildEnvVars(spec)

	found := false
	for _, ev := range envVars {
		if ev.Name == "BEADS_DAEMON_TOKEN" {
			found = true
			if ev.ValueFrom == nil || ev.ValueFrom.SecretKeyRef == nil {
				t.Error("BEADS_DAEMON_TOKEN should use SecretKeyRef")
			} else {
				ref := ev.ValueFrom.SecretKeyRef
				if ref.Name != "daemon-token-secret" || ref.Key != "token" {
					t.Errorf("secret ref = %s/%s, want daemon-token-secret/token", ref.Name, ref.Key)
				}
			}
		}
	}
	if !found {
		t.Error("BEADS_DAEMON_TOKEN env var not found")
	}
}

// --- buildResources tests ---

func TestBuildResources_Default(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{}

	res := mgr.buildResources(spec)

	cpuReq := res.Requests[corev1.ResourceCPU]
	if cpuReq.String() != DefaultCPURequest {
		t.Errorf("default CPU request = %s, want %s", cpuReq.String(), DefaultCPURequest)
	}
	memLimit := res.Limits[corev1.ResourceMemory]
	if memLimit.String() != DefaultMemoryLimit {
		t.Errorf("default memory limit = %s, want %s", memLimit.String(), DefaultMemoryLimit)
	}
}

func TestBuildResources_Custom(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	custom := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("500m"),
		},
	}
	spec := AgentPodSpec{Resources: custom}

	res := mgr.buildResources(spec)
	cpuReq := res.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "500m" {
		t.Errorf("custom CPU request = %s, want 500m", cpuReq.String())
	}
}

// ── ApplyDefaults ──────────────────────────────────────────────────────────

func TestApplyDefaults_Nil(t *testing.T) {
	spec := minimalSpec()
	original := spec.Image
	ApplyDefaults(&spec, nil)
	if spec.Image != original {
		t.Error("nil defaults should not change spec")
	}
}

func TestApplyDefaults_FillEmpty(t *testing.T) {
	spec := AgentPodSpec{}
	defaults := &PodDefaults{
		Image:              "default-image:latest",
		ServiceAccountName: "default-sa",
		ConfigMapName:      "default-cm",
		NodeSelector:       map[string]string{"zone": "us-east-1a"},
		Tolerations:        []corev1.Toleration{{Key: "spot"}},
	}
	ApplyDefaults(&spec, defaults)

	if spec.Image != "default-image:latest" {
		t.Errorf("Image = %q, want default-image:latest", spec.Image)
	}
	if spec.ServiceAccountName != "default-sa" {
		t.Errorf("ServiceAccountName = %q, want default-sa", spec.ServiceAccountName)
	}
	if spec.ConfigMapName != "default-cm" {
		t.Errorf("ConfigMapName = %q, want default-cm", spec.ConfigMapName)
	}
	if spec.NodeSelector["zone"] != "us-east-1a" {
		t.Error("NodeSelector not applied")
	}
	if len(spec.Tolerations) != 1 {
		t.Error("Tolerations not applied")
	}
}

func TestApplyDefaults_SpecTakesPrecedence(t *testing.T) {
	spec := AgentPodSpec{
		Image:              "custom:v1",
		ServiceAccountName: "custom-sa",
		Env:                map[string]string{"KEY": "spec-value"},
	}
	defaults := &PodDefaults{
		Image:              "default:latest",
		ServiceAccountName: "default-sa",
		Env:                map[string]string{"KEY": "default-value", "OTHER": "default-other"},
	}
	ApplyDefaults(&spec, defaults)

	if spec.Image != "custom:v1" {
		t.Errorf("Image = %q, want custom:v1 (spec should win)", spec.Image)
	}
	if spec.ServiceAccountName != "custom-sa" {
		t.Errorf("ServiceAccountName = %q, want custom-sa (spec should win)", spec.ServiceAccountName)
	}
	if spec.Env["KEY"] != "spec-value" {
		t.Errorf("Env[KEY] = %q, want spec-value (spec should win)", spec.Env["KEY"])
	}
	if spec.Env["OTHER"] != "default-other" {
		t.Errorf("Env[OTHER] = %q, want default-other (should be merged from defaults)", spec.Env["OTHER"])
	}
}

func TestApplyDefaults_EnvMerge_NilSpecEnv(t *testing.T) {
	spec := AgentPodSpec{}
	defaults := &PodDefaults{
		Env: map[string]string{"A": "1", "B": "2"},
	}
	ApplyDefaults(&spec, defaults)

	if spec.Env["A"] != "1" || spec.Env["B"] != "2" {
		t.Errorf("Env not merged: %v", spec.Env)
	}
}

func TestApplyDefaults_SecretEnvDedup(t *testing.T) {
	spec := AgentPodSpec{
		SecretEnv: []SecretEnvSource{
			{EnvName: "TOKEN", SecretName: "spec-secret", SecretKey: "k"},
		},
	}
	defaults := &PodDefaults{
		SecretEnv: []SecretEnvSource{
			{EnvName: "TOKEN", SecretName: "default-secret", SecretKey: "k"},
			{EnvName: "OTHER", SecretName: "other-secret", SecretKey: "v"},
		},
	}
	ApplyDefaults(&spec, defaults)

	if len(spec.SecretEnv) != 2 {
		t.Fatalf("SecretEnv length = %d, want 2", len(spec.SecretEnv))
	}
	// TOKEN should keep spec value.
	if spec.SecretEnv[0].SecretName != "spec-secret" {
		t.Errorf("TOKEN secret = %q, want spec-secret", spec.SecretEnv[0].SecretName)
	}
	// OTHER should be appended.
	if spec.SecretEnv[1].EnvName != "OTHER" {
		t.Errorf("SecretEnv[1] = %q, want OTHER", spec.SecretEnv[1].EnvName)
	}
}

func TestApplyDefaults_WorkspaceStorage(t *testing.T) {
	spec := AgentPodSpec{}
	defaults := &PodDefaults{
		WorkspaceStorage: &WorkspaceStorageSpec{Size: "20Gi"},
	}
	ApplyDefaults(&spec, defaults)
	if spec.WorkspaceStorage == nil {
		t.Fatal("WorkspaceStorage should be applied from defaults")
	}
	if spec.WorkspaceStorage.Size != "20Gi" {
		t.Errorf("Size = %q, want 20Gi", spec.WorkspaceStorage.Size)
	}

	// Spec already has WorkspaceStorage — should not be overwritten.
	spec2 := AgentPodSpec{
		WorkspaceStorage: &WorkspaceStorageSpec{Size: "5Gi"},
	}
	ApplyDefaults(&spec2, defaults)
	if spec2.WorkspaceStorage.Size != "5Gi" {
		t.Errorf("Size = %q, want 5Gi (spec should win)", spec2.WorkspaceStorage.Size)
	}
}

// ── DefaultPodDefaults ─────────────────────────────────────────────────────

func TestDefaultPodDefaults_Crew(t *testing.T) {
	defaults := DefaultPodDefaults("crew")
	if defaults.WorkspaceStorage == nil {
		t.Fatal("crew mode should have WorkspaceStorage")
	}
	if defaults.WorkspaceStorage.Size != "10Gi" {
		t.Errorf("Size = %q, want 10Gi", defaults.WorkspaceStorage.Size)
	}
	if defaults.Resources == nil {
		t.Fatal("defaults should have Resources")
	}
	if defaults.NodeSelector["kubernetes.io/arch"] != "amd64" {
		t.Error("missing amd64 node selector")
	}
	if defaults.Affinity == nil {
		t.Fatal("defaults should have Affinity")
	}
}

func TestDefaultPodDefaults_Job(t *testing.T) {
	defaults := DefaultPodDefaults("job")
	if defaults.WorkspaceStorage != nil {
		t.Error("job mode should not have WorkspaceStorage")
	}
	if defaults.Resources == nil {
		t.Fatal("defaults should have Resources")
	}
}
