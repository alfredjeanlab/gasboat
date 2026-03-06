package podmanager

import (
	"context"
	"log/slog"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestManager() *K8sManager {
	return New(fake.NewClientset(), slog.Default())
}

func minimalSpec() AgentPodSpec {
	return AgentPodSpec{
		Project:   "gasboat",
		Mode:      "crew",
		Role:      "dev",
		AgentName: "test-1",
		BeadID:    "bead-abc",
		Image:     "ghcr.io/agent:latest",
		Namespace: "default",
	}
}

// ── AgentPodSpec.PodName / Labels ──────────────────────────────────────────

func TestPodName(t *testing.T) {
	spec := AgentPodSpec{
		Mode: "crew", Project: "gasboat", Role: "dev", AgentName: "matt-1",
	}
	want := "crew-gasboat-dev-matt-1"
	if got := spec.PodName(); got != want {
		t.Errorf("PodName() = %q, want %q", got, want)
	}
}

func TestLabels(t *testing.T) {
	spec := AgentPodSpec{
		Mode: "job", Project: "acme", Role: "qa", AgentName: "bot-7",
	}
	labels := spec.Labels()
	checks := map[string]string{
		LabelApp:     LabelAppValue,
		LabelProject: "acme",
		LabelMode:    "job",
		LabelRole:    "qa",
		LabelAgent:   "bot-7",
	}
	for k, want := range checks {
		if got := labels[k]; got != want {
			t.Errorf("Labels()[%q] = %q, want %q", k, got, want)
		}
	}
}

// ── restartPolicyForMode ───────────────────────────────────────────────────

func TestRestartPolicyForMode(t *testing.T) {
	tests := []struct {
		mode string
		want corev1.RestartPolicy
	}{
		{"crew", corev1.RestartPolicyAlways},
		{"job", corev1.RestartPolicyNever},
		{"unknown", corev1.RestartPolicyAlways},
	}
	for _, tt := range tests {
		if got := restartPolicyForMode(tt.mode); got != tt.want {
			t.Errorf("restartPolicyForMode(%q) = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

// ── K8sManager CRUD (with fake client) ─────────────────────────────────────

func TestCreateAgentPod(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())
	spec := minimalSpec()

	if err := m.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("CreateAgentPod: %v", err)
	}

	pod, err := client.CoreV1().Pods("default").Get(context.Background(), spec.PodName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if pod.Name != spec.PodName() {
		t.Errorf("pod name = %q, want %q", pod.Name, spec.PodName())
	}
}

func TestCreateAgentPod_Idempotent(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())
	spec := minimalSpec()
	if err := m.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("first CreateAgentPod: %v", err)
	}
	if err := m.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("second CreateAgentPod should be idempotent: %v", err)
	}
}

func TestCreateAgentPod_WithPVC(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())
	spec := minimalSpec()
	spec.WorkspaceStorage = &WorkspaceStorageSpec{
		Size:             "10Gi",
		StorageClassName: "gp3",
	}

	if err := m.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("CreateAgentPod: %v", err)
	}

	// PVC should be created.
	pvcName := spec.PodName() + "-workspace"
	pvc, err := client.CoreV1().PersistentVolumeClaims("default").Get(context.Background(), pvcName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get PVC: %v", err)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "gp3" {
		t.Error("PVC storage class not set")
	}
}

func TestCreateAgentPod_PVCIdempotent(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())
	spec := minimalSpec()
	spec.WorkspaceStorage = &WorkspaceStorageSpec{Size: "5Gi"}

	// Pre-create the PVC.
	pvcName := spec.PodName() + "-workspace"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: "default"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("5Gi"),
				},
			},
		},
	}
	_, err := client.CoreV1().PersistentVolumeClaims("default").Create(context.Background(), pvc, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("pre-create PVC: %v", err)
	}

	// CreateAgentPod should not fail on existing PVC.
	if err := m.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("CreateAgentPod with existing PVC: %v", err)
	}
}

