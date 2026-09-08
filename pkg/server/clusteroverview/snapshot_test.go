package clusteroverview

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
)

// snapshotReader is an Agent that keeps a resource cache. It counts its calls
// so a test can tell a snapshot read from ten Cluster listings.
type snapshotReader struct {
	*fakeResourceReader
	snapshot resourcesummary.OverviewSnapshot
	err      error
	calls    atomic.Int64
}

func (reader *snapshotReader) ClusterOverviewSnapshot(
	context.Context,
	string,
) (resourcesummary.OverviewSnapshot, error) {
	reader.calls.Add(1)
	if reader.err != nil {
		return resourcesummary.OverviewSnapshot{}, reader.err
	}
	return reader.snapshot, nil
}

func healthySections(codes map[string]string) []resourcesummary.SectionState {
	sections := []string{
		"nodes", "namespaces", "pods",
		"storage.persistentvolumes", "storage.persistentvolumeclaims",
		"workloads.cronjobs", "workloads.daemonsets", "workloads.deployments",
		"workloads.jobs", "workloads.statefulsets",
	}
	states := make([]resourcesummary.SectionState, 0, len(sections))
	for _, section := range sections {
		state := resourcesummary.SectionState{
			Section:         section,
			Synced:          true,
			ResourceVersion: "1000",
			LastSuccessAt:   time.Now().UTC(),
		}
		if code, failing := codes[section]; failing {
			state.Code = code
			if code == "resource_absent" {
				state.Synced = false
			}
		}
		states = append(states, state)
	}
	return states
}

// A Cluster whose Agent keeps a cache is read from that cache: no Cluster
// listing at all, however many times the page is opened.
func TestOverviewPrefersTheAgentSnapshot(t *testing.T) {
	t.Parallel()
	reader := &snapshotReader{
		fakeResourceReader: &fakeResourceReader{},
		snapshot: resourcesummary.OverviewSnapshot{
			GeneratedAt:   time.Now().UTC(),
			CacheSynced:   true,
			LastSuccessAt: time.Now().UTC(),
			Sections:      healthySections(nil),
			Nodes: resourcesummary.NodeTotals{
				Total:              2,
				Unschedulable:      1,
				StatusCounts:       map[string]int64{"ready": 1, "not_ready": 1},
				KubernetesVersions: map[string]int64{"v1.31.4": 1, "v1.30.8": 1},
			},
			Namespaces: resourcesummary.NamespaceTotals{
				Total:        2,
				StatusCounts: map[string]int64{"active": 1, "terminating": 1},
			},
			Pods: resourcesummary.PodTotals{
				Total:        3,
				Ready:        1,
				NotReady:     1,
				StatusCounts: map[string]int64{"running": 2, "succeeded": 1},
			},
			Workloads: []resourcesummary.WorkloadTotals{
				{Resource: "deployments", Total: 2, StatusCounts: map[string]int64{"available": 2}},
				{Resource: "jobs", Total: 1, StatusCounts: map[string]int64{"complete": 1}},
			},
			Storage: resourcesummary.StorageTotals{
				PersistentVolumes: resourcesummary.PersistentVolumeTotals{
					Total: 2, CapacityBytes: 32212254720,
					StatusCounts: map[string]int64{"bound": 1, "released": 1},
				},
				PersistentVolumeClaims: resourcesummary.PersistentVolumeClaimTotals{
					Total: 2, RequestedBytes: 26843545600,
					StatusCounts: map[string]int64{"bound": 1, "pending": 1},
				},
			},
			Resources: resourcesummary.ResourceTotals{
				CPUCapacityMillis: 6000, CPUAllocatableMillis: 5300,
				CPURequestedMillis: 750, MemoryRequestedBytes: 167772160,
				PodCapacity: 220, PodAllocatable: 200, NonTerminalPods: 2,
			},
		},
	}
	service := NewService(reader, Config{CacheTTL: -1})

	overview, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Partial || len(overview.Issues) != 0 {
		t.Fatalf("a healthy snapshot produced issues: %+v", overview.Issues)
	}
	if overview.Nodes.Total != 2 || overview.Pods.Total != 3 ||
		overview.Workloads.Total != 3 ||
		overview.Resources.CPURequestedMillis != 750 ||
		overview.Storage.PersistentVolumes.CapacityBytes != 32212254720 {
		t.Fatalf("snapshot totals did not reach the response: %+v", overview)
	}
	if overview.Workloads.StatusCounts["available"] != 2 ||
		overview.Workloads.StatusCounts["complete"] != 1 {
		t.Fatalf("workload statuses = %+v", overview.Workloads.StatusCounts)
	}

	for range 5 {
		if _, err := service.Get(context.Background(), overviewClusterID); err != nil {
			t.Fatal(err)
		}
	}
	for _, section := range []string{"nodes", "pods", "namespaces"} {
		if count := reader.callCount(section); count != 0 {
			t.Fatalf("section %s was listed %d times despite the Agent cache", section, count)
		}
	}
	if calls := reader.calls.Load(); calls != 6 {
		t.Fatalf("%d snapshot reads for 6 requests", calls)
	}
}

