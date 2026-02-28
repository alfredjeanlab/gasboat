package podmanager

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// --- buildVolumes tests ---

func TestBuildVolumes_EmptyDir(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	volumes := m.buildVolumes(spec)

	volMap := make(map[string]corev1.Volume)
	for _, v := range volumes {
		volMap[v.Name] = v
	}

	ws, ok := volMap[VolumeWorkspace]
	if !ok {
		t.Fatal("missing workspace volume")
	}
	if ws.VolumeSource.EmptyDir == nil {
		t.Error("workspace should be EmptyDir without WorkspaceStorage")
	}

	tmp, ok := volMap[VolumeTmp]
	if !ok {
		t.Fatal("missing tmp volume")
	}
	if tmp.VolumeSource.EmptyDir == nil {
		t.Error("tmp should be EmptyDir")
	}
}

func TestBuildVolumes_PVC(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.WorkspaceStorage = &WorkspaceStorageSpec{ClaimName: "my-pvc"}
	volumes := m.buildVolumes(spec)

	for _, v := range volumes {
		if v.Name == VolumeWorkspace {
			if v.VolumeSource.PersistentVolumeClaim == nil {
				t.Fatal("workspace should be PVC with WorkspaceStorage")
			}
			if v.VolumeSource.PersistentVolumeClaim.ClaimName != "my-pvc" {
				t.Errorf("PVC claim = %q, want my-pvc",
					v.VolumeSource.PersistentVolumeClaim.ClaimName)
			}
			return
		}
	}
	t.Error("workspace volume not found")
}

func TestBuildVolumes_PVCDefaultName(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.WorkspaceStorage = &WorkspaceStorageSpec{} // empty ClaimName
	volumes := m.buildVolumes(spec)

	for _, v := range volumes {
		if v.Name == VolumeWorkspace {
			want := spec.PodName() + "-workspace"
			if v.VolumeSource.PersistentVolumeClaim.ClaimName != want {
				t.Errorf("PVC claim = %q, want %q",
					v.VolumeSource.PersistentVolumeClaim.ClaimName, want)
			}
			return
		}
	}
	t.Error("workspace volume not found")
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
	m := newTestManager()
	spec := minimalSpec()
	spec.ConfigMapName = "agent-config"
	volumes := m.buildVolumes(spec)

	for _, v := range volumes {
		if v.Name == VolumeBeadsConfig {
			if v.VolumeSource.ConfigMap == nil {
				t.Fatal("beads-config should be ConfigMap")
			}
			if v.VolumeSource.ConfigMap.Name != "agent-config" {
				t.Errorf("ConfigMap name = %q, want agent-config", v.VolumeSource.ConfigMap.Name)
			}
			return
		}
	}
	t.Error("beads-config volume not found")
}

func TestBuildVolumes_CredentialsSecret(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.CredentialsSecret = "claude-creds"
	volumes := m.buildVolumes(spec)

	for _, v := range volumes {
		if v.Name == VolumeClaudeCreds {
			if v.VolumeSource.Secret == nil {
				t.Fatal("claude-creds should be Secret volume")
			}
			if v.VolumeSource.Secret.SecretName != "claude-creds" {
				t.Errorf("Secret name = %q, want claude-creds", v.VolumeSource.Secret.SecretName)
			}
			return
		}
	}
	t.Error("claude-creds volume not found")
}

// --- buildVolumeMounts tests ---

func TestBuildVolumeMounts_Minimal(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	mounts := m.buildVolumeMounts(spec)

	mountMap := make(map[string]corev1.VolumeMount)
	for _, vm := range mounts {
		mountMap[vm.Name] = vm
	}

	if ws, ok := mountMap[VolumeWorkspace]; !ok {
		t.Error("missing workspace mount")
	} else if ws.MountPath != MountWorkspace {
		t.Errorf("workspace mount path = %q, want %q", ws.MountPath, MountWorkspace)
	}

	if tmp, ok := mountMap[VolumeTmp]; !ok {
		t.Error("missing tmp mount")
	} else if tmp.MountPath != MountTmp {
		t.Errorf("tmp mount path = %q, want %q", tmp.MountPath, MountTmp)
	}
}

func TestBuildVolumeMounts_ClaudeState(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.WorkspaceStorage = &WorkspaceStorageSpec{Size: "5Gi"}
	mounts := m.buildVolumeMounts(spec)

	found := false
	for _, vm := range mounts {
		if vm.MountPath == MountClaudeState {
			found = true
			if vm.SubPath != SubPathClaudeState {
				t.Errorf("SubPath = %q, want %q", vm.SubPath, SubPathClaudeState)
			}
		}
	}
	if !found {
		t.Error("Claude state mount not found with WorkspaceStorage")
	}

	// Without workspace storage — no Claude state mount.
	spec2 := minimalSpec()
	mounts2 := m.buildVolumeMounts(spec2)
	for _, vm := range mounts2 {
		if vm.MountPath == MountClaudeState {
			t.Error("Claude state mount should not exist without WorkspaceStorage")
		}
	}
}