func TestEnsurePVC_Idempotent(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())
	spec := minimalSpec()
	spec.WorkspaceStorage = &WorkspaceStorageSpec{Size: "10Gi"}
	if err := m.ensurePVC(context.Background(), spec); err != nil {
		t.Fatalf("first ensurePVC: %v", err)
	}
	if err := m.ensurePVC(context.Background(), spec); err != nil {
		t.Fatalf("second ensurePVC should be idempotent: %v", err)
	}
}

func TestDeleteAgentPod(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())
	spec := minimalSpec()

	// Create then delete.
	_ = m.CreateAgentPod(context.Background(), spec)
	if err := m.DeleteAgentPod(context.Background(), spec.PodName(), "default"); err != nil {
		t.Fatalf("DeleteAgentPod: %v", err)
	}

	_, err := client.CoreV1().Pods("default").Get(context.Background(), spec.PodName(), metav1.GetOptions{})
	if err == nil {
		t.Error("pod should be deleted")
	}
}

func TestDeleteAgentPod_NotFound(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())
	if err := m.DeleteAgentPod(context.Background(), "nonexistent", "default"); err != nil {
		t.Fatalf("DeleteAgentPod for missing pod should be idempotent: %v", err)
	}
}

func TestListAgentPods(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())

	spec1 := minimalSpec()
	spec2 := minimalSpec()
	spec2.AgentName = "test-2"

	_ = m.CreateAgentPod(context.Background(), spec1)
	_ = m.CreateAgentPod(context.Background(), spec2)

	pods, err := m.ListAgentPods(context.Background(), "default", map[string]string{
		LabelApp: LabelAppValue,
	})
	if err != nil {
		t.Fatalf("ListAgentPods: %v", err)
	}
	if len(pods) != 2 {
		t.Errorf("expected 2 pods, got %d", len(pods))
	}
}

func TestGetAgentPod(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())
	spec := minimalSpec()

	_ = m.CreateAgentPod(context.Background(), spec)

	pod, err := m.GetAgentPod(context.Background(), spec.PodName(), "default")
	if err != nil {
		t.Fatalf("GetAgentPod: %v", err)
	}
	if pod.Name != spec.PodName() {
		t.Errorf("pod name = %q, want %q", pod.Name, spec.PodName())
	}
}

func TestGetAgentPod_NotFound(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())

	_, err := m.GetAgentPod(context.Background(), "nonexistent", "default")
	if err == nil {
		t.Error("expected error for nonexistent pod")
	}
}

// ── DeleteAllAgentPods ──────────────────────────────────────────────────────

func TestDeleteAllAgentPods(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())

	for _, name := range []string{"a-1", "a-2", "a-3"} {
		spec := minimalSpec()
		spec.AgentName = name
		if err := m.CreateAgentPod(context.Background(), spec); err != nil {
			t.Fatalf("create pod %s: %v", name, err)
		}
	}

	deleted, err := m.DeleteAllAgentPods(context.Background(), "default", false)
	if err != nil {
		t.Fatalf("DeleteAllAgentPods: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}

	pods, _ := m.ListAgentPods(context.Background(), "default", map[string]string{LabelApp: LabelAppValue})
	if len(pods) != 0 {
		t.Errorf("expected 0 pods remaining, got %d", len(pods))
	}
}

func TestDeleteAllAgentPods_Force(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())

	spec := minimalSpec()
	if err := m.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	deleted, err := m.DeleteAllAgentPods(context.Background(), "default", true)
	if err != nil {
		t.Fatalf("DeleteAllAgentPods force: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	pods, _ := m.ListAgentPods(context.Background(), "default", map[string]string{LabelApp: LabelAppValue})
	if len(pods) != 0 {
		t.Errorf("expected 0 pods remaining, got %d", len(pods))
	}
}

func TestDeleteAllAgentPods_NoPods(t *testing.T) {
	client := fake.NewClientset()
	m := New(client, slog.Default())

	deleted, err := m.DeleteAllAgentPods(context.Background(), "default", false)
	if err != nil {
		t.Fatalf("DeleteAllAgentPods empty: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}
