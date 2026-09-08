package clusteroverview

import (
	"context"
	"errors"
	"sort"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
)

// SnapshotReader is an Agent that keeps a resource cache and can answer the
// overview from it.
//
// It is optional. A Server whose Agents predate the cache never gets one, and
// every Cluster keeps being read the way it always was.
type SnapshotReader interface {
	ClusterOverviewSnapshot(
		ctx context.Context,
		clusterID string,
	) (resourcesummary.OverviewSnapshot, error)
}

// The reason a section reports nothing, in the vocabulary the overview already
// uses for its issues.
const (
	sectionWarmingUp    = "warming_up"
	issueCacheWarmingUp = "cache_warming_up"
	issueItemLimit      = "item_limit_reached"
)

// snapshotOverview asks the Agent for the overview it already holds.
//
// The second return value says whether this path produced an answer at all. It
// is false when the Agent cannot answer — it predates the cache, or its cache
// has not finished its first listing — and the caller then reads the Cluster
// the way it always has. That is what lets a Server be upgraded ahead of its
// Agents, and what keeps a warming cache from putting zeroes on the page.
func (service *Service) snapshotOverview(
	ctx context.Context,
	clusterID string,
) (Overview, bool, error) {
	if service.snapshots == nil {
		return Overview{}, false, nil
	}
	snapshot, err := service.snapshots.ClusterOverviewSnapshot(ctx, clusterID)
	if err != nil {
		if errors.Is(err, kubernetesresource.ErrAgentUnsupported) {
			return Overview{}, false, nil
		}
		return Overview{}, false, err
	}
	if warming(snapshot) {
		// The cache knows it does not know yet. Reading the Cluster directly is
		// both correct and brief: it lasts until the first listing completes.
		return Overview{}, false, nil
	}
	return overviewFromSnapshot(snapshot), true, nil
}

func warming(snapshot resourcesummary.OverviewSnapshot) bool {
	for _, section := range snapshot.Sections {
		if section.Code == sectionWarmingUp {
			return true
		}
	}
	return false
}

// overviewFromSnapshot translates the Agent's read model into the response the
// Cluster overview has always returned.
//
// A section the cache could not read becomes an issue and leaves its numbers
// out, which is exactly what a failed listing does on the other path — the two
// paths therefore report an unavailable section the same way.
func overviewFromSnapshot(snapshot resourcesummary.OverviewSnapshot) Overview {
	overview := Overview{
		GeneratedAt: snapshot.GeneratedAt,
		Stale:       snapshot.Stale,
		Issues:      make([]SectionIssue, 0, len(snapshot.Sections)),
		Nodes: NodeOverview{
			StatusCounts:       counts(snapshot.Nodes.StatusCounts),
			KubernetesVersions: counts(snapshot.Nodes.KubernetesVersions),
			Total:              snapshot.Nodes.Total,
			Unschedulable:      snapshot.Nodes.Unschedulable,
		},
		Namespaces: NamespaceOverview{
			Total:        snapshot.Namespaces.Total,
			StatusCounts: counts(snapshot.Namespaces.StatusCounts),
		},
		Pods: PodOverview{
			Total:        snapshot.Pods.Total,
			Ready:        snapshot.Pods.Ready,
			NotReady:     snapshot.Pods.NotReady,
			Terminating:  snapshot.Pods.Terminating,
			StatusCounts: counts(snapshot.Pods.StatusCounts),
		},
		Workloads: WorkloadOverview{
			StatusCounts: map[string]int64{},
			ByResource:   make([]WorkloadResourceOverview, 0, len(snapshot.Workloads)),
		},
		Storage: StorageOverview{
			PersistentVolumes: PersistentVolumeOverview{
				Total:         snapshot.Storage.PersistentVolumes.Total,
				CapacityBytes: snapshot.Storage.PersistentVolumes.CapacityBytes,
				StatusCounts:  counts(snapshot.Storage.PersistentVolumes.StatusCounts),
			},
			PersistentVolumeClaims: PersistentVolumeClaimOverview{
				Total:          snapshot.Storage.PersistentVolumeClaims.Total,
				RequestedBytes: snapshot.Storage.PersistentVolumeClaims.RequestedBytes,
				StatusCounts:   counts(snapshot.Storage.PersistentVolumeClaims.StatusCounts),
			},
		},
		Resources: ClusterResourceTotals{
			CPUCapacityMillis:      snapshot.Resources.CPUCapacityMillis,
			CPUAllocatableMillis:   snapshot.Resources.CPUAllocatableMillis,
			CPURequestedMillis:     snapshot.Resources.CPURequestedMillis,
			MemoryCapacityBytes:    snapshot.Resources.MemoryCapacityBytes,
			MemoryAllocatableBytes: snapshot.Resources.MemoryAllocatableBytes,
			MemoryRequestedBytes:   snapshot.Resources.MemoryRequestedBytes,
			PodCapacity:            snapshot.Resources.PodCapacity,
			PodAllocatable:         snapshot.Resources.PodAllocatable,
			NonTerminalPods:        snapshot.Resources.NonTerminalPods,
		},
	}
	for _, totals := range snapshot.Workloads {
		entry := WorkloadResourceOverview{
			Resource:     totals.Resource,
			Total:        totals.Total,
			StatusCounts: counts(totals.StatusCounts),
		}
		overview.Workloads.ByResource = append(overview.Workloads.ByResource, entry)
		overview.Workloads.Total += entry.Total
		mergeStatusCounts(overview.Workloads.StatusCounts, entry.StatusCounts)
	}
	sort.Slice(overview.Workloads.ByResource, func(left, right int) bool {
		return overview.Workloads.ByResource[left].Resource <
			overview.Workloads.ByResource[right].Resource
	})
	for _, section := range snapshot.Sections {
		switch {
		case !section.Synced || section.Code != "":
			overview.Issues = append(overview.Issues, SectionIssue{
				Section: section.Section,
				Code:    sectionIssueCode(section),
			})
		case section.Truncated:
			overview.Issues = append(overview.Issues, SectionIssue{
				Section: section.Section,
				Code:    issueItemLimit,
			})
		}
	}
	sort.Slice(overview.Issues, func(left, right int) bool {
		return overview.Issues[left].Section < overview.Issues[right].Section
	})
	overview.Partial = len(overview.Issues) > 0
	return overview
}

// sectionIssueCode keeps the codes the Cluster overview already publishes. The
// cache reports a few of its own — an API this Cluster does not have, a watch
// that cannot be resumed — and those pass through unchanged rather than being
// flattened into a generic failure.
func sectionIssueCode(section resourcesummary.SectionState) string {
	if section.Code == "" {
		return issueCacheWarmingUp
	}
	if section.Code == sectionWarmingUp {
		return issueCacheWarmingUp
	}
	return section.Code
}

func counts(source map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(source))
	for key, value := range source {
		if value > 0 {
			result[key] = value
		}
	}
	return result
}
