package clusteroverview

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/shared/validation"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	resourcehelper "k8s.io/component-helpers/resource"
)

const (
	defaultPageSize           int64 = 500
	defaultMaxItemsPerSection int   = 10_000
	defaultMaxParallelQueries int   = 4
	// How long a completed snapshot answers for its Cluster.
	//
	// One overview is ten full listings of the Cluster, so the page that every
	// operator lands on is also the most expensive read in this application. The
	// window is short on purpose: it absorbs bursts — several operators opening
	// the application, one operator moving between sections — without making a
	// deliberate refresh return yesterday's numbers. The response carries
	// `generated_at`, so a repeat inside the window is visibly the same snapshot
	// rather than a silently stale one.
	defaultCacheTTL = 15 * time.Second
	// How long past its window a snapshot still answers.
	//
	// Past the fresh window the numbers are re-read, but a Cluster whose Agent
	// has just dropped, or whose control plane is briefly refusing reads, has
	// no numbers to re-read. Showing the last ones that were true, marked
	// `stale`, is a better answer than an error page for an operator who is
	// looking at that Cluster precisely because something is wrong with it.
	defaultMaxStaleAge = 2 * time.Minute
	// The first wait after a failed load, doubling up to the ceiling.
	//
	// A Cluster that cannot answer is the case where users press refresh most,
	// and every press is ten Kubernetes listings. The backoff is what turns a
	// row of refreshes into one attempt.
	defaultFailureBackoff    = 2 * time.Second
	defaultMaxFailureBackoff = 30 * time.Second
	// The lifetime of one shared load. It is not any caller's deadline: the
	// load outlives the request that started it so the callers still waiting
	// keep their answer, so it needs a bound of its own.
	defaultLoadTimeout = 2 * time.Minute
	// Cached Clusters, so the cache cannot grow with the number of Clusters a
	// Server has ever been asked about.
	defaultMaxCachedClusters = 64
)

type ResourceReader interface {
	ListNodes(context.Context, kubernetesresource.ListNodesInput) (kubernetesresource.NodePage, error)
	ListResources(context.Context, kubernetesresource.ListResourcesInput) (kubernetesresource.ResourcePage, error)
	ListWorkloads(context.Context, kubernetesresource.ListWorkloadsInput) (kubernetesresource.WorkloadPage, error)
}

type Config struct {
	PageSize           int64
	MaxItemsPerSection int
	MaxParallelQueries int
	// Negative disables the cache; zero selects the default.
	CacheTTL time.Duration
	// MaxStaleAge is how long past the fresh window a completed snapshot may
	// still be returned, marked stale, while a refresh runs behind it.
	// Negative disables serving stale snapshots.
	MaxStaleAge time.Duration
	// FailureBackoff and MaxFailureBackoff bound how often a Cluster that keeps
	// failing is asked again.
	FailureBackoff    time.Duration
	MaxFailureBackoff time.Duration
	// LoadTimeout bounds one shared load.
	LoadTimeout       time.Duration
	MaxCachedClusters int
}

type Service struct {
	resources ResourceReader
	// snapshots is the Agent's own read model, when the Agent keeps one. It is
	// tried first and falls back to resources, so a Server upgraded ahead of its
	// Agents keeps working against every one of them.
	snapshots SnapshotReader
	config    Config
	// Completed snapshots and in-flight loads, keyed by Cluster. Nothing here
	// is caller-specific: the overview describes a Cluster, and every caller
	// reaching this service has already been checked for `cluster.read` on that
	// Cluster. Keying by Cluster is also what keeps one Cluster's snapshot from
	// ever answering for another.
	mutex sync.Mutex
	cache map[string]*cacheEntry
	// clock and afterLoad exist for tests, which need to move time and to
	// observe a background refresh that no caller is waiting on.
	clock     func() time.Time
	afterLoad func(clusterID string, err error)
}

type cacheEntry struct {
	// The last snapshot that completed with every section intact. It keeps
	// answering, marked stale, while a refresh runs behind it.
	overview    Overview
	hasSnapshot bool
	freshUntil  time.Time
	staleUntil  time.Time
	// The one load allowed to be in flight for this Cluster. Every other caller
	// waits on it rather than starting a second set of ten Kubernetes listings.
	call *loadCall
	// Consecutive failures, and the time before which no new load starts.
	failures   int
	retryAfter time.Time
	lastError  error
}

