package podmanager

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// PodDefaults holds default pod template values that can be overridden
// at each level of the merge hierarchy:
//   Gasboat defaults < Project overrides < Mode overrides < AgentPool template
type PodDefaults struct {
	Image              string
	Resources          *corev1.ResourceRequirements
	ServiceAccountName string
	NodeSelector       map[string]string
	Tolerations        []corev1.Toleration
	Affinity           *corev1.Affinity
	Env                map[string]string
	SecretEnv          []SecretEnvSource
	ConfigMapName      string
	WorkspaceStorage   *WorkspaceStorageSpec
}

// ApplyDefaults applies PodDefaults to an AgentPodSpec, filling in
// any fields that aren't already set on the spec.
func ApplyDefaults(spec *AgentPodSpec, defaults *PodDefaults) {
	if defaults == nil {
		return
	}

	if spec.Image == "" && defaults.Image != "" {
		spec.Image = defaults.Image
	}
	if spec.Resources == nil && defaults.Resources != nil {
		spec.Resources = defaults.Resources
	}
	if spec.ServiceAccountName == "" && defaults.ServiceAccountName != "" {
		spec.ServiceAccountName = defaults.ServiceAccountName
	}
	if len(spec.NodeSelector) == 0 && len(defaults.NodeSelector) > 0 {
		spec.NodeSelector = defaults.NodeSelector
	}
	if len(spec.Tolerations) == 0 && len(defaults.Tolerations) > 0 {
		spec.Tolerations = defaults.Tolerations
	}
	if spec.Affinity == nil && defaults.Affinity != nil {
		spec.Affinity = defaults.Affinity
	}
	if spec.ConfigMapName == "" && defaults.ConfigMapName != "" {
		spec.ConfigMapName = defaults.ConfigMapName
	}
	if spec.WorkspaceStorage == nil && defaults.WorkspaceStorage != nil {
		spec.WorkspaceStorage = defaults.WorkspaceStorage
	}
	// Merge env maps (spec values take precedence over defaults).
	if len(defaults.Env) > 0 {
		if spec.Env == nil {
			spec.Env = make(map[string]string)
		}
		for k, v := range defaults.Env {
			if _, exists := spec.Env[k]; !exists {
				spec.Env[k] = v
			}
		}
	}

	// Append default secret env sources that aren't already in the spec.
	if len(defaults.SecretEnv) > 0 {
		existing := make(map[string]bool)
		for _, se := range spec.SecretEnv {
			existing[se.EnvName] = true
		}
		for _, se := range defaults.SecretEnv {
			if !existing[se.EnvName] {
				spec.SecretEnv = append(spec.SecretEnv, se)
			}
		}
	}
}

// DefaultPodDefaults returns sensible defaults for a given mode.
func DefaultPodDefaults(mode string) *PodDefaults {
	defaults := &PodDefaults{
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(DefaultCPURequest),
				corev1.ResourceMemory: resource.MustParse(DefaultMemoryRequest),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(DefaultCPULimit),
				corev1.ResourceMemory: resource.MustParse(DefaultMemoryLimit),
			},
		},
		// Agent image is x86-only; pin to amd64 nodes.
		NodeSelector: map[string]string{
			"kubernetes.io/arch": "amd64",
		},
		// Prefer on-demand, compute-optimized nodes; exclude per-MR nodes.
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
					{
						Weight: 100,
						Preference: corev1.NodeSelectorTerm{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "karpenter.sh/capacity-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"on-demand"},
							}},
						},
					},
					{
						Weight: 80,
						Preference: corev1.NodeSelectorTerm{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "karpenter.k8s.aws/instance-family",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"c5", "c5a", "c6i", "c6a", "c7i"},
							}},
						},
					},
				},
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      "per-mr",
							Operator: corev1.NodeSelectorOpDoesNotExist,
						}},
					}},
				},
			},
		},
	}

	switch mode {
	case "crew":
		// Crew pods get persistent workspace storage.
		defaults.WorkspaceStorage = &WorkspaceStorageSpec{
			Size:             "10Gi",
			StorageClassName: "", // set by AGENT_STORAGE_CLASS or project bead
		}
	case "job":
		// Jobs use EmptyDir (no WorkspaceStorage).
	}

	return defaults
}
