package clusteroverview

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

// blockingReader lets a test hold every section of one load open, so several
// callers are provably inside Get at the same time.
type blockingReader struct {
	fakeResourceReader
	mutex   sync.Mutex
	gate    chan struct{}
	loads   map[string]int
	failing map[string]error
}

func newBlockingReader() *blockingReader {
	return &blockingReader{
		gate:    make(chan struct{}),
		loads:   map[string]int{},
		failing: map[string]error{},
	}
}

func (reader *blockingReader) enter(clusterID string) error {
	reader.mutex.Lock()
	reader.loads[clusterID]++
	err := reader.failing[clusterID]
	gate := reader.gate
	reader.mutex.Unlock()
	if gate != nil {
		<-gate
	}
	return err
}

func (reader *blockingReader) open() {
	reader.mutex.Lock()
	gate := reader.gate
	reader.gate = nil
	reader.mutex.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (reader *blockingReader) loadCount(clusterID string) int {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	return reader.loads[clusterID]
}

func (reader *blockingReader) fail(clusterID string, err error) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	reader.failing[clusterID] = err
}

func (reader *blockingReader) ListNodes(
	_ context.Context,
	input kubernetesresource.ListNodesInput,
) (kubernetesresource.NodePage, error) {
	if err := reader.enter(input.ClusterID); err != nil {
		return kubernetesresource.NodePage{}, err
	}
	return kubernetesresource.NodePage{}, nil
}

func (reader *blockingReader) ListResources(
	_ context.Context,
	input kubernetesresource.ListResourcesInput,
) (kubernetesresource.ResourcePage, error) {
	if err := reader.enter(input.ClusterID); err != nil {
		return kubernetesresource.ResourcePage{}, err
	}
	return kubernetesresource.ResourcePage{}, nil
}

func (reader *blockingReader) ListWorkloads(
	_ context.Context,
	input kubernetesresource.ListWorkloadsInput,
) (kubernetesresource.WorkloadPage, error) {
	if err := reader.enter(input.ClusterID); err != nil {
		return kubernetesresource.WorkloadPage{}, err
	}
	return kubernetesresource.WorkloadPage{}, nil
}

// The overview is ten Cluster-wide listings, so several operators opening the
// page at once must still cost one.
func TestConcurrentColdRequestsCoalescePerCluster(t *testing.T) {
	t.Parallel()
	reader := newBlockingReader()
	service := NewService(reader, Config{CacheTTL: time.Minute})

	const callers = 8
	results := make(chan error, callers)
	for range callers {
		go func() {
			_, err := service.Get(context.Background(), overviewClusterID)
			results <- err
		}()
	}
	// Every caller is inside Get before the load is allowed to finish.
	waitFor(t, func() bool { return reader.loadCount(overviewClusterID) > 0 })
	time.Sleep(20 * time.Millisecond)
	reader.open()
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	// Ten sections, read once between all eight callers.
	if count := reader.loadCount(overviewClusterID); count != 10 {
		t.Fatalf("%d section listings for %d concurrent callers, want 10", count, callers)
	}
}

// Coalescing is per Cluster: two Clusters asked at once are two loads, and
// neither Cluster is ever answered from the other one.
func TestConcurrentRequestsDoNotCoalesceAcrossClusters(t *testing.T) {
	t.Parallel()
	reader := newBlockingReader()
	service := NewService(reader, Config{CacheTTL: time.Minute})

	results := make(chan error, 2)
	for _, clusterID := range []string{overviewClusterID, otherClusterID} {
		go func() {
			_, err := service.Get(context.Background(), clusterID)
			results <- err
		}()
	}
	waitFor(t, func() bool {
		return reader.loadCount(overviewClusterID) > 0 && reader.loadCount(otherClusterID) > 0
	})
	reader.open()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for _, clusterID := range []string{overviewClusterID, otherClusterID} {
		if count := reader.loadCount(clusterID); count != 10 {
			t.Fatalf("Cluster %s produced %d section listings, want 10", clusterID, count)
		}
	}
}

// One caller giving up must not take the load away from the callers still
// waiting on it.
func TestCancelingOneCallerLeavesTheSharedLoadRunning(t *testing.T) {
	t.Parallel()
	reader := newBlockingReader()
	service := NewService(reader, Config{CacheTTL: time.Minute})

	leavingContext, leave := context.WithCancel(context.Background())
	leaving := make(chan error, 1)
	go func() {
		_, err := service.Get(leavingContext, overviewClusterID)
		leaving <- err
	}()
	waitFor(t, func() bool { return reader.loadCount(overviewClusterID) > 0 })

	staying := make(chan error, 1)
	go func() {
		_, err := service.Get(context.Background(), overviewClusterID)
		staying <- err
	}()
	time.Sleep(20 * time.Millisecond)

	leave()
	if err := <-leaving; !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that left got %v, want context.Canceled", err)
	}
	reader.open()
	select {
	case err := <-staying:
		if err != nil {
			t.Fatalf("the caller that stayed was failed by another caller leaving: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the caller that stayed never got an answer")
	}
}