// loadCall is one shared load. It is deliberately detached from the Context of
// whichever caller started it: the others waiting on it did nothing wrong, and
// a load canceled because the first caller closed a browser tab would make them
// all fail together.
type loadCall struct {
	done     chan struct{}
	overview Overview
	err      error
}

type Overview struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Stale marks a snapshot returned past its fresh window because a current
	// one could not be produced. `generated_at` says how old it is.
	Stale      bool                  `json:"stale"`
	Partial    bool                  `json:"partial"`
	Issues     []SectionIssue        `json:"issues"`
	Nodes      NodeOverview          `json:"nodes"`
	Namespaces NamespaceOverview     `json:"namespaces"`
	Pods       PodOverview           `json:"pods"`
	Workloads  WorkloadOverview      `json:"workloads"`
	Storage    StorageOverview       `json:"storage"`
	Resources  ClusterResourceTotals `json:"resources"`
}

type SectionIssue struct {
	Section string `json:"section"`
	Code    string `json:"code"`
}

type NodeOverview struct {
	Total         int64            `json:"total"`
	Unschedulable int64            `json:"unschedulable"`
	StatusCounts  map[string]int64 `json:"status_counts"`
	// Nodes per reported kubelet version. A Cluster mid-upgrade reports more
	// than one, and the skew between them decides which APIs are safe to use.
	KubernetesVersions map[string]int64 `json:"kubernetes_versions"`
}

