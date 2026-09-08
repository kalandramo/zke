package resourcecache

import (
	"maps"
	"math"
)

// contribution is what one cached object adds to its section's totals.
//
// It is a flat value on purpose. Every Added, Updated and Deleted event applies
// one of these, so a shape that allocates — a map of counters, say — would
// allocate once per Pod change in a Cluster where Pods change constantly.
//
// Updates are applied by subtracting the old contribution and adding the new
// one, which is why every field here has to be something that can be subtracted
// as cleanly as it was added.
type contribution struct {
	// status is the one bucket this object counts towards. Empty is recorded as
	// "unknown" rather than dropped, so the buckets always sum to the total.
	status string
	// kubernetesVersion is the Node's reported kubelet version. Only Nodes set
	// it; a Cluster mid-upgrade reports more than one, and the skew between
	// them decides which APIs are safe to use.
	kubernetesVersion string

	unschedulable bool
	ready         bool
	notReady      bool
	terminating   bool
	// nonTerminal marks a Pod that still occupies its Node, and is therefore
	// the one that contributes requests.
	nonTerminal bool

	cpuCapacityMillis    int64
	cpuAllocatableMillis int64
	cpuRequestedMillis   int64

	memoryCapacityBytes    int64
	memoryAllocatableBytes int64
	memoryRequestedBytes   int64

	podCapacity    int64
	podAllocatable int64

	storageCapacityBytes  int64
	storageRequestedBytes int64
}

// sectionTotals is the running aggregate for one cached resource.
//
// Each cached GVR owns exactly one of these, so the overview never needs a lock
// spanning resources, and a re-list rebuilds one section without disturbing the
// others.
type sectionTotals struct {
	total         int64
	statusCounts  map[string]int64
	versionCounts map[string]int64

	unschedulable int64
	ready         int64
	notReady      int64
	terminating   int64
	nonTerminal   int64

	cpuCapacityMillis    int64
	cpuAllocatableMillis int64
	cpuRequestedMillis   int64

	memoryCapacityBytes    int64
	memoryAllocatableBytes int64
	memoryRequestedBytes   int64

	podCapacity    int64
	podAllocatable int64

	storageCapacityBytes  int64
	storageRequestedBytes int64
}

func newSectionTotals() sectionTotals {
	return sectionTotals{
		statusCounts:  map[string]int64{},
		versionCounts: map[string]int64{},
	}
}

// apply folds one contribution in (sign +1) or back out (sign -1).
func (totals *sectionTotals) apply(value contribution, sign int64) {
	totals.total = addSigned(totals.total, sign)
	countBucket(totals.statusCounts, value.status, sign)
	if value.kubernetesVersion != "" {
		countBucket(totals.versionCounts, value.kubernetesVersion, sign)
	}
	totals.unschedulable = addFlag(totals.unschedulable, value.unschedulable, sign)
	totals.ready = addFlag(totals.ready, value.ready, sign)
	totals.notReady = addFlag(totals.notReady, value.notReady, sign)
	totals.terminating = addFlag(totals.terminating, value.terminating, sign)
	totals.nonTerminal = addFlag(totals.nonTerminal, value.nonTerminal, sign)

	totals.cpuCapacityMillis = addScaled(totals.cpuCapacityMillis, value.cpuCapacityMillis, sign)
	totals.cpuAllocatableMillis = addScaled(totals.cpuAllocatableMillis, value.cpuAllocatableMillis, sign)
	totals.cpuRequestedMillis = addScaled(totals.cpuRequestedMillis, value.cpuRequestedMillis, sign)

	totals.memoryCapacityBytes = addScaled(totals.memoryCapacityBytes, value.memoryCapacityBytes, sign)
	totals.memoryAllocatableBytes = addScaled(totals.memoryAllocatableBytes, value.memoryAllocatableBytes, sign)
	totals.memoryRequestedBytes = addScaled(totals.memoryRequestedBytes, value.memoryRequestedBytes, sign)

	totals.podCapacity = addScaled(totals.podCapacity, value.podCapacity, sign)
	totals.podAllocatable = addScaled(totals.podAllocatable, value.podAllocatable, sign)

	totals.storageCapacityBytes = addScaled(totals.storageCapacityBytes, value.storageCapacityBytes, sign)
	totals.storageRequestedBytes = addScaled(totals.storageRequestedBytes, value.storageRequestedBytes, sign)
}

// clone hands out a copy, because the maps inside keep being written by the
// event stream after a reader has taken the snapshot.
func (totals sectionTotals) clone() sectionTotals {
	copied := totals
	copied.statusCounts = cloneCounts(totals.statusCounts)
	copied.versionCounts = cloneCounts(totals.versionCounts)
	return copied
}

func cloneCounts(counts map[string]int64) map[string]int64 {
	copied := make(map[string]int64, len(counts))
	maps.Copy(copied, counts)
	return copied
}

// countBucket keeps a bucket only while something is in it, so a status no
// object has any more stops being reported rather than being reported as zero.
func countBucket(counts map[string]int64, bucket string, sign int64) {
	if bucket == "" {
		bucket = "unknown"
	}
	counts[bucket] = addSigned(counts[bucket], sign)
	if counts[bucket] <= 0 {
		delete(counts, bucket)
	}
}

func addFlag(total int64, set bool, sign int64) int64 {
	if !set {
		return total
	}
	return addSigned(total, sign)
}

func addSigned(total int64, sign int64) int64 {
	return max(total+sign, 0)
}

// addScaled saturates rather than wrapping. A Cluster cannot really hold
// 9.2 exabytes of memory requests, so an int64 that would wrap means a value
// arrived that was not what it claimed to be; reporting the ceiling is a
// visible wrong number, while wrapping would be an invisible one.
func addScaled(total int64, value int64, sign int64) int64 {
	if value == 0 {
		return total
	}
	if sign < 0 {
		return max(total-value, 0)
	}
	if total > math.MaxInt64-value {
		return math.MaxInt64
	}
	return total + value
}
