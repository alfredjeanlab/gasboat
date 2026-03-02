package podmanager

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// --- AgentPodSpec tests ---

func TestPodName(t *testing.T) {
	spec := AgentPodSpec{Mode: "crew", Project: "gasboat", Role: "dev", AgentName: "alpha"}
	if got := spec.PodName(); got != "crew-gasboat-dev-alpha" {
		t.Errorf("PodName() = %s, want crew-gasboat-dev-alpha", got)
	}
}

func TestLabels(t *testing.T) {
	spec := AgentPodSpec{Mode: "crew", Project: "gasboat", Role: "dev", AgentName: "alpha"}
	labels := spec.Labels()

	expected := map[string]string{
		LabelApp:     LabelAppValue,
		LabelProject: "gasboat",
		LabelMode:    "crew",
		LabelRole:    "dev",
		LabelAgent:   "alpha",
	}
	for k, v := range expected {
		if labels[k] != v {
			t.Errorf("Labels()[%s] = %s, want %s", k, labels[k], v)
		}
	}
}

// --- restartPolicyForMode tests ---

func TestRestartPolicyForMode_Crew(t *testing.T) {
	if got := restartPolicyForMode("crew"); got != corev1.RestartPolicyAlways {
		t.Errorf("restartPolicyForMode(crew) = %s, want Always", got)
	}
}

func TestRestartPolicyForMode_Job(t *testing.T) {
	if got := restartPolicyForMode("job"); got != corev1.RestartPolicyNever {
		t.Errorf("restartPolicyForMode(job) = %s, want Never", got)
	}
}

func TestRestartPolicyForMode_Unknown(t *testing.T) {
	if got := restartPolicyForMode("other"); got != corev1.RestartPolicyAlways {
		t.Errorf("restartPolicyForMode(other) = %s, want Always", got)
	}
}

// --- CreateAgentPod tests ---

func TestCreateAgentPod_Basic(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())

	spec := AgentPodSpec{
		Mode:      "crew",
		Project:   "proj",
		Role:      "dev",
		AgentName: "alpha",
		Image:     "ghcr.io/org/agent:v1",
		Namespace: "ns",
		BeadID:    "kd-abc",
	}

	err := mgr.CreateAgentPod(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the pod was created.
	pod, err := client.CoreV1().Pods("ns").Get(context.Background(), "crew-proj-dev-alpha", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod not found: %v", err)
	}

	// Check labels.
	if pod.Labels[LabelProject] != "proj" {
		t.Errorf("expected project label proj, got %s", pod.Labels[LabelProject])
	}
	if pod.Labels[LabelAgent] != "alpha" {
		t.Errorf("expected agent label alpha, got %s", pod.Labels[LabelAgent])
	}

	// Check annotation.
	if pod.Annotations[AnnotationBeadID] != "kd-abc" {
		t.Errorf("expected bead-id annotation kd-abc, got %s", pod.Annotations[AnnotationBeadID])
	}

	// Check container image.
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Spec.Containers))
	}
	if pod.Spec.Containers[0].Image != "ghcr.io/org/agent:v1" {
		t.Errorf("expected image ghcr.io/org/agent:v1, got %s", pod.Spec.Containers[0].Image)
	}
	if pod.Spec.Containers[0].Name != ContainerName {
		t.Errorf("expected container name %s, got %s", ContainerName, pod.Spec.Containers[0].Name)
	}
}

