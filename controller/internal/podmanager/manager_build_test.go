package podmanager

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/fake"
)

// --- buildPod tests ---

func TestBuildPod_SecurityContext(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image: "img:v1", Namespace: "ns",
	}

	pod := mgr.buildPod(spec)

	sc := pod.Spec.SecurityContext
	if sc == nil {
		t.Fatal("expected pod security context")
	}
	if *sc.RunAsUser != AgentUID {
		t.Errorf("expected RunAsUser %d, got %d", AgentUID, *sc.RunAsUser)
	}
	if *sc.RunAsGroup != AgentGID {
		t.Errorf("expected RunAsGroup %d, got %d", AgentGID, *sc.RunAsGroup)
	}
	if !*sc.RunAsNonRoot {
		t.Error("expected RunAsNonRoot true")
	}
	if *sc.FSGroup != AgentGID {
		t.Errorf("expected FSGroup %d, got %d", AgentGID, *sc.FSGroup)
	}
}

func TestBuildPod_TerminationGracePeriod(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image: "img:v1", Namespace: "ns",
	}

	pod := mgr.buildPod(spec)

	if pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds != 30 {
		t.Error("expected 30s termination grace period")
	}
}

func TestBuildPod_ServiceAccountName(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image: "img:v1", Namespace: "ns", ServiceAccountName: "agent-sa",
	}

	pod := mgr.buildPod(spec)
	if pod.Spec.ServiceAccountName != "agent-sa" {
		t.Errorf("expected ServiceAccountName agent-sa, got %s", pod.Spec.ServiceAccountName)
	}
}

func TestBuildPod_NodeSelectorAndTolerations(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image:        "img:v1",
		Namespace:    "ns",
		NodeSelector: map[string]string{"node-type": "gpu"},
		Tolerations: []corev1.Toleration{
			{Key: "gpu", Operator: corev1.TolerationOpExists},
		},
	}

	pod := mgr.buildPod(spec)
	if pod.Spec.NodeSelector["node-type"] != "gpu" {
		t.Error("expected node-type=gpu in NodeSelector")
	}
	if len(pod.Spec.Tolerations) != 1 {
		t.Errorf("expected 1 toleration, got %d", len(pod.Spec.Tolerations))
	}
}

func TestBuildPod_NoInitContainerWithoutGitURL(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image: "img:v1", Namespace: "ns",
	}

	pod := mgr.buildPod(spec)
	if len(pod.Spec.InitContainers) != 0 {
		t.Errorf("expected no init containers without GitURL, got %d", len(pod.Spec.InitContainers))
	}
}

func TestBuildPod_InitContainerWithGitURL(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image: "img:v1", Namespace: "ns",
		GitURL: "https://github.com/org/repo.git",
	}

	pod := mgr.buildPod(spec)
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(pod.Spec.InitContainers))
	}
	if pod.Spec.InitContainers[0].Name != InitCloneName {
		t.Errorf("expected init container name %s, got %s", InitCloneName, pod.Spec.InitContainers[0].Name)
	}
}

// --- buildContainer tests ---

func TestBuildContainer_Ports(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image: "img:v1",
	}

	c := mgr.buildContainer(spec)

	if len(c.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(c.Ports))
	}

	portMap := map[string]int32{}
	for _, p := range c.Ports {
		portMap[p.Name] = p.ContainerPort
	}
	if portMap["api"] != int32(CoopDefaultPort) {
		t.Errorf("expected api port %d, got %d", CoopDefaultPort, portMap["api"])
	}
	if portMap["health"] != int32(CoopDefaultHealthPort) {
		t.Errorf("expected health port %d, got %d", CoopDefaultHealthPort, portMap["health"])
	}
}

func TestBuildContainer_Probes(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image: "img:v1",
	}

	c := mgr.buildContainer(spec)

	if c.LivenessProbe == nil {
		t.Error("expected liveness probe")
	}
	if c.ReadinessProbe == nil {
		t.Error("expected readiness probe")
	}
	if c.StartupProbe == nil {
		t.Error("expected startup probe")
	}
	if c.LivenessProbe.HTTPGet.Path != "/api/v1/health" {
		t.Errorf("expected probe path /api/v1/health, got %s", c.LivenessProbe.HTTPGet.Path)
	}
}

func TestBuildContainer_SecurityContext(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Image: "img:v1",
	}

	c := mgr.buildContainer(spec)

	sc := c.SecurityContext
	if sc == nil {
		t.Fatal("expected container security context")
	}
	if !*sc.AllowPrivilegeEscalation {
		t.Error("expected AllowPrivilegeEscalation true")
	}
	if *sc.ReadOnlyRootFilesystem {
		t.Error("expected ReadOnlyRootFilesystem false")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Error("expected capabilities drop ALL")
	}
	if len(sc.Capabilities.Add) != 2 {
		t.Errorf("expected 2 capability adds, got %d", len(sc.Capabilities.Add))
	}
}

