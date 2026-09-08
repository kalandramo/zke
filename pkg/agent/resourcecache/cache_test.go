package resourcecache

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

var (
	podGVR  = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	nodeGVR = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
)

func podKind() resourceKind {
	for _, kind := range CoreResources() {
		if kind.gvr == podGVR {
			return kind
		}
	}
	panic("pods are not a core cached resource")
}

func nodeKind() resourceKind {
	for _, kind := range CoreResources() {
		if kind.gvr == nodeGVR {
			return kind
		}
	}
	panic("nodes are not a core cached resource")
}

// newFakeDynamicClient knows the list kinds for the resources these tests
// cache, which the fake dynamic client cannot infer from an empty scheme.
func newFakeDynamicClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			podGVR:  "PodList",
			nodeGVR: "NodeList",
		},
		objects...,
	)
}

func toUnstructured(t *testing.T, object runtime.Object) *unstructured.Unstructured {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: raw}
}

func testPod(name string, phase corev1.PodPhase, ready bool, cpu string, memory string) *corev1.Pod {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            name,
			UID:             types.UID("uid-" + name),
			ResourceVersion: "100",
			Labels:          map[string]string{"app": name},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "registry.example.test/app:1",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
	if cpu != "" {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if memory != "" {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse(memory)
	}
	condition := corev1.ConditionFalse
	if ready {
		condition = corev1.ConditionTrue
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: condition}}
	return pod
}

func testNode(name string, version string, cpu string, memory string, pods string) *corev1.Node {
	return &corev1.Node{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			UID:             types.UID("uid-" + name),
			ResourceVersion: "10",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: version},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
				corev1.ResourcePods:   resource.MustParse(pods),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
				corev1.ResourcePods:   resource.MustParse(pods),
			},
		},
	}
}

// Added, Updated and Deleted each move the totals by exactly what the object
// contributes, because an update subtracts the old contribution before adding
// the new one.
func TestStoreAggregatesEventsIncrementally(t *testing.T) {
	t.Parallel()
	target := newStore(podKind(), time.Now)

	running := testPod("running", corev1.PodRunning, true, "500m", "128Mi")
	if err := target.Add(toUnstructured(t, running)); err != nil {
		t.Fatal(err)
	}
	totals, _ := target.snapshotTotals()
	if totals.total != 1 || totals.ready != 1 || totals.notReady != 0 ||
		totals.cpuRequestedMillis != 500 ||
		totals.memoryRequestedBytes != 128*1024*1024 ||
		totals.nonTerminal != 1 ||
		totals.statusCounts["running"] != 1 {
		t.Fatalf("after Added: %+v", totals)
	}

	// The same Pod, now not ready and asking for more.
	updated := testPod("running", corev1.PodRunning, false, "1", "256Mi")
	updated.ResourceVersion = "101"
	if err := target.Update(toUnstructured(t, updated)); err != nil {
		t.Fatal(err)
	}
	totals, health := target.snapshotTotals()
	if totals.total != 1 || totals.ready != 0 || totals.notReady != 1 ||
		totals.cpuRequestedMillis != 1000 ||
		totals.memoryRequestedBytes != 256*1024*1024 {
		t.Fatalf("after Updated: %+v", totals)
	}
	if health.ResourceVersion != "101" {
		t.Fatalf("section ResourceVersion = %q", health.ResourceVersion)
	}

	if err := target.Delete(toUnstructured(t, updated)); err != nil {
		t.Fatal(err)
	}
	totals, _ = target.snapshotTotals()
	if totals.total != 0 || totals.notReady != 0 || totals.cpuRequestedMillis != 0 ||
		totals.memoryRequestedBytes != 0 || len(totals.statusCounts) != 0 {
		t.Fatalf("after Deleted: %+v", totals)
	}
}

// A watch that falls behind reports the disappearance without the object. The
// tombstone still carries the identity, and dropping the event would leave the
// object counted forever.
func TestStoreRemovesFromTombstone(t *testing.T) {
	t.Parallel()
	target := newStore(podKind(), time.Now)
	pod := testPod("gone", corev1.PodRunning, true, "250m", "64Mi")
	if err := target.Add(toUnstructured(t, pod)); err != nil {
		t.Fatal(err)
	}
	tombstone := deletedFinalStateUnknown("default/gone", toUnstructured(t, pod))
	if err := target.Delete(tombstone); err != nil {
		t.Fatal(err)
	}
	totals, _ := target.snapshotTotals()
	if totals.total != 0 || totals.cpuRequestedMillis != 0 {
		t.Fatalf("a tombstone did not remove the object: %+v", totals)
	}

	// The same, with only the key: nothing but the key survives some watch gaps.
	if err := target.Add(toUnstructured(t, pod)); err != nil {
		t.Fatal(err)
	}
	if err := target.Delete(deletedFinalStateUnknown("default/gone", nil)); err != nil {
		t.Fatal(err)
	}
	totals, _ = target.snapshotTotals()
	if totals.total != 0 {
		t.Fatalf("a key-only tombstone did not remove the object: %+v", totals)
	}
}

