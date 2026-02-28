package podmanager

import (
	"context"
	"log/slog"
	"os"
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

func TestPodName(t *testing.T) {
	spec := AgentPodSpec{Mode: "crew", Project: "gasboat", Role: "dev", AgentName: "alpha"}
	if got := spec.PodName(); got != "crew-gasboat-dev-alpha" {
		t.Errorf("PodName() = %s, want crew-gasboat-dev-alpha", got)
	}
}

func TestLabels(t *testing.T) {
	spec := AgentPodSpec{Mode: "crew", Project: "gasboat", Role: "dev", AgentName: "alpha"}
	labels := spec.Labels()
	expected := map[string]string{LabelApp: LabelAppValue, LabelProject: "gasboat", LabelMode: "crew", LabelRole: "dev", LabelAgent: "alpha"}
	for k, v := range expected {
		if labels[k] != v {
			t.Errorf("Labels()[%s] = %s, want %s", k, labels[k], v)
		}
	}
}

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

func TestCreateAgentPod_Basic(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "ghcr.io/org/agent:v1", Namespace: "ns", BeadID: "kd-abc"}
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pod, err := client.CoreV1().Pods("ns").Get(context.Background(), "crew-proj-dev-alpha", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod not found: %v", err)
	}
	if pod.Labels[LabelProject] != "proj" {
		t.Errorf("expected project label proj, got %s", pod.Labels[LabelProject])
	}
	if pod.Annotations[AnnotationBeadID] != "kd-abc" {
		t.Errorf("expected bead-id annotation kd-abc, got %s", pod.Annotations[AnnotationBeadID])
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != "ghcr.io/org/agent:v1" {
		t.Error("unexpected container setup")
	}
}

func TestCreateAgentPod_Idempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "ghcr.io/org/agent:v1", Namespace: "ns"}
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("first CreateAgentPod: %v", err)
	}
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("second CreateAgentPod should be idempotent: %v", err)
	}
}

func TestCreateAgentPod_WithPVC(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1", Namespace: "ns", WorkspaceStorage: &WorkspaceStorageSpec{Size: "20Gi", StorageClassName: "gp3"}}
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("ns").Get(context.Background(), "crew-proj-dev-alpha-workspace", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("PVC not found: %v", err)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "gp3" {
		t.Errorf("expected storage class gp3")
	}
}

func TestCreateAgentPod_PVCIdempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1", Namespace: "ns", WorkspaceStorage: &WorkspaceStorageSpec{ClaimName: "my-pvc", Size: "10Gi"}}
	client.CoreV1().PersistentVolumeClaims("ns").Create(context.Background(), &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "my-pvc", Namespace: "ns"}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}}}, metav1.CreateOptions{})
	if err := mgr.CreateAgentPod(context.Background(), spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsurePVC_Idempotent(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	spec := AgentPodSpec{Mode: "crew", Project: "proj", Role: "dev", AgentName: "alpha", Image: "img:v1", Namespace: "ns", WorkspaceStorage: &WorkspaceStorageSpec{Size: "10Gi"}}
	if err := mgr.ensurePVC(context.Background(), spec); err != nil {
		t.Fatalf("first ensurePVC: %v", err)
	}
	if err := mgr.ensurePVC(context.Background(), spec); err != nil {
		t.Fatalf("second ensurePVC should be idempotent: %v", err)
	}
}

func TestDeleteAgentPod(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "ns"}})
	mgr := New(client, testLogger())
	if err := mgr.DeleteAgentPod(context.Background(), "test-pod", "ns"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := client.CoreV1().Pods("ns").Get(context.Background(), "test-pod", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestDeleteAgentPod_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := New(client, testLogger())
	if err := mgr.DeleteAgentPod(context.Background(), "nonexistent", "ns"); err != nil {
		t.Fatalf("DeleteAgentPod for missing pod should be idempotent: %v", err)
	}
}

func TestListAgentPods(t *testing.T) {
	pod1 := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "crew-proj-dev-alpha", Namespace: "ns", Labels: map[string]string{LabelApp: LabelAppValue, LabelProject: "proj"}}}
	pod2 := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "crew-other-dev-beta", Namespace: "ns", Labels: map[string]string{LabelApp: LabelAppValue, LabelProject: "other"}}}
	client := fake.NewSimpleClientset(pod1, pod2)
	mgr := New(client, testLogger())
	pods, err := mgr.ListAgentPods(context.Background(), "ns", map[string]string{LabelProject: "proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != "crew-proj-dev-alpha" {
		t.Errorf("expected 1 pod crew-proj-dev-alpha, got %d pods", len(pods))
	}
}

func TestGetAgentPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "ns"}}
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
