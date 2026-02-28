package podmanager

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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