type NamespaceOverview struct {
	Total        int64            `json:"total"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

type PodOverview struct {
	Total        int64            `json:"total"`
	Ready        int64            `json:"ready"`
	NotReady     int64            `json:"not_ready"`
	Terminating  int64            `json:"terminating"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

type WorkloadOverview struct {
	Total        int64                      `json:"total"`
	StatusCounts map[string]int64           `json:"status_counts"`
	ByResource   []WorkloadResourceOverview `json:"by_resource"`
}

type WorkloadResourceOverview struct {
	Resource     string           `json:"resource"`
	Total        int64            `json:"total"`
	StatusCounts map[string]int64 `json:"status_counts"`
}

// StorageOverview reports persistent storage as counts, not as a ratio.
//
// Volume capacity has no Cluster-wide maximum to be read against the way CPU and
// memory do: with a dynamic provisioner the supply is whatever the backing
// system will still hand out, and it is not visible from Kubernetes. So this
// section reports how much has been provisioned and asked for, and how the
// objects are distributed across their phases — a Pending claim or a Failed
// volume is the thing worth seeing here, and both are counts.
type StorageOverview struct {
	PersistentVolumes      PersistentVolumeOverview      `json:"persistent_volumes"`
	PersistentVolumeClaims PersistentVolumeClaimOverview `json:"persistent_volume_claims"`
}

type PersistentVolumeOverview struct {
	Total int64 `json:"total"`
	// Sum of `spec.capacity.storage` over every PersistentVolume, whatever phase
	// it is in: a Released volume still occupies its backing storage.
	CapacityBytes int64            `json:"capacity_bytes"`
	StatusCounts  map[string]int64 `json:"status_counts"`
}

type PersistentVolumeClaimOverview struct {
	Total int64 `json:"total"`
	// Sum of `spec.resources.requests.storage`, which is what was asked for
	// rather than what was provisioned — the same reading as the CPU and memory
	// request totals above.
	RequestedBytes int64            `json:"requested_bytes"`
	StatusCounts   map[string]int64 `json:"status_counts"`
}

type ClusterResourceTotals struct {
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

type sectionResult struct {
	section   string
	value     any
	truncated bool
	err       error
}

type nodeSnapshot struct {
	overview  NodeOverview
	resources ClusterResourceTotals
}

type podSnapshot struct {
	overview  PodOverview
	resources ClusterResourceTotals
}

type sectionTask struct {
	section string
	load    func(context.Context) (any, bool, error)
}

func NewService(resources ResourceReader, configs ...Config) *Service {
	config := Config{}
	if len(configs) > 0 {
		config = configs[0]
	}
	if config.PageSize <= 0 || config.PageSize > kubernetesresource.MaxResourceListLimit {
		config.PageSize = defaultPageSize
	}
	if config.MaxItemsPerSection <= 0 {
		config.MaxItemsPerSection = defaultMaxItemsPerSection
	}
	if config.MaxParallelQueries <= 0 {
		config.MaxParallelQueries = defaultMaxParallelQueries
	}
	if config.CacheTTL == 0 {
		config.CacheTTL = defaultCacheTTL
	}
	if config.MaxStaleAge == 0 {
		config.MaxStaleAge = defaultMaxStaleAge
	}
	if config.FailureBackoff <= 0 {
		config.FailureBackoff = defaultFailureBackoff
	}
	if config.MaxFailureBackoff < config.FailureBackoff {
		config.MaxFailureBackoff = max(defaultMaxFailureBackoff, config.FailureBackoff)
	}
	if config.LoadTimeout <= 0 {
		config.LoadTimeout = defaultLoadTimeout
	}
	if config.MaxCachedClusters <= 0 {
		config.MaxCachedClusters = defaultMaxCachedClusters
	}
	service := &Service{
		resources: resources,
		config:    config,
		cache:     make(map[string]*cacheEntry),
		clock:     time.Now,
	}
	// The same object usually implements both: the Server's Kubernetes resource
	// service can list a Cluster and can ask its Agent for a snapshot. Taking
	// the second only when it is offered is what keeps this optional.
	if snapshots, ok := resources.(SnapshotReader); ok {
		service.snapshots = snapshots
	}
	return service
}

func (service *Service) now() time.Time {
	if service.clock != nil {
		return service.clock()
	}
	return time.Now()
}

// Get answers for one Cluster.
//
// Freshness is decided per Cluster and nowhere else: a snapshot inside its
// window is returned as it stands, one just past it is returned marked stale
// while a single refresh runs behind it, and a Cluster with nothing to return
// gets exactly one load however many callers are waiting. Authorization is not
// part of that decision — every caller has already been checked for
// cluster.read on this Cluster before reaching here, and a cached snapshot can
// therefore never be a way around that check.
func (service *Service) Get(ctx context.Context, clusterID string) (Overview, error) {
	if service == nil || service.resources == nil || ctx == nil || !validation.IsUUID(clusterID) {
		return Overview{}, kubernetesresource.ErrInvalidInput
	}
	if service.config.CacheTTL < 0 {
		return service.load(ctx, clusterID)
	}
	call, cached, err := service.begin(ctx, clusterID)
	if call == nil {
		return cached, err
	}
	select {
	case <-call.done:
		if call.err != nil {
			return Overview{}, call.err
		}
		return call.overview.clone(), nil
	case <-ctx.Done():
		// Only this caller leaves. The load keeps running for whoever else is
		// waiting on it, and its result still reaches the cache.
		return Overview{}, ctx.Err()
	}
}

// begin decides what this caller gets: a snapshot to return now, or the load to
// wait on. Exactly one of them comes back.
func (service *Service) begin(
	ctx context.Context,
	clusterID string,
) (*loadCall, Overview, error) {
	now := service.now()
	service.mutex.Lock()
	defer service.mutex.Unlock()
	entry := service.cache[clusterID]
	if entry != nil && entry.hasSnapshot {
		if now.Before(entry.freshUntil) {
			return nil, entry.overview.clone(), nil
		}
		if now.Before(entry.staleUntil) {
			// Answer from the last known good and refresh behind it, so the
			// operator waiting on the page never pays for the ten listings.
			service.startLocked(ctx, clusterID, entry, now)
			stale := entry.overview.clone()
			stale.Stale = true
			return nil, stale, nil
		}
	}
	if entry != nil && entry.call == nil && now.Before(entry.retryAfter) {
		// Backing off after repeated failures. Returning the remembered failure
		// is what keeps a row of refreshes from becoming a row of ten-listing
		// attempts against a Cluster that is already struggling.
		return nil, Overview{}, entry.lastError
	}
	if entry == nil {
		entry = &cacheEntry{}
		if !service.admitLocked(clusterID, entry, now) {
			// The cache is full of live entries, so this Cluster is loaded
			// without one. It still gets an answer; it just does not displace a
			// snapshot another Cluster is about to read.
			return service.startDetached(ctx, clusterID), Overview{}, nil
		}
	}
	call := service.startLocked(ctx, clusterID, entry, now)
	if call == nil {
		return nil, Overview{}, entry.lastError
	}
	return call, Overview{}, nil
}

// startLocked returns the load in flight for this Cluster, starting one if
// there is none and nothing says to wait.
func (service *Service) startLocked(
	ctx context.Context,
	clusterID string,
	entry *cacheEntry,
	now time.Time,
) *loadCall {
	if entry.call != nil {
		return entry.call
	}
	if now.Before(entry.retryAfter) {
		return nil
	}
	entry.call = service.startDetached(ctx, clusterID)
	return entry.call
}

func (service *Service) startDetached(ctx context.Context, clusterID string) *loadCall {
	call := &loadCall{done: make(chan struct{})}
	// The Context keeps its values — the request identifier travels with the
	// Agent Stream — and loses its cancellation, which belongs to one caller
	// rather than to the load every caller is sharing.
	loadContext := context.WithoutCancel(ctx)
	go service.run(loadContext, clusterID, call)
	return call
}

func (service *Service) run(ctx context.Context, clusterID string, call *loadCall) {
	loadContext, cancel := context.WithTimeout(ctx, service.config.LoadTimeout)
	overview, err := service.load(loadContext, clusterID)
	cancel()

	now := service.now()
	service.mutex.Lock()
	entry := service.cache[clusterID]
	// A detached load is not registered in the cache. If capacity becomes
	// available while it runs, a later caller may create a new entry and a new
	// load for the same Cluster. Only the call owned by that entry may update it;
	// otherwise the older detached result can overwrite the newer load's state.
	if entry != nil && entry.call == call {
		entry.call = nil
		switch {
		case err != nil:
			entry.failures++
			entry.lastError = err
			entry.retryAfter = now.Add(service.backoff(entry.failures))
		case cacheable(overview):
			entry.overview = overview.clone()
			entry.hasSnapshot = true
			entry.freshUntil = now.Add(service.config.CacheTTL)
			entry.staleUntil = entry.freshUntil
			if service.config.MaxStaleAge > 0 {
				entry.staleUntil = entry.freshUntil.Add(service.config.MaxStaleAge)
			}
			entry.failures = 0
			entry.lastError = nil
			entry.retryAfter = time.Time{}
		default:
			// A section failed. The result is returned to whoever asked, but it
			// is not remembered: an operator who sees a section reported as
			// unavailable presses refresh precisely to find out whether it still
			// is, and a cache that answers from the failure has turned the
			// button off. Nor is it a failure to back off from — it succeeded.
			entry.failures = 0
			entry.lastError = nil
			entry.retryAfter = time.Time{}
		}
	}
	service.mutex.Unlock()

	call.overview = overview
	call.err = err
	close(call.done)
	if service.afterLoad != nil {
		service.afterLoad(clusterID, err)
	}
}

func (service *Service) backoff(failures int) time.Duration {
	delay := service.config.FailureBackoff
	for range failures - 1 {
		if delay >= service.config.MaxFailureBackoff {
			break
		}
		delay *= 2
	}
	return min(delay, service.config.MaxFailureBackoff)
}

// admitLocked makes room for one more Cluster, dropping entries nothing can
// still read. A cache full of live entries admits nothing rather than evicting
// a snapshot another Cluster is about to use.
func (service *Service) admitLocked(
	clusterID string,
	entry *cacheEntry,
	now time.Time,
) bool {
	if len(service.cache) >= service.config.MaxCachedClusters {
		for key, cached := range service.cache {
			if cached.call == nil &&
				!now.Before(cached.staleUntil) &&
				!now.Before(cached.retryAfter) {
				delete(service.cache, key)
			}
		}
		if len(service.cache) >= service.config.MaxCachedClusters {
			return false
		}
	}
	service.cache[clusterID] = entry
	return true
}

func cacheable(overview Overview) bool {
	for _, issue := range overview.Issues {
		if issue.Code != "item_limit_reached" {
			return false
		}
	}
	return true
}

func (service *Service) load(ctx context.Context, clusterID string) (Overview, error) {
	// The Agent's own read model first. It answers from memory, so a Cluster
	// whose Agent keeps one costs no Kubernetes listing at all here.
	if overview, answered, err := service.snapshotOverview(ctx, clusterID); err != nil {
		return Overview{}, err
	} else if answered {
		return overview, nil
	}
	tasks := service.tasks(clusterID)
	results := make([]sectionResult, len(tasks))
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(service.config.MaxParallelQueries)
	for index := range tasks {
		index := index
		group.Go(func() error {
			value, truncated, err := tasks[index].load(groupContext)
			results[index] = sectionResult{
				section: tasks[index].section, value: value, truncated: truncated, err: err,
			}
			// Section failures are intentionally collected instead of canceling
			// sibling queries: a useful overview may still be returned as partial.
			return nil
		})
	}
	_ = group.Wait()

	overview := Overview{
		GeneratedAt: time.Now().UTC(),
		Issues:      make([]SectionIssue, 0),
		Nodes: NodeOverview{
			StatusCounts: map[string]int64{}, KubernetesVersions: map[string]int64{},
		},
		Namespaces: NamespaceOverview{StatusCounts: map[string]int64{}},
		Pods:       PodOverview{StatusCounts: map[string]int64{}},
		Workloads: WorkloadOverview{
			StatusCounts: map[string]int64{},
			ByResource:   make([]WorkloadResourceOverview, 0, 5),
		},
		Storage: StorageOverview{
			PersistentVolumes:      PersistentVolumeOverview{StatusCounts: map[string]int64{}},
			PersistentVolumeClaims: PersistentVolumeClaimOverview{StatusCounts: map[string]int64{}},
		},
	}
	succeeded := 0
	var firstError error
	for _, result := range results {
		if result.err != nil {
			if firstError == nil {
				firstError = result.err
			}
			overview.Issues = append(overview.Issues, SectionIssue{
				Section: result.section,
				Code:    issueCode(result.err),
			})
			continue
		}
		succeeded++
		if result.truncated {
			overview.Issues = append(overview.Issues, SectionIssue{
				Section: result.section,
				Code:    "item_limit_reached",
			})
		}
		switch value := result.value.(type) {
		case nodeSnapshot:
			overview.Nodes = value.overview
			mergeNodeResources(&overview.Resources, value.resources)
		case NamespaceOverview:
			overview.Namespaces = value
		case podSnapshot:
			overview.Pods = value.overview
			mergePodResources(&overview.Resources, value.resources)
		case WorkloadResourceOverview:
			overview.Workloads.ByResource = append(overview.Workloads.ByResource, value)
			overview.Workloads.Total += value.Total
			mergeStatusCounts(overview.Workloads.StatusCounts, value.StatusCounts)
		case PersistentVolumeOverview:
			overview.Storage.PersistentVolumes = value
		case PersistentVolumeClaimOverview:
			overview.Storage.PersistentVolumeClaims = value
		}
	}
	if succeeded == 0 {
		if firstError == nil {
			firstError = kubernetesresource.ErrUpstreamFailure
		}
		return Overview{}, firstError
	}
	overview.Partial = len(overview.Issues) > 0
	sort.Slice(overview.Workloads.ByResource, func(left, right int) bool {
		return overview.Workloads.ByResource[left].Resource < overview.Workloads.ByResource[right].Resource
	})
	return overview, nil
}

func (service *Service) tasks(clusterID string) []sectionTask {
	tasks := []sectionTask{
		{section: "nodes", load: func(ctx context.Context) (any, bool, error) {
			return service.loadNodes(ctx, clusterID)
		}},
		{section: "namespaces", load: func(ctx context.Context) (any, bool, error) {
			return service.loadNamespaces(ctx, clusterID)
		}},
		{section: "pods", load: func(ctx context.Context) (any, bool, error) {
			return service.loadPods(ctx, clusterID)
		}},
		{section: "storage.persistentvolumes", load: func(ctx context.Context) (any, bool, error) {
			return service.loadPersistentVolumes(ctx, clusterID)
		}},
		{section: "storage.persistentvolumeclaims", load: func(ctx context.Context) (any, bool, error) {
			return service.loadPersistentVolumeClaims(ctx, clusterID)
		}},
	}
	for _, resourceName := range []kubernetesresource.WorkloadResource{
		kubernetesresource.WorkloadDeployments,
		kubernetesresource.WorkloadStatefulSets,
		kubernetesresource.WorkloadDaemonSets,
		kubernetesresource.WorkloadJobs,
		kubernetesresource.WorkloadCronJobs,
	} {
		resourceName := resourceName
		tasks = append(tasks, sectionTask{
			section: "workloads." + string(resourceName),
			load: func(ctx context.Context) (any, bool, error) {
				return service.loadWorkloads(ctx, clusterID, resourceName)
			},
		})
	}
	return tasks
}

func (service *Service) loadNodes(ctx context.Context, clusterID string) (any, bool, error) {
	snapshot := nodeSnapshot{overview: NodeOverview{
		StatusCounts: map[string]int64{}, KubernetesVersions: map[string]int64{},
	}}
	continuation := ""
	for {
		page, err := service.resources.ListNodes(ctx, kubernetesresource.ListNodesInput{
			ClusterID: clusterID, Limit: service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, node := range page.Nodes {
			if snapshot.overview.Total >= int64(service.config.MaxItemsPerSection) {
				return snapshot, true, nil
			}
			snapshot.overview.Total++
			incrementStatus(snapshot.overview.StatusCounts, node.Status)
			incrementStatus(snapshot.overview.KubernetesVersions, node.KubernetesVersion)
			if node.Unschedulable {
				snapshot.overview.Unschedulable++
			}
			if err := addNodeResources(&snapshot.resources, node); err != nil {
				return nil, false, err
			}
		}
		if page.ContinueToken == "" {
			return snapshot, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func (service *Service) loadNamespaces(ctx context.Context, clusterID string) (any, bool, error) {
	overview := NamespaceOverview{StatusCounts: map[string]int64{}}
	continuation := ""
	for {
		page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
			ClusterID: clusterID,
			Resource:  kubernetesresource.ResourceIdentity{Version: "v1", Resource: "namespaces"},
			Limit:     service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, object := range page.Items {
			if overview.Total >= int64(service.config.MaxItemsPerSection) {
				return overview, true, nil
			}
			var namespace corev1.Namespace
			if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &namespace) != nil ||
				namespace.APIVersion != "v1" || namespace.Kind != "Namespace" || namespace.Name == "" {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
			status := strings.ToLower(string(namespace.Status.Phase))
			if namespace.DeletionTimestamp != nil {
				status = strings.ToLower(string(corev1.NamespaceTerminating))
			}
			overview.Total++
			incrementStatus(overview.StatusCounts, status)
		}
		if page.ContinueToken == "" {
			return overview, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func (service *Service) loadPods(ctx context.Context, clusterID string) (any, bool, error) {
	snapshot := podSnapshot{overview: PodOverview{StatusCounts: map[string]int64{}}}
	continuation := ""
	for {
		page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
			ClusterID: clusterID,
			Resource:  kubernetesresource.ResourceIdentity{Version: "v1", Resource: "pods"},
			Limit:     service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, object := range page.Items {
			if snapshot.overview.Total >= int64(service.config.MaxItemsPerSection) {
				return snapshot, true, nil
			}
			var pod corev1.Pod
			if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &pod) != nil ||
				pod.APIVersion != "v1" || pod.Kind != "Pod" || pod.Name == "" || pod.Namespace == "" {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
			snapshot.overview.Total++
			incrementStatus(snapshot.overview.StatusCounts, strings.ToLower(string(pod.Status.Phase)))
			terminal := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
			if pod.DeletionTimestamp != nil {
				snapshot.overview.Terminating++
			}
			if podReady(&pod) {
				snapshot.overview.Ready++
			} else if !terminal {
				snapshot.overview.NotReady++
			}
			if !terminal {
				if err := addPodRequests(&snapshot.resources, &pod); err != nil {
					return nil, false, err
				}
			}
		}
		if page.ContinueToken == "" {
			return snapshot, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func (service *Service) loadPersistentVolumes(
	ctx context.Context,
	clusterID string,
) (any, bool, error) {
	overview := PersistentVolumeOverview{StatusCounts: map[string]int64{}}
	continuation := ""
	for {
		page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
			ClusterID: clusterID,
			Resource:  kubernetesresource.ResourceIdentity{Version: "v1", Resource: "persistentvolumes"},
			Limit:     service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, object := range page.Items {
			if overview.Total >= int64(service.config.MaxItemsPerSection) {
				return overview, true, nil
			}
			var volume corev1.PersistentVolume
			if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &volume) != nil ||
				volume.APIVersion != "v1" || volume.Kind != "PersistentVolume" || volume.Name == "" {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
			overview.Total++
			// The phase as `kubectl get pv` prints it, and nothing derived: a
			// volume being deleted keeps the phase that says whether anything
			// still claims it.
			incrementStatus(overview.StatusCounts, strings.ToLower(string(volume.Status.Phase)))
			capacity := volume.Spec.Capacity[corev1.ResourceStorage]
			if capacity.Sign() < 0 || !checkedAdd(&overview.CapacityBytes, capacity.Value()) {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
		}
		if page.ContinueToken == "" {
			return overview, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func (service *Service) loadPersistentVolumeClaims(
	ctx context.Context,
	clusterID string,
) (any, bool, error) {
	overview := PersistentVolumeClaimOverview{StatusCounts: map[string]int64{}}
	continuation := ""
	for {
		page, err := service.resources.ListResources(ctx, kubernetesresource.ListResourcesInput{
			ClusterID: clusterID,
			Resource: kubernetesresource.ResourceIdentity{
				Version: "v1", Resource: "persistentvolumeclaims",
			},
			Limit: service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, object := range page.Items {
			if overview.Total >= int64(service.config.MaxItemsPerSection) {
				return overview, true, nil
			}
			var claim corev1.PersistentVolumeClaim
			if runtime.DefaultUnstructuredConverter.FromUnstructured(object, &claim) != nil ||
				claim.APIVersion != "v1" || claim.Kind != "PersistentVolumeClaim" ||
				claim.Name == "" || claim.Namespace == "" {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
			overview.Total++
			incrementStatus(overview.StatusCounts, strings.ToLower(string(claim.Status.Phase)))
			requested := claim.Spec.Resources.Requests[corev1.ResourceStorage]
			if requested.Sign() < 0 ||
				!checkedAdd(&overview.RequestedBytes, requested.Value()) {
				return nil, false, kubernetesresource.ErrInvalidResponse
			}
		}
		if page.ContinueToken == "" {
			return overview, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func (service *Service) loadWorkloads(
	ctx context.Context,
	clusterID string,
	resourceName kubernetesresource.WorkloadResource,
) (any, bool, error) {
	overview := WorkloadResourceOverview{
		Resource: string(resourceName), StatusCounts: map[string]int64{},
	}
	continuation := ""
	for {
		page, err := service.resources.ListWorkloads(ctx, kubernetesresource.ListWorkloadsInput{
			ClusterID: clusterID, Resource: resourceName,
			Limit: service.config.PageSize, ContinueToken: continuation,
		})
		if err != nil {
			return nil, false, err
		}
		for _, workload := range page.Workloads {
			if overview.Total >= int64(service.config.MaxItemsPerSection) {
				return overview, true, nil
			}
			overview.Total++
			incrementStatus(overview.StatusCounts, workload.Status)
		}
		if page.ContinueToken == "" {
			return overview, false, nil
		}
		if page.ContinueToken == continuation {
			return nil, false, kubernetesresource.ErrInvalidResponse
		}
		continuation = page.ContinueToken
	}
}

func addNodeResources(target *ClusterResourceTotals, node kubernetesresource.NodeSummary) error {
	values := []struct {
		text string
		cpu  bool
		dest *int64
	}{
		{node.CPUCapacity, true, &target.CPUCapacityMillis},
		{node.CPUAllocatable, true, &target.CPUAllocatableMillis},
		{node.MemoryCapacity, false, &target.MemoryCapacityBytes},
		{node.MemoryAllocatable, false, &target.MemoryAllocatableBytes},
		{node.PodsCapacity, false, &target.PodCapacity},
		{node.PodsAllocatable, false, &target.PodAllocatable},
	}
	for _, value := range values {
		parsed, err := quantityValue(value.text, value.cpu)
		if err != nil || !checkedAdd(value.dest, parsed) {
			return kubernetesresource.ErrInvalidResponse
		}
	}
	return nil
}

func addPodRequests(target *ClusterResourceTotals, pod *corev1.Pod) error {
	requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
	cpu := requests[corev1.ResourceCPU]
	memory := requests[corev1.ResourceMemory]
	if cpu.Sign() < 0 || memory.Sign() < 0 ||
		!checkedAdd(&target.CPURequestedMillis, cpu.MilliValue()) ||
		!checkedAdd(&target.MemoryRequestedBytes, memory.Value()) ||
		!checkedAdd(&target.NonTerminalPods, 1) {
		return kubernetesresource.ErrInvalidResponse
	}
	return nil
}

func quantityValue(text string, cpu bool) (int64, error) {
	if text == "" {
		return 0, nil
	}
	quantity, err := resource.ParseQuantity(text)
	if err != nil || quantity.Sign() < 0 {
		return 0, kubernetesresource.ErrInvalidResponse
	}
	if cpu {
		return quantity.MilliValue(), nil
	}
	return quantity.Value(), nil
}

func checkedAdd(target *int64, value int64) bool {
	if target == nil || value < 0 || *target > math.MaxInt64-value {
		return false
	}
	*target += value
	return true
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// A snapshot handed out separately from the one held in the cache.
//
// The same Overview is returned to every caller inside its window, and it is
// made of maps and slices. Copying them keeps one caller's response from being
// something another caller could reach.
func (overview Overview) clone() Overview {
	copied := overview
	copied.Issues = append(make([]SectionIssue, 0, len(overview.Issues)), overview.Issues...)
	copied.Nodes.StatusCounts = cloneCounts(overview.Nodes.StatusCounts)
	copied.Nodes.KubernetesVersions = cloneCounts(overview.Nodes.KubernetesVersions)
	copied.Namespaces.StatusCounts = cloneCounts(overview.Namespaces.StatusCounts)
	copied.Pods.StatusCounts = cloneCounts(overview.Pods.StatusCounts)
	copied.Workloads.StatusCounts = cloneCounts(overview.Workloads.StatusCounts)
	copied.Workloads.ByResource = make([]WorkloadResourceOverview, 0, len(overview.Workloads.ByResource))
	for _, entry := range overview.Workloads.ByResource {
		entry.StatusCounts = cloneCounts(entry.StatusCounts)
		copied.Workloads.ByResource = append(copied.Workloads.ByResource, entry)
	}
	copied.Storage.PersistentVolumes.StatusCounts = cloneCounts(
		overview.Storage.PersistentVolumes.StatusCounts,
	)
	copied.Storage.PersistentVolumeClaims.StatusCounts = cloneCounts(
		overview.Storage.PersistentVolumeClaims.StatusCounts,
	)
	return copied
}

func cloneCounts(counts map[string]int64) map[string]int64 {
	copied := make(map[string]int64, len(counts))
	for key, value := range counts {
		copied[key] = value
	}
	return copied
}

func incrementStatus(counts map[string]int64, status string) {
	if status == "" {
		status = "unknown"
	}
	counts[status]++
}

func mergeStatusCounts(target map[string]int64, source map[string]int64) {
	for status, count := range source {
		target[status] += count
	}
}

func mergeNodeResources(target *ClusterResourceTotals, source ClusterResourceTotals) {
	target.CPUCapacityMillis = source.CPUCapacityMillis
	target.CPUAllocatableMillis = source.CPUAllocatableMillis
	target.MemoryCapacityBytes = source.MemoryCapacityBytes
	target.MemoryAllocatableBytes = source.MemoryAllocatableBytes
	target.PodCapacity = source.PodCapacity
	target.PodAllocatable = source.PodAllocatable
}

func mergePodResources(target *ClusterResourceTotals, source ClusterResourceTotals) {
	target.CPURequestedMillis = source.CPURequestedMillis
	target.MemoryRequestedBytes = source.MemoryRequestedBytes
	target.NonTerminalPods = source.NonTerminalPods
}

func issueCode(err error) string {
	switch {
	case errors.Is(err, kubernetesresource.ErrAgentNotConnected):
		return "agent_not_connected"
	case errors.Is(err, kubernetesresource.ErrAgentUnsupported):
		return "agent_capability_unavailable"
	case errors.Is(err, kubernetesresource.ErrRequestCapacity):
		return "resource_capacity_exhausted"
	case errors.Is(err, kubernetesresource.ErrResponseBudget):
		return "response_budget_exhausted"
	case errors.Is(err, kubernetesresource.ErrClusterUnauthenticated):
		return "cluster_api_unauthenticated"
	case errors.Is(err, kubernetesresource.ErrClusterAccessDenied):
		return "cluster_api_forbidden"
	case errors.Is(err, kubernetesresource.ErrClusterUnavailable):
		return "cluster_api_unavailable"
	case errors.Is(err, kubernetesresource.ErrClusterTimeout), errors.Is(err, context.DeadlineExceeded):
		return "cluster_api_timeout"
	case errors.Is(err, kubernetesresource.ErrResponseTooLarge):
		return "agent_response_too_large"
	case errors.Is(err, kubernetesresource.ErrInvalidResponse):
		return "invalid_agent_response"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "cluster_api_error"
	}
}