func TestCreateAgentPod_WithPVC(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())

	spec := AgentPodSpec{
		Mode:      "crew",
		Project:   "proj",
		Role:      "dev",
		AgentName: "alpha",
		Image:     "img:v1",
		Namespace: "ns",
		WorkspaceStorage: &WorkspaceStorageSpec{
			Size:             "20Gi",
			StorageClassName: "gp3",
		},
	}

	err := mgr.CreateAgentPod(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify PVC was created.
	pvc, err := client.CoreV1().PersistentVolumeClaims("ns").Get(
		context.Background(), "crew-proj-dev-alpha-workspace", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("PVC not found: %v", err)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "gp3" {
		t.Errorf("expected storage class gp3")
	}
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.String() != "20Gi" {
		t.Errorf("expected storage 20Gi, got %s", storage.String())
	}
}

func TestCreateAgentPod_PVCIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())

	spec := AgentPodSpec{
		Mode:      "crew",
		Project:   "proj",
		Role:      "dev",
		AgentName: "alpha",
		Image:     "img:v1",
		Namespace: "ns",
		WorkspaceStorage: &WorkspaceStorageSpec{
			ClaimName: "my-pvc",
			Size:      "10Gi",
		},
	}

	// Create PVC first.
	_, err := client.CoreV1().PersistentVolumeClaims("ns").Create(context.Background(),
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: "ns"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
			},
		}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to pre-create PVC: %v", err)
	}

	// CreateAgentPod should succeed (PVC already exists).
	err = mgr.CreateAgentPod(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateAgentPod_PVCDefaultClaimName(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())

	spec := AgentPodSpec{
		Mode:      "crew",
		Project:   "proj",
		Role:      "dev",
		AgentName: "beta",
		Image:     "img:v1",
		Namespace: "ns",
		WorkspaceStorage: &WorkspaceStorageSpec{
			// ClaimName empty — should derive from pod name.
		},
	}

	err := mgr.CreateAgentPod(context.Background(), spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// PVC name should be derived from pod name.
	_, err = client.CoreV1().PersistentVolumeClaims("ns").Get(
		context.Background(), "crew-proj-dev-beta-workspace", metav1.GetOptions{})
	if err != nil {
		t.Errorf("expected PVC crew-proj-dev-beta-workspace, got error: %v", err)
	}
}

// --- DeleteAgentPod tests ---

func TestDeleteAgentPod(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "ns"},
	})
	mgr := New(client, testLogger())

	err := mgr.DeleteAgentPod(context.Background(), "test-pod", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify pod is gone.
	_, err = client.CoreV1().Pods("ns").Get(context.Background(), "test-pod", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestDeleteAgentPod_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())

	err := mgr.DeleteAgentPod(context.Background(), "nonexistent", "ns")
	if err == nil {
		t.Fatal("expected error when deleting non-existent pod")
	}
}

// --- ListAgentPods tests ---

func TestListAgentPods(t *testing.T) {
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crew-proj-dev-alpha",
			Namespace: "ns",
			Labels:    map[string]string{LabelApp: LabelAppValue, LabelProject: "proj"},
		},
	}
	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crew-other-dev-beta",
			Namespace: "ns",
			Labels:    map[string]string{LabelApp: LabelAppValue, LabelProject: "other"},
		},
	}
	client := fake.NewSimpleClientset(pod1, pod2)
	mgr := New(client, testLogger())

	pods, err := mgr.ListAgentPods(context.Background(), "ns", map[string]string{
		LabelProject: "proj",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(pods))
	}
	if pods[0].Name != "crew-proj-dev-alpha" {
		t.Errorf("expected crew-proj-dev-alpha, got %s", pods[0].Name)
	}
}

func TestListAgentPods_Empty(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())

	pods, err := mgr.ListAgentPods(context.Background(), "ns", map[string]string{
		LabelApp: LabelAppValue,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 0 {
		t.Errorf("expected 0 pods, got %d", len(pods))
	}
}

// --- GetAgentPod tests ---

func TestGetAgentPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "ns"},
	}
	client := fake.NewSimpleClientset(pod)
	mgr := New(client, testLogger())

	got, err := mgr.GetAgentPod(context.Background(), "my-pod", "ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "my-pod" {
		t.Errorf("expected my-pod, got %s", got.Name)
	}
}

