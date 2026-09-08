// Package resourcesummary is the compact read model the Agent keeps for the
// Kubernetes resources the container service lists, and the shape those
// resources travel in between Agent and Server.
//
// It is deliberately not a Kubernetes object. A summary holds what the built-in
// pages read and nothing else: no managedFields, no annotations, no Secret or
// ConfigMap bodies, no status detail a list never shows. That is what makes a
// cache of ten thousand Pods affordable, and it is also the boundary that keeps
// sensitive content out of a long-lived in-memory store.
//
// Details, YAML and every write keep going to the Kubernetes API Server. These
// types answer lists and counts; they are never the source of truth for an
// object a user is about to edit.
package resourcesummary

import "time"

// ObjectSummary is the identity every cached object carries.
type ObjectSummary struct {
	UID       string `json:"uid"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// ResourceVersion of the object this summary was projected from. It is the
	// Kubernetes concurrency token of that one object, not of the cache.
	ResourceVersion string `json:"resource_version"`
	// Labels are kept because the built-in pages filter on them and because a
	// label selector must be answerable from the cache. Annotations are not:
	// they routinely carry whole configurations, and no list shows them.
	Labels            map[string]string `json:"labels,omitempty"`
	CreationTimestamp time.Time         `json:"creation_timestamp"`
	DeletionTimestamp *time.Time        `json:"deletion_timestamp,omitempty"`
}

type OwnerReference struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller bool   `json:"controller"`
}

// Pod is the projection the Pod list, the workload Pod tab and the Cluster
// overview all read.
type Pod struct {
	ObjectSummary
	NodeName string `json:"node_name"`
	PodIP    string `json:"pod_ip"`
	Phase    string `json:"phase"`
	Reason   string `json:"reason"`
	Ready    bool   `json:"ready"`
	// Terminating is a Pod with a deletion timestamp: it still occupies its
	// Node, so it is counted separately rather than removed early.
	Terminating bool `json:"terminating"`
	// Terminal is Succeeded or Failed. A terminal Pod holds no resources, which
	// is why it does not contribute requests.
	Terminal     bool            `json:"terminal"`
	RestartCount int64           `json:"restart_count"`
	Images       []string        `json:"images"`
	Controller   *OwnerReference `json:"controller,omitempty"`
	// Requests as Kubernetes itself computes them for scheduling, including
	// init containers, restartable init containers, Pod-level resources and
	// overhead. Zero for a terminal Pod.
	CPURequestsMillis   int64 `json:"cpu_requests_millis"`
	MemoryRequestsBytes int64 `json:"memory_requests_bytes"`
}

// Node keeps the quantities as Kubernetes reports them, as strings, because
// that is the form the Node list already presents. The numbers the overview
// adds up are derived once at projection time and travel separately.
type Node struct {
	ObjectSummary
	Status            string   `json:"status"`
	Unschedulable     bool     `json:"unschedulable"`
	Roles             []string `json:"roles"`
	InternalIP        string   `json:"internal_ip"`
	KubernetesVersion string   `json:"kubernetes_version"`
	OperatingSystem   string   `json:"operating_system"`
	OSImage           string   `json:"os_image"`
	KernelVersion     string   `json:"kernel_version"`
	ContainerRuntime  string   `json:"container_runtime"`
	CPUCapacity       string   `json:"cpu_capacity"`
	MemoryCapacity    string   `json:"memory_capacity"`
	PodsCapacity      string   `json:"pods_capacity"`
	CPUAllocatable    string   `json:"cpu_allocatable"`
	MemoryAllocatable string   `json:"memory_allocatable"`
	PodsAllocatable   string   `json:"pods_allocatable"`
	// Taints as a count and their keys, not the full objects: the list shows
	// whether a Node is tainted, and the detail request shows which.
	TaintCount int      `json:"taint_count"`
	TaintKeys  []string `json:"taint_keys,omitempty"`
}

type Namespace struct {
	ObjectSummary
	// Phase as Kubernetes reports it, lowered, with a deleting Namespace
	// reported as terminating whatever its stored phase says.
	Status string `json:"status"`
}

// Workload covers the five controller kinds the workload list shows. One type
// rather than five keeps the query, sort and pagination path single.
type Workload struct {
	ObjectSummary
	// Resource is the plural name: deployments, statefulsets, daemonsets, jobs,
	// cronjobs.
	Resource           string            `json:"resource"`
	Generation         int64             `json:"generation"`
	ObservedGeneration int64             `json:"observed_generation"`
	Status             string            `json:"status"`
	Images             []string          `json:"images"`
	Replicas           *WorkloadReplicas `json:"replicas,omitempty"`
	Job                *WorkloadJob      `json:"job,omitempty"`
	CronJob            *WorkloadCronJob  `json:"cron_job,omitempty"`
}

type WorkloadReplicas struct {
	Desired     int32 `json:"desired"`
	Current     int32 `json:"current"`
	Updated     int32 `json:"updated"`
	Ready       int32 `json:"ready"`
	Available   int32 `json:"available"`
	Unavailable int32 `json:"unavailable"`
}

type WorkloadJob struct {
	Parallelism    *int32     `json:"parallelism,omitempty"`
	Completions    *int32     `json:"completions,omitempty"`
	Active         int32      `json:"active"`
	Succeeded      int32      `json:"succeeded"`
	Failed         int32      `json:"failed"`
	StartTime      *time.Time `json:"start_time,omitempty"`
	CompletionTime *time.Time `json:"completion_time,omitempty"`
}

type WorkloadCronJob struct {
	Schedule           string     `json:"schedule"`
	Suspend            bool       `json:"suspend"`
	Active             int        `json:"active"`
	LastScheduleTime   *time.Time `json:"last_schedule_time,omitempty"`
	LastSuccessfulTime *time.Time `json:"last_successful_time,omitempty"`
}

type PersistentVolume struct {
	ObjectSummary
	Status        string   `json:"status"`
	CapacityBytes int64    `json:"capacity_bytes"`
	Capacity      string   `json:"capacity"`
	AccessModes   []string `json:"access_modes"`
	ReclaimPolicy string   `json:"reclaim_policy"`
	StorageClass  string   `json:"storage_class"`
	ClaimRef      string   `json:"claim_ref,omitempty"`
	VolumeMode    string   `json:"volume_mode,omitempty"`
}

type PersistentVolumeClaim struct {
	ObjectSummary
	Status         string   `json:"status"`
	RequestedBytes int64    `json:"requested_bytes"`
	Requested      string   `json:"requested"`
	CapacityBytes  int64    `json:"capacity_bytes"`
	Capacity       string   `json:"capacity"`
	AccessModes    []string `json:"access_modes"`
	StorageClass   string   `json:"storage_class"`
	VolumeName     string   `json:"volume_name,omitempty"`
	VolumeMode     string   `json:"volume_mode,omitempty"`
}
