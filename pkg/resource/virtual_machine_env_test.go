package resource_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	"github.com/agoda-com/macOS-vz-kubelet/pkg/resource"
)

func TestAddEnvDoesNotModifyPodEnv(t *testing.T) {
	podEnv := make([]corev1.EnvVar, 1, 4) // spare capacity: a plain append would write into it
	podEnv[0] = corev1.EnvVar{Name: "A", Value: "1"}

	m := resource.NewMacOSVirtualMachine(podEnv)
	m.AddEnv(corev1.EnvVar{Name: "MACOS_VZ_SLOT", Value: "2"})

	assert.Equal(t, []corev1.EnvVar{{Name: "A", Value: "1"}, {Name: "MACOS_VZ_SLOT", Value: "2"}}, m.Env())
	assert.Len(t, podEnv, 1)
	assert.Equal(t, "", podEnv[:2][1].Name, "pod env's backing array must be untouched")
}