func TestGetAgentPod_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())

	_, err := mgr.GetAgentPod(context.Background(), "nonexistent", "ns")
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected not found error, got: %v", err)
	}
}

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
		"BOAT_ROLE":           "dev",
		"BOAT_PROJECT":        "proj",
		"BOAT_AGENT":          "alpha",
		"BOAT_MODE":           "crew",
		"HOME":                "/home/agent",
		"BEADS_ACTOR":         "alpha",
		"KD_ACTOR":            "alpha",
		"KD_AGENT_ID":         "kd-abc",
		"GIT_AUTHOR_NAME":     "alpha",
		"BEADS_AGENT_NAME":    "proj/alpha",
		"BOAT_AGENT_BEAD_ID":  "kd-abc",
		"XDG_STATE_HOME":      MountStateDir,
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
	var foundDaemon, foundAuth bool
	for _, e := range envVars {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			switch e.Name {
			case "BEADS_DAEMON_TOKEN":
				foundDaemon = true
				if e.ValueFrom.SecretKeyRef.Name != "beads-token" {
					t.Errorf("daemon token secret name = %s, want beads-token", e.ValueFrom.SecretKeyRef.Name)
				}
			case "BEADS_AUTH_TOKEN":
				foundAuth = true
				if e.ValueFrom.SecretKeyRef.Name != "beads-token" {
					t.Errorf("auth token secret name = %s, want beads-token", e.ValueFrom.SecretKeyRef.Name)
				}
			}
		}
	}
	if !foundDaemon {
		t.Error("expected BEADS_DAEMON_TOKEN from secret")
	}
	if !foundAuth {
		t.Error("expected BEADS_AUTH_TOKEN from secret")
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

// --- buildVolumes tests ---

func TestBuildVolumes_EmptyDir(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha"}

	volumes := mgr.buildVolumes(spec)

	var wsVol *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == VolumeWorkspace {
			wsVol = &volumes[i]
		}
	}
	if wsVol == nil {
		t.Fatal("expected workspace volume")
	}
	if wsVol.EmptyDir == nil {
		t.Error("expected EmptyDir for workspace without storage spec")
	}
}

func TestBuildVolumes_PVC(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		WorkspaceStorage: &WorkspaceStorageSpec{ClaimName: "my-pvc"},
	}

	volumes := mgr.buildVolumes(spec)

	var wsVol *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == VolumeWorkspace {
			wsVol = &volumes[i]
		}
	}
	if wsVol == nil {
		t.Fatal("expected workspace volume")
	}
	if wsVol.PersistentVolumeClaim == nil {
		t.Fatal("expected PVC for workspace with storage spec")
	}
	if wsVol.PersistentVolumeClaim.ClaimName != "my-pvc" {
		t.Errorf("PVC claim name = %s, want my-pvc", wsVol.PersistentVolumeClaim.ClaimName)
	}
}

func TestBuildVolumes_TmpAlwaysPresent(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{}

	volumes := mgr.buildVolumes(spec)

	var found bool
	for _, v := range volumes {
		if v.Name == VolumeTmp && v.EmptyDir != nil {
			found = true
		}
	}
	if !found {
		t.Error("expected tmp EmptyDir volume")
	}
}

func TestBuildVolumes_ConfigMap(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{ConfigMapName: "agent-config"}

	volumes := mgr.buildVolumes(spec)

	var found bool
	for _, v := range volumes {
		if v.Name == VolumeBeadsConfig && v.ConfigMap != nil {
			found = true
			if v.ConfigMap.Name != "agent-config" {
				t.Errorf("ConfigMap name = %s, want agent-config", v.ConfigMap.Name)
			}
		}
	}
	if !found {
		t.Error("expected beads-config ConfigMap volume")
	}
}

func TestBuildVolumes_CredentialsSecret(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{CredentialsSecret: "claude-creds"}

	volumes := mgr.buildVolumes(spec)

	var found bool
	for _, v := range volumes {
		if v.Name == VolumeClaudeCreds && v.Secret != nil {
			found = true
			if v.Secret.SecretName != "claude-creds" {
				t.Errorf("Secret name = %s, want claude-creds", v.Secret.SecretName)
			}
		}
	}
	if !found {
		t.Error("expected claude-creds Secret volume")
	}
}

