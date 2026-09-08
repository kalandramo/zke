package resourcesummary

import "time"

// OverviewSnapshot is what an Agent's resource cache can say about a Cluster
// without reading it again.
//
// It is a converged view, not an atomic one. The ten resources it summarises
// have no ResourceVersion in common — Kubernetes has no such thing across
// GroupVersions — so each section reports its own, and the snapshot as a whole
// says when it was produced rather than claiming to be a single instant.
type OverviewSnapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	// CacheSynced is true only when every section this Cluster has completed
	// its first listing. Until then some sections know nothing, and saying so
	// is the point: an unsynchronised section reported as zero is a confident
	// wrong number.
	CacheSynced bool `json:"cache_synced"`
	// Stale marks a snapshot whose sections are not all currently healthy.
	Stale bool `json:"stale"`
	// LastSuccessAt is the most recent moment any section observed Kubernetes
	// successfully. Zero when none ever has.
	LastSuccessAt time.Time        `json:"last_success_at"`
	Sections      []SectionState   `json:"sections"`
	Nodes         NodeTotals       `json:"nodes"`
	Namespaces    NamespaceTotals  `json:"namespaces"`
	Pods          PodTotals        `json:"pods"`
	Workloads     []WorkloadTotals `json:"workloads"`
	Storage       StorageTotals    `json:"storage"`
	Resources     ResourceTotals   `json:"resources"`
}

// SectionState is one cached resource's own account of itself. A reader that
// wants to know whether a number can be trusted reads this, not the number.
type SectionState struct {
	Section string `json:"section"`
	Synced  bool   `json:"synced"`
	// ResourceVersion of this section alone. It is the Kubernetes token for
	// this resource, and is not comparable with another section's.
	ResourceVersion string    `json:"resource_version"`
	LastSuccessAt   time.Time `json:"last_success_at"`
	LastEventAt     time.Time `json:"last_event_at"`
	WatchRestarts   int64     `json:"watch_restarts"`
	Observed        int64     `json:"observed"`
	// Code is empty while the section is healthy. Otherwise it is a short,
	// stable reason — warming_up, resource_absent, cluster_api_forbidden — and
	// never a message from the API Server, which can quote object content.
	Code string `json:"code,omitempty"`
	// Truncated marks a section that holds more objects than the product counts.
	Truncated bool `json:"truncated"`
}

type NodeTotals struct {
	Total              int64            `json:"total"`
	Unschedulable      int64            `json:"unschedulable"`
	StatusCounts       map[string]int64 `json:"status_counts"`
	KubernetesVersions map[string]int64 `json:"kubernetes_versions"`
}

type NamespaceTotals struct {
	Total        int64            `json:"total"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

type PodTotals struct {
	Total        int64            `json:"total"`
	Ready        int64            `json:"ready"`
	NotReady     int64            `json:"not_ready"`
	Terminating  int64            `json:"terminating"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

type WorkloadTotals struct {
	Resource     string           `json:"resource"`
	Total        int64            `json:"total"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

type StorageTotals struct {
	PersistentVolumes      PersistentVolumeTotals      `json:"persistent_volumes"`
	PersistentVolumeClaims PersistentVolumeClaimTotals `json:"persistent_volume_claims"`
}

type PersistentVolumeTotals struct {
	Total         int64            `json:"total"`
	CapacityBytes int64            `json:"capacity_bytes"`
	StatusCounts  map[string]int64 `json:"status_counts"`
}

type PersistentVolumeClaimTotals struct {
	Total          int64            `json:"total"`
	RequestedBytes int64            `json:"requested_bytes"`
	StatusCounts   map[string]int64 `json:"status_counts"`
}

type ResourceTotals struct {
	CPUCapacityMillis      int64 `json:"cpu_capacity_millis"`
	CPUAllocatableMillis   int64 `json:"cpu_allocatable_millis"`
	CPURequestedMillis     int64 `json:"cpu_requested_millis"`
	MemoryCapacityBytes    int64 `json:"memory_capacity_bytes"`
	MemoryAllocatableBytes int64 `json:"memory_allocatable_bytes"`
	MemoryRequestedBytes   int64 `json:"memory_requested_bytes"`
	PodCapacity            int64 `json:"pod_capacity"`
	PodAllocatable         int64 `json:"pod_allocatable"`
	NonTerminalPods        int64 `json:"non_terminal_pods"`
}