func TestBuildVolumeMounts_ConfigMapReadOnly(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.ConfigMapName = "agent-config"
	mounts := m.buildVolumeMounts(spec)

	for _, vm := range mounts {
		if vm.Name == VolumeBeadsConfig {
			if !vm.ReadOnly {
				t.Error("beads-config mount should be ReadOnly")
			}
			if vm.MountPath != MountBeadsConfig {
				t.Errorf("mount path = %q, want %q", vm.MountPath, MountBeadsConfig)
			}
			return
		}
	}
	t.Error("beads-config mount not found")
}

func TestBuildVolumeMounts_Credentials(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.CredentialsSecret = "claude-creds"
	mounts := m.buildVolumeMounts(spec)

	for _, vm := range mounts {
		if vm.Name == VolumeClaudeCreds {
			if !vm.ReadOnly {
				t.Error("credentials mount should be ReadOnly")
			}
			if vm.MountPath != MountClaudeCreds {
				t.Errorf("mount path = %q, want %q", vm.MountPath, MountClaudeCreds)
			}
			return
		}
	}
	t.Error("credentials mount not found")
}

// --- initclone tests ---

func TestBuildInitCloneContainer_Nil(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	if ic := m.buildInitCloneContainer(spec); ic != nil {
		t.Error("expected nil init container without GitURL or ReferenceRepos")
	}
}

func TestBuildInitCloneContainer_WithGitURL(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.GitURL = "https://github.com/example/repo.git"
	ic := m.buildInitCloneContainer(spec)

	if ic == nil {
		t.Fatal("expected init container with GitURL")
	}
	if ic.Name != InitCloneName {
		t.Errorf("name = %q, want %q", ic.Name, InitCloneName)
	}
	if ic.Image != InitCloneImage {
		t.Errorf("image = %q, want %q", ic.Image, InitCloneImage)
	}

	// Should run as root.
	if ic.SecurityContext == nil || ic.SecurityContext.RunAsUser == nil {
		t.Fatal("missing security context")
	}
	if *ic.SecurityContext.RunAsUser != 0 {
		t.Errorf("RunAsUser = %d, want 0 (root)", *ic.SecurityContext.RunAsUser)
	}

	// Script should reference the git URL.
	script := ic.Command[2]
	if !strings.Contains(script, spec.GitURL) {
		t.Error("script should contain GitURL")
	}
	if !strings.Contains(script, "apk add --no-cache git") {
		t.Error("script should install git")
	}
	// Should set default branch to main.
	if !strings.Contains(script, "main") {
		t.Error("script should use default branch 'main'")
	}
}

func TestBuildInitCloneContainer_GitCredentials(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.GitURL = "https://github.com/example/repo.git"
	spec.GitCredentialsSecret = "git-creds"

	ic := m.buildInitCloneContainer(spec)
	script := ic.Command[2]

	if !strings.Contains(script, "credential.helper") {
		t.Error("script should set up credential helper")
	}

	envMap := make(map[string]corev1.EnvVar)
	for _, ev := range ic.Env {
		envMap[ev.Name] = ev
	}
	if _, ok := envMap["GIT_USERNAME"]; !ok {
		t.Error("missing GIT_USERNAME env var")
	}
	if _, ok := envMap["GIT_TOKEN"]; !ok {
		t.Error("missing GIT_TOKEN env var")
	}
}

func TestBuildInitCloneContainer_GitlabToken(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.GitURL = "https://gitlab.com/example/repo.git"
	spec.GitlabTokenSecret = "gitlab-token"

	ic := m.buildInitCloneContainer(spec)
	script := ic.Command[2]

	if !strings.Contains(script, "GITLAB_TOKEN") {
		t.Error("script should reference GITLAB_TOKEN")
	}

	envMap := make(map[string]corev1.EnvVar)
	for _, ev := range ic.Env {
		envMap[ev.Name] = ev
	}
	if _, ok := envMap["GITLAB_TOKEN"]; !ok {
		t.Error("missing GITLAB_TOKEN env var")
	}
}

func TestBuildInitCloneContainer_Resources(t *testing.T) {
	m := newTestManager()
	spec := minimalSpec()
	spec.GitURL = "https://github.com/example/repo.git"
	ic := m.buildInitCloneContainer(spec)

	cpuReq := ic.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "100m" {
		t.Errorf("init CPU request = %s, want 100m", cpuReq.String())
	}
	memLimit := ic.Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != "512Mi" {
		t.Errorf("init memory limit = %s, want 512Mi", memLimit.String())
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