// A re-list installs the new contents in one step. A reader must never see the
// half-filled state, which would look exactly like a Cluster that had just lost
// most of its Pods.
func TestStoreReplaceIsAtomic(t *testing.T) {
	t.Parallel()
	target := newStore(podKind(), time.Now)
	first := []any{
		toUnstructured(t, testPod("a", corev1.PodRunning, true, "100m", "16Mi")),
		toUnstructured(t, testPod("b", corev1.PodRunning, true, "100m", "16Mi")),
	}
	if err := target.Replace(first, "500"); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var observedZero atomic.Bool
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			totals, health := target.snapshotTotals()
			if health.Synced && totals.total == 0 {
				observedZero.Store(true)
				return
			}
		}
	}()

	for range 200 {
		replacement := make([]any, 0, 64)
		for index := range 64 {
			replacement = append(replacement, toUnstructured(t, testPod(
				"pod-"+string(rune('a'+index%26))+string(rune('a'+index/26)),
				corev1.PodRunning, true, "100m", "16Mi",
			)))
		}
		if err := target.Replace(replacement, "600"); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	if observedZero.Load() {
		t.Fatal("a reader saw an empty section during a re-list")
	}
	totals, health := target.snapshotTotals()
	if totals.total != 64 || health.ResourceVersion != "600" || !health.Synced {
		t.Fatalf("after Replace: totals=%+v health=%+v", totals, health)
	}
}

// Nothing about a Pod that a page does not show survives the projection: no
// managedFields, no annotations, no environment.
func TestCachedSummariesDropEverythingThePagesDoNotShow(t *testing.T) {
	t.Parallel()
	pod := testPod("secretive", corev1.PodRunning, true, "100m", "16Mi")
	pod.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: "Update"}}
	pod.Annotations = map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": "{\"password\":\"hunter2\"}",
	}
	pod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "TOKEN", Value: "super-secret-token"}}
	pod.Spec.Volumes = []corev1.Volume{{
		Name: "credentials",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: "database-password"},
		},
	}}

	target := newStore(podKind(), time.Now)
	if err := target.Add(toUnstructured(t, pod)); err != nil {
		t.Fatal(err)
	}
	target.mutex.RLock()
	stored := target.items[objectKey{namespace: "default", name: "secretive"}]
	target.mutex.RUnlock()
	encoded, err := json.Marshal(stored.summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"managedFields",
		"hunter2",
		"super-secret-token",
		"database-password",
		"last-applied-configuration",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("the cached summary carries %q: %s", forbidden, encoded)
		}
	}
	summary, ok := stored.summary.(resourcesummary.Pod)
	if !ok {
		t.Fatalf("cached summary is %T", stored.summary)
	}
	if summary.Name != "secretive" || summary.NodeName != "node-a" || !summary.Ready {
		t.Fatalf("the projection lost fields the pages do need: %+v", summary)
	}
}

// One GVR gets one watch in one Agent process, even if it is registered twice.
func TestCacheCreatesOneInformerPerResource(t *testing.T) {
	t.Parallel()
	kinds := append(CoreResources(), podKind(), nodeKind())
	instance := New(nil, nil, Config{Resources: kinds}, nil)
	if len(instance.stores) != len(CoreResources()) {
		t.Fatalf("%d stores for %d registrations", len(instance.stores), len(kinds))
	}
	if len(instance.order) != len(instance.stores) {
		t.Fatalf("registration order has %d entries for %d stores",
			len(instance.order), len(instance.stores))
	}
}

