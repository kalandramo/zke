package resourcesummary

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	resourcehelper "k8s.io/component-helpers/resource"
)

func container(name string, cpu string, memory string) corev1.Container {
	return corev1.Container{
		Name: name,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
			},
		},
	}
}

func restartableInitContainer(name string, cpu string, memory string) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	init := container(name, cpu, memory)
	init.RestartPolicy = &always
	return init
}

func basePod(spec corev1.PodSpec, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "workload-0",
			UID:       "11111111-1111-4111-8111-111111111111",
		},
		Spec:   spec,
		Status: corev1.PodStatus{Phase: phase},
	}
}

// The projection must keep the reading Kubernetes itself uses for scheduling —
// which is the reading the Cluster overview has always reported — across every
// shape of Pod that changes the answer.
func TestProjectPodKeepsKubernetesRequestSemantics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		pod  *corev1.Pod
	}{
		{
			name: "plain containers add up",
			pod: basePod(corev1.PodSpec{
				Containers: []corev1.Container{
					container("a", "250m", "64Mi"),
					container("b", "500m", "128Mi"),
				},
			}, corev1.PodRunning),
		},
		{
			name: "an init container sets a floor rather than adding",
			pod: basePod(corev1.PodSpec{
				InitContainers: []corev1.Container{container("setup", "2", "1Gi")},
				Containers:     []corev1.Container{container("a", "250m", "64Mi")},
			}, corev1.PodRunning),
		},
		{
			name: "a restartable init container adds, because it keeps running",
			pod: basePod(corev1.PodSpec{
				InitContainers: []corev1.Container{
					restartableInitContainer("sidecar", "500m", "256Mi"),
				},
				Containers: []corev1.Container{container("a", "250m", "64Mi")},
			}, corev1.PodRunning),
		},
		{
			name: "overhead is part of what the Pod occupies",
			pod: basePod(corev1.PodSpec{
				Containers: []corev1.Container{container("a", "250m", "64Mi")},
				Overhead: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
			}, corev1.PodRunning),
		},
		{
			name: "Pod-level resources take over from the containers",
			pod: basePod(corev1.PodSpec{
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				Containers: []corev1.Container{container("a", "250m", "64Mi")},
			}, corev1.PodRunning),
		},
		{
			name: "no requests at all",
			pod: basePod(corev1.PodSpec{
				Containers: []corev1.Container{{Name: "a"}},
			}, corev1.PodRunning),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			expected := resourcehelper.PodRequests(
				testCase.pod,
				resourcehelper.PodResourcesOptions{},
			)
			cpu := expected[corev1.ResourceCPU]
			memory := expected[corev1.ResourceMemory]
			summary := ProjectPod(testCase.pod)
			if summary.CPURequestsMillis != cpu.MilliValue() {
				t.Fatalf("CPU = %dm, want %dm", summary.CPURequestsMillis, cpu.MilliValue())
			}
			if summary.MemoryRequestsBytes != memory.Value() {
				t.Fatalf("memory = %d, want %d", summary.MemoryRequestsBytes, memory.Value())
			}
		})
	}
}

// A Succeeded or Failed Pod holds nothing, so it contributes nothing — the same
// rule the overview has always applied.
func TestProjectPodExcludesTerminalPodsFromRequests(t *testing.T) {
	t.Parallel()
	for _, phase := range []corev1.PodPhase{corev1.PodSucceeded, corev1.PodFailed} {
		pod := basePod(corev1.PodSpec{
			Containers: []corev1.Container{container("a", "2", "1Gi")},
		}, phase)
		summary := ProjectPod(pod)
		if !summary.Terminal {
			t.Fatalf("phase %s was not treated as terminal", phase)
		}
		if summary.CPURequestsMillis != 0 || summary.MemoryRequestsBytes != 0 {
			t.Fatalf("a %s Pod contributed requests: %+v", phase, summary)
		}
	}

	// A Pod being deleted is still running until it is gone.
	deleting := basePod(corev1.PodSpec{
		Containers: []corev1.Container{container("a", "2", "1Gi")},
	}, corev1.PodRunning)
	deletion := metav1.NewTime(time.Now())
	deleting.DeletionTimestamp = &deletion
	summary := ProjectPod(deleting)
	if !summary.Terminating || summary.Terminal {
		t.Fatalf("a deleting Pod was projected as %+v", summary)
	}
	if summary.CPURequestsMillis != 2000 {
		t.Fatalf("a deleting Pod stopped contributing requests: %+v", summary)
	}
}

// The projection is also the boundary that keeps sensitive content out of a
// long-lived store. Nothing a page does not show may survive it.
func TestProjectionsCarryNoSensitiveContent(t *testing.T) {
	t.Parallel()
	pod := basePod(corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "a",
			Image: "registry.example.test/app:1",
			Env: []corev1.EnvVar{
				{Name: "PASSWORD", Value: "hunter2"},
				{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "api-credentials"},
						Key:                  "token",
					},
				}},
			},
			Args: []string{"--password=hunter2"},
		}},
		Volumes: []corev1.Volume{{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "application-config"},
				},
			},
		}},
	}, corev1.PodRunning)
	pod.Annotations = map[string]string{"checksum/config": "hunter2"}
	pod.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubectl"}}

	encoded, err := json.Marshal(ProjectPod(pod))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"hunter2",
		"api-credentials",
		"application-config",
		"managedFields",
		"annotations",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("the projection carries %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), "registry.example.test/app:1") {
		t.Fatal("the projection dropped the image the list shows")
	}
}

func TestProjectNamespaceReportsDeletionAsTerminating(t *testing.T) {
	t.Parallel()
	deletion := metav1.NewTime(time.Now())
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "old", DeletionTimestamp: &deletion},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
	if status := ProjectNamespace(namespace).Status; status != "terminating" {
		t.Fatalf("a deleting Namespace reported %q", status)
	}
	active := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
	if status := ProjectNamespace(active).Status; status != "active" {
		t.Fatalf("an active Namespace reported %q", status)
	}
}