// --- buildEnvVars tests ---

func TestBuildEnvVars_CoreVars(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		BeadID: "kd-abc",
	}

	envVars := mgr.buildEnvVars(spec)

	envMap := map[string]string{}
	for _, e := range envVars {
		if e.Value != "" {
			envMap[e.Name] = e.Value
		}
	}

	expected := map[string]string{
		"BOAT_ROLE":          "dev",
		"BOAT_PROJECT":       "proj",
		"BOAT_AGENT":         "alpha",
		"BOAT_MODE":          "crew",
		"HOME":               "/home/agent",
		"BEADS_ACTOR":        "alpha",
		"KD_ACTOR":           "alpha",
		"KD_AGENT_ID":        "kd-abc",
		"GIT_AUTHOR_NAME":    "alpha",
		"BEADS_AGENT_NAME":   "proj/alpha",
		"BOAT_AGENT_BEAD_ID": "kd-abc",
		"XDG_STATE_HOME":     MountStateDir,
	}
	for k, v := range expected {
		if envMap[k] != v {
			t.Errorf("env %s = %q, want %q", k, envMap[k], v)
		}
	}
}

func TestBuildEnvVars_PodIP(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
	}

	envVars := mgr.buildEnvVars(spec)

	var foundPodIP bool
	for _, e := range envVars {
		if e.Name == "POD_IP" {
			foundPodIP = true
			if e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != "status.podIP" {
				t.Error("POD_IP should come from downward API status.podIP")
			}
		}
	}
	if !foundPodIP {
		t.Error("expected POD_IP env var from downward API")
	}
}

func TestBuildEnvVars_SessionResume(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())

	// Without workspace storage — no session resume.
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha"}
	envVars := mgr.buildEnvVars(spec)
	for _, e := range envVars {
		if e.Name == "BOAT_SESSION_RESUME" {
			t.Error("BOAT_SESSION_RESUME should not be set without workspace storage")
		}
	}

	// With workspace storage — session resume enabled.
	spec.WorkspaceStorage = &WorkspaceStorageSpec{Size: "10Gi"}
	envVars = mgr.buildEnvVars(spec)
	var found bool
	for _, e := range envVars {
		if e.Name == "BOAT_SESSION_RESUME" {
			found = true
			if e.Value != "1" {
				t.Errorf("BOAT_SESSION_RESUME = %s, want 1", e.Value)
			}
		}
	}
	if !found {
		t.Error("expected BOAT_SESSION_RESUME with workspace storage")
	}
}

func TestBuildEnvVars_CustomEnv(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		Env: map[string]string{"CUSTOM_VAR": "custom_value"},
	}

	envVars := mgr.buildEnvVars(spec)
	var found bool
	for _, e := range envVars {
		if e.Name == "CUSTOM_VAR" && e.Value == "custom_value" {
			found = true
		}
	}
	if !found {
		t.Error("expected CUSTOM_VAR=custom_value")
	}
}

func TestBuildEnvVars_SecretEnv(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		SecretEnv: []SecretEnvSource{
			{EnvName: "API_KEY", SecretName: "api-secret", SecretKey: "key"},
		},
	}

	envVars := mgr.buildEnvVars(spec)
	var found bool
	for _, e := range envVars {
		if e.Name == "API_KEY" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			found = true
			if e.ValueFrom.SecretKeyRef.Name != "api-secret" {
				t.Errorf("secret name = %s, want api-secret", e.ValueFrom.SecretKeyRef.Name)
			}
			if e.ValueFrom.SecretKeyRef.Key != "key" {
				t.Errorf("secret key = %s, want key", e.ValueFrom.SecretKeyRef.Key)
			}
		}
	}
	if !found {
		t.Error("expected API_KEY secret env var")
	}
}

func TestBuildEnvVars_DaemonToken(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		DaemonTokenSecret: "beads-token",
	}

	envVars := mgr.buildEnvVars(spec)
	var found bool
	for _, e := range envVars {
		if e.Name == "BEADS_DAEMON_TOKEN" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			found = true
			if e.ValueFrom.SecretKeyRef.Name != "beads-token" {
				t.Errorf("daemon token secret name = %s, want beads-token", e.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	if !found {
		t.Error("expected BEADS_DAEMON_TOKEN from secret")
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