func TestCacheListsAndWatchesOncePerResource(t *testing.T) {
	t.Parallel()
	client := newFakeDynamicClient()
	watcher := watch.NewRaceFreeFake()
	var lists, watches atomic.Int64
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return false, nil, nil
	})
	client.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		watches.Add(1)
		return true, watcher, nil
	})
	if err := client.Tracker().Add(toUnstructured(
		t,
		testPod("existing", corev1.PodRunning, true, "250m", "64Mi"),
	)); err != nil {
		t.Fatal(err)
	}

	instance := New(client, nil, Config{
		Resources:     []resourceKind{podKind()},
		StartupJitter: time.Nanosecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	instance.Start(ctx)
	defer instance.Close()
	if !instance.WaitForSync(ctx) {
		t.Fatal("the cache did not synchronize")
	}

	snapshot := instance.OverviewSnapshot()
	if !snapshot.CacheSynced || snapshot.Pods.Total != 1 {
		t.Fatalf("after the initial listing: %+v", snapshot.Pods)
	}

	// Repeated overview reads must cost nothing on the Kubernetes side.
	before := lists.Load()
	for range 20 {
		if snapshot := instance.OverviewSnapshot(); snapshot.Pods.Total != 1 {
			t.Fatalf("overview changed without a Cluster change: %+v", snapshot.Pods)
		}
	}
	if after := lists.Load(); after != before {
		t.Fatalf("%d listings for 20 overview reads", after-before)
	}

	// A watch event updates the same cache the overview reads.
	watcher.Add(toUnstructured(t, testPod("arrived", corev1.PodRunning, true, "500m", "128Mi")))
	waitFor(t, func() bool { return instance.OverviewSnapshot().Pods.Total == 2 })
	snapshot = instance.OverviewSnapshot()
	if snapshot.Resources.CPURequestedMillis != 750 {
		t.Fatalf("requests after a watch event = %d", snapshot.Resources.CPURequestedMillis)
	}
	if lists.Load() != before {
		t.Fatal("a watch event caused a new listing")
	}
}

// A watch that cannot be resumed is allowed to re-list, and the numbers come
// back rather than staying broken.
func TestCacheRecoversFromAnExpiredResourceVersion(t *testing.T) {
	t.Parallel()
	client := newFakeDynamicClient()
	var watches atomic.Int64
	client.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		if watches.Add(1) == 1 {
			return true, nil, apierrors.NewResourceExpired("too old resource version")
		}
		return true, watch.NewRaceFreeFake(), nil
	})
	if err := client.Tracker().Add(toUnstructured(
		t,
		testPod("existing", corev1.PodRunning, true, "250m", "64Mi"),
	)); err != nil {
		t.Fatal(err)
	}

	instance := New(client, nil, Config{
		Resources:     []resourceKind{podKind()},
		StartupJitter: time.Nanosecond,
		RetryInterval: time.Millisecond,
		MaxRetryWait:  5 * time.Millisecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	instance.Start(ctx)
	defer instance.Close()
	if !instance.WaitForSync(ctx) {
		t.Fatal("the cache did not synchronize")
	}
	waitFor(t, func() bool {
		snapshot := instance.OverviewSnapshot()
		return snapshot.CacheSynced && snapshot.Pods.Total == 1
	})
	waitFor(t, func() bool {
		for _, section := range instance.OverviewSnapshot().Sections {
			if section.Section == SectionPods && section.WatchRestarts > 0 {
				return true
			}
		}
		return false
	})
}

// An optional API a Cluster does not have leaves one section unavailable and
// every other section working.
func TestCacheToleratesAnAbsentAPI(t *testing.T) {
	t.Parallel()
	client := newFakeDynamicClient()
	discoveryClient := &discoveryfake.FakeDiscovery{Fake: &k8stesting.Fake{
		Resources: []*metav1.APIResourceList{{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{{Name: "pods", Namespaced: true}},
		}},
	}}
	if err := client.Tracker().Add(toUnstructured(
		t,
		testPod("existing", corev1.PodRunning, true, "250m", "64Mi"),
	)); err != nil {
		t.Fatal(err)
	}

	instance := New(client, discoveryClient, Config{
		Resources:     []resourceKind{podKind(), nodeKind()},
		StartupJitter: time.Nanosecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	instance.Start(ctx)
	defer instance.Close()
	waitFor(t, func() bool { return instance.OverviewSnapshot().Pods.Total == 1 })

	snapshot := instance.OverviewSnapshot()
	nodes := sectionState(t, snapshot, SectionNodes)
	if nodes.Synced || nodes.Code != "resource_absent" {
		t.Fatalf("an absent resource reported %+v", nodes)
	}
	if snapshot.Nodes.Total != 0 || snapshot.CacheSynced {
		t.Fatal("an absent section was reported as a synchronized zero")
	}
	pods := sectionState(t, snapshot, SectionPods)
	if !pods.Synced || pods.Code != "" {
		t.Fatalf("an available resource was disturbed by an absent one: %+v", pods)
	}
}

// A section that has not listed yet says so. Reporting it as zero would be a
// confident wrong number in front of an operator.
func TestUnsyncedSectionIsNotReportedAsZero(t *testing.T) {
	t.Parallel()
	instance := New(nil, nil, Config{Resources: []resourceKind{podKind()}}, nil)
	snapshot := instance.OverviewSnapshot()
	if snapshot.CacheSynced {
		t.Fatal("an empty cache reported itself synchronized")
	}
	if !snapshot.Stale {
		t.Fatal("an unsynchronized snapshot was not marked stale")
	}
	pods := sectionState(t, snapshot, SectionPods)
	if pods.Synced || pods.Code != "warming_up" {
		t.Fatalf("an unsynchronized section reported %+v", pods)
	}
	if !snapshot.LastSuccessAt.IsZero() {
		t.Fatal("a cache that never succeeded reported a success time")
	}
}

// A list-only cache may still be warming or temporarily unavailable while all
// overview counters are ready. Since the overview does not read that resource,
// its state must not turn a complete overview into a partial one.
func TestListOnlyResourceDoesNotAffectOverviewReadiness(t *testing.T) {
	t.Parallel()
	listOnly := ExtendedResources()[0]
	instance := New(nil, nil, Config{
		Resources: []resourceKind{podKind(), listOnly},
	}, nil)
	if err := instance.stores[podGVR].Replace([]any{}, "100"); err != nil {
		t.Fatal(err)
	}
	instance.stores[listOnly.gvr].recordFailure("cluster_api_unavailable")

	snapshot := instance.OverviewSnapshot()
	if !snapshot.CacheSynced || snapshot.Stale {
		t.Fatalf("a list-only resource affected overview readiness: %+v", snapshot)
	}
	if len(snapshot.Sections) != 1 || snapshot.Sections[0].Section != SectionPods {
		t.Fatalf("overview sections include a list-only resource: %+v", snapshot.Sections)
	}
}

// A section that fails after having synchronized keeps answering with the last
// numbers it observed, and says that is what it is doing.
func TestFailingSectionKeepsItsLastNumbers(t *testing.T) {
	t.Parallel()
	instance := New(nil, nil, Config{Resources: []resourceKind{podKind()}}, nil)
	target := instance.stores[podGVR]
	if err := target.Replace([]any{
		toUnstructured(t, testPod("a", corev1.PodRunning, true, "100m", "16Mi")),
	}, "700"); err != nil {
		t.Fatal(err)
	}
	target.recordFailure("cluster_api_forbidden")

	snapshot := instance.OverviewSnapshot()
	if snapshot.Pods.Total != 1 {
		t.Fatalf("a failing section lost its last numbers: %+v", snapshot.Pods)
	}
	if !snapshot.Stale {
		t.Fatal("a failing section did not mark the snapshot stale")
	}
	pods := sectionState(t, snapshot, SectionPods)
	if !pods.Synced || pods.Code != "cluster_api_forbidden" {
		t.Fatalf("section state = %+v", pods)
	}
}

// Close stops every watch and every goroutine, so an Agent shutdown is final.
func TestCloseReleasesEverything(t *testing.T) {
	t.Parallel()
	client := newFakeDynamicClient()
	watcher := watch.NewRaceFreeFake()
	client.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})
	instance := New(client, nil, Config{
		Resources:     []resourceKind{podKind()},
		StartupJitter: time.Nanosecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	instance.Start(ctx)
	if !instance.WaitForSync(ctx) {
		t.Fatal("the cache did not synchronize")
	}

	closed := make(chan struct{})
	go func() {
		instance.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	// Closing twice is what a shutdown path that also cancels its Context does.
	instance.Close()
}

func sectionState(
	t *testing.T,
	snapshot resourcesummary.OverviewSnapshot,
	section string,
) resourcesummary.SectionState {
	t.Helper()
	for _, state := range snapshot.Sections {
		if state.Section == section {
			return state
		}
	}
	t.Fatalf("section %q is missing from the snapshot", section)
	return resourcesummary.SectionState{}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition was not reached")
}

func deletedFinalStateUnknown(key string, object any) cache.DeletedFinalStateUnknown {
	return cache.DeletedFinalStateUnknown{Key: key, Obj: object}
}

var _ dynamic.Interface = (*dynamicfake.FakeDynamicClient)(nil)