// --- buildVolumeMounts tests ---

func TestBuildVolumeMounts_Basic(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{}

	mounts := mgr.buildVolumeMounts(spec)

	mountPaths := map[string]string{}
	for _, m := range mounts {
		mountPaths[m.Name] = m.MountPath
	}
	if mountPaths[VolumeWorkspace] != MountWorkspace {
		t.Errorf("workspace mount path = %s, want %s", mountPaths[VolumeWorkspace], MountWorkspace)
	}
	if mountPaths[VolumeTmp] != MountTmp {
		t.Errorf("tmp mount path = %s, want %s", mountPaths[VolumeTmp], MountTmp)
	}
}

func TestBuildVolumeMounts_ClaudeState(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		WorkspaceStorage: &WorkspaceStorageSpec{Size: "10Gi"},
	}

	mounts := mgr.buildVolumeMounts(spec)

	var found bool
	for _, m := range mounts {
		if m.MountPath == MountClaudeState && m.SubPath == SubPathClaudeState {
			found = true
		}
	}
	if !found {
		t.Error("expected Claude state subPath mount with workspace storage")
	}
}

func TestBuildVolumeMounts_NoClaudeStateWithoutStorage(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{}

	mounts := mgr.buildVolumeMounts(spec)

	for _, m := range mounts {
		if m.MountPath == MountClaudeState {
			t.Error("should not have Claude state mount without workspace storage")
		}
	}
}

func TestBuildVolumeMounts_ConfigMap(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{ConfigMapName: "cfg"}

	mounts := mgr.buildVolumeMounts(spec)

	var found bool
	for _, m := range mounts {
		if m.Name == VolumeBeadsConfig && m.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Error("expected read-only beads-config mount")
	}
}

func TestBuildVolumeMounts_CredentialsSecret(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{CredentialsSecret: "creds"}

	mounts := mgr.buildVolumeMounts(spec)

	var found bool
	for _, m := range mounts {
		if m.Name == VolumeClaudeCreds && m.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Error("expected read-only claude-creds mount")
	}
}

// --- initclone tests ---

func TestBuildInitCloneContainer_NoGitURL(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj"}

	ic := mgr.buildInitCloneContainer(spec)
	if ic != nil {
		t.Error("expected nil init container without GitURL")
	}
}

func TestBuildInitCloneContainer_WithGitURL(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		GitURL:           "https://github.com/org/repo.git",
		GitDefaultBranch: "develop",
	}

	ic := mgr.buildInitCloneContainer(spec)
	if ic == nil {
		t.Fatal("expected init container with GitURL")
	}
	if ic.Name != InitCloneName {
		t.Errorf("init container name = %s, want %s", ic.Name, InitCloneName)
	}
	if ic.Image != InitCloneImage {
		t.Errorf("init container image = %s, want %s", ic.Image, InitCloneImage)
	}

	script := ic.Command[2] // /bin/sh -c <script>
	if !strings.Contains(script, "git clone -b develop") {
		t.Error("script should contain git clone with specified branch")
	}
	if !strings.Contains(script, "https://github.com/org/repo.git") {
		t.Error("script should contain the git URL")
	}
	if !strings.Contains(script, "git config user.name") {
		t.Error("script should configure git user name")
	}
}

func TestBuildInitCloneContainer_DefaultBranch(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		GitURL: "https://github.com/org/repo.git",
		// No GitDefaultBranch — defaults to "main"
	}

	ic := mgr.buildInitCloneContainer(spec)
	script := ic.Command[2]
	if !strings.Contains(script, "git clone -b main") {
		t.Error("script should default to main branch")
	}
}