// An Agent that predates the cache is read exactly the way it always was.
func TestOverviewFallsBackForAnAgentWithoutACache(t *testing.T) {
	t.Parallel()
	reader := &snapshotReader{
		fakeResourceReader: &fakeResourceReader{},
		err:                kubernetesresource.ErrAgentUnsupported,
	}
	service := NewService(reader, Config{CacheTTL: -1})
	overview, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatalf("an Agent without a cache produced an error: %v", err)
	}
	if overview.Partial {
		t.Fatalf("the fallback path reported issues: %+v", overview.Issues)
	}
	for _, section := range []string{"nodes", "pods", "namespaces"} {
		if count := reader.callCount(section); count != 1 {
			t.Fatalf("section %s was listed %d times on the fallback path", section, count)
		}
	}
}

// A cache that has not finished its first listing knows it does not know. The
// Cluster is read directly rather than reported as a Cluster full of zeroes.
func TestOverviewFallsBackWhileTheCacheIsWarming(t *testing.T) {
	t.Parallel()
	reader := &snapshotReader{
		fakeResourceReader: &fakeResourceReader{
			nodes: []kubernetesresource.NodeSummary{{
				Status: "ready", KubernetesVersion: "v1.31.4",
				CPUCapacity: "4", CPUAllocatable: "3500m",
				MemoryCapacity: "8Gi", MemoryAllocatable: "7Gi",
				PodsCapacity: "110", PodsAllocatable: "100",
			}},
		},
		snapshot: resourcesummary.OverviewSnapshot{
			GeneratedAt: time.Now().UTC(),
			Sections:    healthySections(map[string]string{"pods": "warming_up"}),
		},
	}
	service := NewService(reader, Config{CacheTTL: -1})
	overview, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Nodes.Total != 1 {
		t.Fatalf("the warming path did not read the Cluster: %+v", overview.Nodes)
	}
	if count := reader.callCount("pods"); count != 1 {
		t.Fatalf("Pods were listed %d times while the cache was warming", count)
	}
}

// A section the cache cannot read is reported the same way a failed listing is:
// an issue, a partial result, and no invented numbers for that section.
func TestSnapshotSectionFailuresBecomeIssues(t *testing.T) {
	t.Parallel()
	snapshot := resourcesummary.OverviewSnapshot{
		GeneratedAt: time.Now().UTC(),
		Stale:       true,
		Sections: healthySections(map[string]string{
			"workloads.cronjobs": "resource_absent",
			"nodes":              "cluster_api_forbidden",
		}),
		Pods: resourcesummary.PodTotals{
			Total: 4, StatusCounts: map[string]int64{"running": 4},
		},
	}
	overview := overviewFromSnapshot(snapshot)
	if !overview.Partial || !overview.Stale {
		t.Fatalf("a degraded snapshot was reported as complete: %+v", overview)
	}
	found := map[string]string{}
	for _, issue := range overview.Issues {
		found[issue.Section] = issue.Code
	}
	if found["nodes"] != "cluster_api_forbidden" ||
		found["workloads.cronjobs"] != "resource_absent" {
		t.Fatalf("section issues = %+v", found)
	}
	if overview.Nodes.Total != 0 {
		t.Fatal("a section that could not be read reported a number anyway")
	}
	if overview.Pods.Total != 4 {
		t.Fatal("a healthy section was disturbed by a failing one")
	}
}

// A snapshot the Agent could not produce at all is an error, not a silent
// fallback that would double the Cluster load on a Cluster already failing.
func TestSnapshotFailuresAreNotSilentlyRetriedAgainstTheCluster(t *testing.T) {
	t.Parallel()
	reader := &snapshotReader{
		fakeResourceReader: &fakeResourceReader{},
		err:                kubernetesresource.ErrClusterTimeout,
	}
	service := NewService(reader, Config{CacheTTL: -1})
	if _, err := service.Get(context.Background(), overviewClusterID); !errors.Is(
		err,
		kubernetesresource.ErrClusterTimeout,
	) {
		t.Fatalf("snapshot failure = %v", err)
	}
	if count := reader.callCount("pods"); count != 0 {
		t.Fatalf("the Cluster was listed %d times after a snapshot failure", count)
	}
}
