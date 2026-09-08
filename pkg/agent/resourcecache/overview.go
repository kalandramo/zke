package resourcecache

import (
	"sort"
	"strings"

	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
)

// MaxItemsPerSection is the product ceiling the overview has always applied.
//
// It is reported rather than enforced by truncation here: the cache holds what
// the Cluster has, and a section past the ceiling says so through
// SectionState.Truncated so the response can stay honest about a count nobody
// promised to complete.
const MaxItemsPerSection = 10_000

// OverviewSnapshot assembles what the cache currently knows.
//
// It copies small aggregates that the event stream maintains as objects arrive;
// it does not walk the cached objects, and it holds each section's lock only
// for the length of that copy. That is what makes repeated overview reads cost
// nothing on the Kubernetes side after the first listing.
func (instance *Cache) OverviewSnapshot() resourcesummary.OverviewSnapshot {
	snapshot := resourcesummary.OverviewSnapshot{
		GeneratedAt: instance.now().UTC(),
		CacheSynced: true,
		Sections:    make([]resourcesummary.SectionState, 0, len(instance.order)),
		Nodes: resourcesummary.NodeTotals{
			StatusCounts:       map[string]int64{},
			KubernetesVersions: map[string]int64{},
		},
		Namespaces: resourcesummary.NamespaceTotals{StatusCounts: map[string]int64{}},
		Pods:       resourcesummary.PodTotals{StatusCounts: map[string]int64{}},
		Workloads:  make([]resourcesummary.WorkloadTotals, 0, 5),
		Storage: resourcesummary.StorageTotals{
			PersistentVolumes:      resourcesummary.PersistentVolumeTotals{StatusCounts: map[string]int64{}},
			PersistentVolumeClaims: resourcesummary.PersistentVolumeClaimTotals{StatusCounts: map[string]int64{}},
		},
	}
	for _, gvr := range instance.order {
		target := instance.stores[gvr]
		totals, health := target.snapshotTotals()
		state := resourcesummary.SectionState{
			Section:         target.kind.section,
			Synced:          health.Synced,
			ResourceVersion: health.ResourceVersion,
			LastSuccessAt:   health.LastSuccessAt,
			LastEventAt:     health.LastEventAt,
			WatchRestarts:   health.WatchRestarts,
			Observed:        health.Observed,
			Code:            health.FailureCode,
			Truncated:       health.Observed > MaxItemsPerSection,
		}
		absent := instance.unavailable(gvr)
		switch {
		case absent:
			// A Cluster without this API is not a Cluster with none of these
			// objects. The section reports why it is empty instead of counting
			// zero.
			state.Synced = false
		case !health.Synced:
			state.Code = "warming_up"
		}
		if !state.Synced {
			snapshot.CacheSynced = false
			snapshot.Stale = true
			snapshot.Sections = append(snapshot.Sections, state)
			continue
		}
		if state.Code != "" {
			// Synchronised once, but currently failing: the numbers below are
			// the last ones observed, which is what stale means here.
			snapshot.Stale = true
		}
		if health.LastSuccessAt.After(snapshot.LastSuccessAt) {
			snapshot.LastSuccessAt = health.LastSuccessAt
		}
		mergeSection(&snapshot, target.kind.section, totals)
		snapshot.Sections = append(snapshot.Sections, state)
	}
	sort.Slice(snapshot.Sections, func(left, right int) bool {
		return snapshot.Sections[left].Section < snapshot.Sections[right].Section
	})
	sort.Slice(snapshot.Workloads, func(left, right int) bool {
		return snapshot.Workloads[left].Resource < snapshot.Workloads[right].Resource
	})
	return snapshot
}

func mergeSection(
	snapshot *resourcesummary.OverviewSnapshot,
	section string,
	totals sectionTotals,
) {
	switch section {
	case SectionNodes:
		snapshot.Nodes = resourcesummary.NodeTotals{
			Total:              totals.total,
			Unschedulable:      totals.unschedulable,
			StatusCounts:       totals.statusCounts,
			KubernetesVersions: totals.versionCounts,
		}
		snapshot.Resources.CPUCapacityMillis = totals.cpuCapacityMillis
		snapshot.Resources.CPUAllocatableMillis = totals.cpuAllocatableMillis
		snapshot.Resources.MemoryCapacityBytes = totals.memoryCapacityBytes
		snapshot.Resources.MemoryAllocatableBytes = totals.memoryAllocatableBytes
		snapshot.Resources.PodCapacity = totals.podCapacity
		snapshot.Resources.PodAllocatable = totals.podAllocatable
	case SectionNamespaces:
		snapshot.Namespaces = resourcesummary.NamespaceTotals{
			Total:        totals.total,
			StatusCounts: totals.statusCounts,
		}
	case SectionPods:
		snapshot.Pods = resourcesummary.PodTotals{
			Total:        totals.total,
			Ready:        totals.ready,
			NotReady:     totals.notReady,
			Terminating:  totals.terminating,
			StatusCounts: totals.statusCounts,
		}
		snapshot.Resources.CPURequestedMillis = totals.cpuRequestedMillis
		snapshot.Resources.MemoryRequestedBytes = totals.memoryRequestedBytes
		snapshot.Resources.NonTerminalPods = totals.nonTerminal
	case SectionPersistentVolumes:
		snapshot.Storage.PersistentVolumes = resourcesummary.PersistentVolumeTotals{
			Total:         totals.total,
			CapacityBytes: totals.storageCapacityBytes,
			StatusCounts:  totals.statusCounts,
		}
	case SectionPersistentVolumeClaims:
		snapshot.Storage.PersistentVolumeClaims = resourcesummary.PersistentVolumeClaimTotals{
			Total:          totals.total,
			RequestedBytes: totals.storageRequestedBytes,
			StatusCounts:   totals.statusCounts,
		}
	default:
		if resource, found := strings.CutPrefix(section, sectionWorkloadPrefix); found {
			snapshot.Workloads = append(snapshot.Workloads, resourcesummary.WorkloadTotals{
				Resource:     resource,
				Total:        totals.total,
				StatusCounts: totals.statusCounts,
			})
		}
	}
}
