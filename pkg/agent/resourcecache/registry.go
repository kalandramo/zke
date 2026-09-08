package resourcecache

import (
	"fmt"
	"strings"

	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Section names, which are also the names the Cluster overview already uses for
// its parts. Keeping them identical is what lets a Server compare a snapshot
// from an Agent cache against the ten listings it used to do.
const (
	SectionNodes                  = "nodes"
	SectionNamespaces             = "namespaces"
	SectionPods                   = "pods"
	SectionPersistentVolumes      = "storage.persistentvolumes"
	SectionPersistentVolumeClaims = "storage.persistentvolumeclaims"
	sectionWorkloadPrefix         = "workloads."
)

// resourceKind is one cached resource: what to watch, what to call the part of
// the overview it feeds, and how to reduce its objects to something small.
type resourceKind struct {
	gvr     schema.GroupVersionResource
	section string
	// namespaced decides whether a query may filter by Namespace, and is what
	// the reflector's ListerWatcher scopes on.
	namespaced bool
	// optional marks a resource that a Cluster may legitimately not have. Its
	// absence leaves one section unavailable; it never fails the cache.
	optional bool
	project  func(*unstructured.Unstructured) (entry, error)
}

// CoreResources are the resources the container service reads on nearly every
// page: the Cluster overview needs all ten, and the Node, Namespace, Pod and
// workload lists read the same ten.
//
// This list is deliberately closed. A cache that followed every CRD in a
// Cluster would have unbounded memory and would be watching objects no page
// ever shows.
func CoreResources() []resourceKind {
	return []resourceKind{
		{
			gvr:     schema.GroupVersionResource{Version: "v1", Resource: "nodes"},
			section: SectionNodes,
			project: projectTyped(func(node *corev1.Node) (any, contribution) {
				summary := resourcesummary.ProjectNode(node)
				return summary, nodeContribution(node, summary)
			}),
		},
		{
			gvr:     schema.GroupVersionResource{Version: "v1", Resource: "namespaces"},
			section: SectionNamespaces,
			project: projectTyped(func(namespace *corev1.Namespace) (any, contribution) {
				summary := resourcesummary.ProjectNamespace(namespace)
				return summary, contribution{status: summary.Status}
			}),
		},
		{
			gvr:        schema.GroupVersionResource{Version: "v1", Resource: "pods"},
			section:    SectionPods,
			namespaced: true,
			project: projectTyped(func(pod *corev1.Pod) (any, contribution) {
				summary := resourcesummary.ProjectPod(pod)
				return summary, podContribution(summary)
			}),
		},
		{
			gvr:        schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			section:    sectionWorkloadPrefix + resourcesummary.WorkloadDeployments,
			namespaced: true,
			project: projectTyped(func(workload *appsv1.Deployment) (any, contribution) {
				summary := resourcesummary.ProjectDeployment(workload)
				return summary, contribution{status: summary.Status}
			}),
		},
		{
			gvr:        schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"},
			section:    sectionWorkloadPrefix + resourcesummary.WorkloadStatefulSets,
			namespaced: true,
			project: projectTyped(func(workload *appsv1.StatefulSet) (any, contribution) {
				summary := resourcesummary.ProjectStatefulSet(workload)
				return summary, contribution{status: summary.Status}
			}),
		},
		{
			gvr:        schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"},
			section:    sectionWorkloadPrefix + resourcesummary.WorkloadDaemonSets,
			namespaced: true,
			project: projectTyped(func(workload *appsv1.DaemonSet) (any, contribution) {
				summary := resourcesummary.ProjectDaemonSet(workload)
				return summary, contribution{status: summary.Status}
			}),
		},
		{
			gvr:        schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"},
			section:    sectionWorkloadPrefix + resourcesummary.WorkloadJobs,
			namespaced: true,
			project: projectTyped(func(workload *batchv1.Job) (any, contribution) {
				summary := resourcesummary.ProjectJob(workload)
				return summary, contribution{status: summary.Status}
			}),
		},
		{
			gvr:        schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"},
			section:    sectionWorkloadPrefix + resourcesummary.WorkloadCronJobs,
			namespaced: true,
			project: projectTyped(func(workload *batchv1.CronJob) (any, contribution) {
				summary := resourcesummary.ProjectCronJob(workload)
				return summary, contribution{status: summary.Status}
			}),
		},
		{
			gvr:     schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"},
			section: SectionPersistentVolumes,
			project: projectTyped(func(volume *corev1.PersistentVolume) (any, contribution) {
				summary := resourcesummary.ProjectPersistentVolume(volume)
				return summary, contribution{
					status:               summary.Status,
					storageCapacityBytes: summary.CapacityBytes,
				}
			}),
		},
		{
			gvr:        schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"},
			section:    SectionPersistentVolumeClaims,
			namespaced: true,
			project: projectTyped(func(claim *corev1.PersistentVolumeClaim) (any, contribution) {
				summary := resourcesummary.ProjectPersistentVolumeClaim(claim)
				return summary, contribution{
					status:                summary.Status,
					storageRequestedBytes: summary.RequestedBytes,
				}
			}),
		},
	}
}

func nodeContribution(node *corev1.Node, summary resourcesummary.Node) contribution {
	capacity := node.Status.Capacity
	allocatable := node.Status.Allocatable
	cpuCapacity := capacity[corev1.ResourceCPU]
	cpuAllocatable := allocatable[corev1.ResourceCPU]
	memoryCapacity := capacity[corev1.ResourceMemory]
	memoryAllocatable := allocatable[corev1.ResourceMemory]
	podCapacity := capacity[corev1.ResourcePods]
	podAllocatable := allocatable[corev1.ResourcePods]
	return contribution{
		status:                 summary.Status,
		kubernetesVersion:      summary.KubernetesVersion,
		unschedulable:          summary.Unschedulable,
		cpuCapacityMillis:      nonNegativeMillis(cpuCapacity.MilliValue(), cpuCapacity.Sign()),
		cpuAllocatableMillis:   nonNegativeMillis(cpuAllocatable.MilliValue(), cpuAllocatable.Sign()),
		memoryCapacityBytes:    nonNegativeMillis(memoryCapacity.Value(), memoryCapacity.Sign()),
		memoryAllocatableBytes: nonNegativeMillis(memoryAllocatable.Value(), memoryAllocatable.Sign()),
		podCapacity:            nonNegativeMillis(podCapacity.Value(), podCapacity.Sign()),
		podAllocatable:         nonNegativeMillis(podAllocatable.Value(), podAllocatable.Sign()),
	}
}

func nonNegativeMillis(value int64, sign int) int64 {
	if sign < 0 {
		return 0
	}
	return value
}

// podContribution keeps the overview's long-standing reading of a Pod: its
// phase is always counted, a Pod being deleted is counted as terminating on top
// of that, and only a Pod that is neither Succeeded nor Failed contributes
// requests or counts towards ready and not-ready.
func podContribution(summary resourcesummary.Pod) contribution {
	return contribution{
		status:               strings.ToLower(summary.Phase),
		ready:                summary.Ready,
		notReady:             !summary.Ready && !summary.Terminal,
		terminating:          summary.Terminating,
		nonTerminal:          !summary.Terminal,
		cpuRequestedMillis:   summary.CPURequestsMillis,
		memoryRequestedBytes: summary.MemoryRequestsBytes,
	}
}

// projectTyped converts once, from the unstructured object the dynamic client
// delivers into the typed object the projection is written against, and then
// drops both. Going through the typed object is deliberate: it is how the
// projection can use the same Kubernetes helpers the Server uses today, so the
// numbers cannot quietly drift from the ones the overview has always reported.
func projectTyped[T any](
	project func(*T) (any, contribution),
) func(*unstructured.Unstructured) (entry, error) {
	return func(object *unstructured.Unstructured) (entry, error) {
		var typed T
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(
			object.Object,
			&typed,
		); err != nil {
			return entry{}, fmt.Errorf("%w: %w", errUnexpectedObject, err)
		}
		summary, value := project(&typed)
		identity := summaryIdentity(summary)
		if identity.Name == "" {
			return entry{}, fmt.Errorf("%w: cached object has no name", errUnexpectedObject)
		}
		return entry{
			key:          objectKey{namespace: identity.Namespace, name: identity.Name},
			uid:          identity.UID,
			labels:       identity.Labels,
			createdAt:    identity.CreationTimestamp,
			summary:      summary,
			contribution: value,
		}, nil
	}
}

// summaryIdentity reads the identity every summary carries. The type switch is
// exhaustive over the projections above; a summary that reaches here without
// one is a programming error, and it fails the object rather than caching
// something the query path could not address.
func summaryIdentity(summary any) resourcesummary.ObjectSummary {
	switch typed := summary.(type) {
	case resourcesummary.Node:
		return typed.ObjectSummary
	case resourcesummary.Namespace:
		return typed.ObjectSummary
	case resourcesummary.Pod:
		return typed.ObjectSummary
	case resourcesummary.Workload:
		return typed.ObjectSummary
	case resourcesummary.PersistentVolume:
		return typed.ObjectSummary
	case resourcesummary.PersistentVolumeClaim:
		return typed.ObjectSummary
	default:
		return resourcesummary.ObjectSummary{}
	}
}

func summaryResourceVersion(summary any) string {
	if version, stripped := strippedResourceVersion(summary); stripped {
		return version
	}
	return summaryIdentity(summary).ResourceVersion
}