func TestBuildInitCloneContainer_WithReferenceRepos(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		ReferenceRepos: []RepoRef{
			{URL: "https://github.com/org/ref1.git", Branch: "v2", Name: "ref1"},
			{URL: "https://github.com/org/ref2.git", Name: "ref2"}, // default branch
		},
	}

	ic := mgr.buildInitCloneContainer(spec)
	if ic == nil {
		t.Fatal("expected init container with reference repos")
	}

	script := ic.Command[2]
	if !strings.Contains(script, "ref1") {
		t.Error("script should contain reference repo ref1")
	}
	if !strings.Contains(script, "ref2") {
		t.Error("script should contain reference repo ref2")
	}
	if !strings.Contains(script, "-b v2") {
		t.Error("script should use branch v2 for ref1")
	}
}

func TestBuildInitCloneContainer_GitCredentials(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		GitURL:               "https://github.com/org/private.git",
		GitCredentialsSecret: "git-creds",
	}

	ic := mgr.buildInitCloneContainer(spec)
	if ic == nil {
		t.Fatal("expected init container")
	}

	// Check credential env vars.
	envMap := map[string]string{}
	for _, e := range ic.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			envMap[e.Name] = e.ValueFrom.SecretKeyRef.Name
		}
	}
	if envMap["GIT_USERNAME"] != "git-creds" {
		t.Errorf("GIT_USERNAME secret = %s, want git-creds", envMap["GIT_USERNAME"])
	}
	if envMap["GIT_TOKEN"] != "git-creds" {
		t.Errorf("GIT_TOKEN secret = %s, want git-creds", envMap["GIT_TOKEN"])
	}

	// Check credential helper in script.
	script := ic.Command[2]
	if !strings.Contains(script, "credential.helper") {
		t.Error("script should configure git credential helper")
	}
}

func TestBuildInitCloneContainer_GitlabToken(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		GitURL:            "https://gitlab.com/org/repo.git",
		GitlabTokenSecret: "gitlab-token",
	}

	ic := mgr.buildInitCloneContainer(spec)

	envMap := map[string]string{}
	for _, e := range ic.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			envMap[e.Name] = e.ValueFrom.SecretKeyRef.Name
		}
	}
	if envMap["GITLAB_TOKEN"] != "gitlab-token" {
		t.Errorf("GITLAB_TOKEN secret = %s, want gitlab-token", envMap["GITLAB_TOKEN"])
	}

	script := ic.Command[2]
	if !strings.Contains(script, "GITLAB_TOKEN") {
		t.Error("script should reference GITLAB_TOKEN for credential setup")
	}
}

func TestBuildInitCloneContainer_RunsAsRoot(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		GitURL: "https://github.com/org/repo.git",
	}

	ic := mgr.buildInitCloneContainer(spec)
	if *ic.SecurityContext.RunAsUser != 0 {
		t.Errorf("init container should run as root, got UID %d", *ic.SecurityContext.RunAsUser)
	}
	if *ic.SecurityContext.RunAsNonRoot {
		t.Error("init container RunAsNonRoot should be false")
	}
}

func TestBuildInitCloneContainer_WorkspaceMount(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha",
		GitURL: "https://github.com/org/repo.git",
	}

	ic := mgr.buildInitCloneContainer(spec)
	if len(ic.VolumeMounts) != 1 {
		t.Fatalf("expected 1 volume mount, got %d", len(ic.VolumeMounts))
	}
	if ic.VolumeMounts[0].Name != VolumeWorkspace {
		t.Errorf("expected workspace volume mount, got %s", ic.VolumeMounts[0].Name)
	}
}

func TestBuildInitCloneContainer_ChownsWorkspace(t *testing.T) {
	mgr := New(fake.NewSimpleClientset(), testLogger())
	spec := AgentPodSpec{
		Mode: "crew", Project: "myproj", Role: "dev", AgentName: "alpha",
		GitURL: "https://github.com/org/repo.git",
	}

	ic := mgr.buildInitCloneContainer(spec)
	script := ic.Command[2]
	expectedChown := "chown -R 1000:1000"
	if !strings.Contains(script, expectedChown) {
		t.Errorf("script should chown workspace to %d:%d", AgentUID, AgentGID)
	}
}