// Past the fresh window the last good snapshot answers immediately, marked
// stale, while one refresh runs behind it.
func TestStaleSnapshotAnswersWhileRefreshing(t *testing.T) {
	t.Parallel()
	reader := newBlockingReader()
	reader.open()
	service := NewService(reader, Config{
		CacheTTL:    50 * time.Millisecond,
		MaxStaleAge: time.Minute,
	})
	refreshed := make(chan struct{}, 4)
	service.afterLoad = func(string, error) { refreshed <- struct{}{} }

	first, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	<-refreshed
	if first.Stale {
		t.Fatal("a fresh snapshot was marked stale")
	}
	fresh, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Stale || !fresh.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatal("a repeat inside the window did not return the same snapshot")
	}
	if count := reader.loadCount(overviewClusterID); count != 10 {
		t.Fatalf("%d section listings inside the fresh window, want 10", count)
	}

	time.Sleep(60 * time.Millisecond)
	stale, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale {
		t.Fatal("a snapshot past its window was not marked stale")
	}
	if !stale.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatal("the stale answer was not the last known good snapshot")
	}
	<-refreshed
	waitFor(t, func() bool {
		current, err := service.Get(context.Background(), overviewClusterID)
		return err == nil && !current.Stale && current.GeneratedAt.After(first.GeneratedAt)
	})
}

// An Agent that has gone away must not turn the page into an error while the
// numbers it last reported are still worth showing.
func TestLastKnownGoodSurvivesAFailingRefresh(t *testing.T) {
	t.Parallel()
	reader := newBlockingReader()
	reader.open()
	service := NewService(reader, Config{
		CacheTTL:    20 * time.Millisecond,
		MaxStaleAge: time.Minute,
	})
	loaded := make(chan error, 8)
	service.afterLoad = func(_ string, err error) { loaded <- err }

	first, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	<-loaded

	reader.fail(overviewClusterID, kubernetesresource.ErrAgentNotConnected)
	time.Sleep(30 * time.Millisecond)
	stale, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatalf("a disconnected Agent produced an error instead of the last known good: %v", err)
	}
	if !stale.Stale || !stale.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatal("the last known good snapshot was not returned")
	}
	if err := <-loaded; !errors.Is(err, kubernetesresource.ErrAgentNotConnected) {
		t.Fatalf("background refresh error = %v", err)
	}
}

// Repeated failures with nothing to fall back on stop reaching the Cluster:
// the remembered failure answers until the backoff expires.
func TestFailuresBackOffInsteadOfRetryingEveryRequest(t *testing.T) {
	t.Parallel()
	reader := newBlockingReader()
	reader.open()
	reader.fail(overviewClusterID, kubernetesresource.ErrAgentNotConnected)
	service := NewService(reader, Config{
		CacheTTL:          time.Minute,
		FailureBackoff:    time.Minute,
		MaxFailureBackoff: time.Minute,
	})
	failed := make(chan error, 8)
	service.afterLoad = func(_ string, err error) { failed <- err }

	if _, err := service.Get(context.Background(), overviewClusterID); !errors.Is(
		err,
		kubernetesresource.ErrAgentNotConnected,
	) {
		t.Fatalf("first attempt = %v", err)
	}
	<-failed
	listings := reader.loadCount(overviewClusterID)

	for range 5 {
		if _, err := service.Get(context.Background(), overviewClusterID); !errors.Is(
			err,
			kubernetesresource.ErrAgentNotConnected,
		) {
			t.Fatalf("attempt inside the backoff = %v", err)
		}
	}
	if count := reader.loadCount(overviewClusterID); count != listings {
		t.Fatalf("%d listings during the backoff, want %d", count, listings)
	}

	// A Cluster that is failing must not hold back a Cluster that is not.
	if _, err := service.Get(context.Background(), otherClusterID); err != nil {
		t.Fatalf("a healthy Cluster was refused during another Cluster's backoff: %v", err)
	}
}

func TestBackoffGrowsAndIsBounded(t *testing.T) {
	t.Parallel()
	service := NewService(&blockingReader{}, Config{
		FailureBackoff:    time.Second,
		MaxFailureBackoff: 8 * time.Second,
	})
	expected := []time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		8 * time.Second,
		8 * time.Second,
	}
	for index, want := range expected {
		if got := service.backoff(index + 1); got != want {
			t.Fatalf("backoff after %d failures = %s, want %s", index+1, got, want)
		}
	}
}

// A load started while the cache was full is detached. If a later request
// manages to admit the same Cluster, the older load must not replace or clear
// the newer in-flight entry when it finishes.
func TestDetachedLoadCannotOverwriteANewerEntry(t *testing.T) {
	t.Parallel()
	service := NewService(&fakeResourceReader{}, Config{CacheTTL: time.Minute})
	newer := &loadCall{done: make(chan struct{})}
	service.cache[overviewClusterID] = &cacheEntry{
		call:      newer,
		failures:  3,
		lastError: kubernetesresource.ErrAgentNotConnected,
	}
	older := &loadCall{done: make(chan struct{})}

	service.run(context.Background(), overviewClusterID, older)

	entry := service.cache[overviewClusterID]
	if entry.call != newer || entry.hasSnapshot || entry.failures != 3 ||
		!errors.Is(entry.lastError, kubernetesresource.ErrAgentNotConnected) {
		t.Fatalf("detached load changed a newer cache entry: %+v", entry)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition was not reached")
}
