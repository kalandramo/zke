package resourcecache

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	k8stesting "k8s.io/client-go/testing"
)

// A write goes to Kubernetes and the cache learns about it from the watch. It
// is never written into directly: a hand-placed object races the watch event
// for the same change, and whichever lost would leave the cache holding
// something Kubernetes never said.
func TestTheCacheOnlyLearnsFromKubernetes(t *testing.T) {
	t.Parallel()
	watcher := watch.NewRaceFreeFake()
	client := newFakeDynamicClient()
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
	defer instance.Close()
	if !instance.WaitForSync(ctx) {
		t.Fatal("the cache did not synchronize")
	}

	// A write lands in Kubernetes. Until the watch reports it, the cache still
	// describes what it has actually observed.
	created := testPod("created", corev1.PodRunning, true, "500m", "128Mi")
	if _, err := client.
		Resource(podGVR).
		Namespace("default").
		Create(ctx, toUnstructured(t, created), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if total := instance.OverviewSnapshot().Pods.Total; total != 0 {
		t.Fatalf("the cache counted %d Pods before observing any", total)
	}

	watcher.Add(toUnstructured(t, created))
	waitFor(t, func() bool { return instance.OverviewSnapshot().Pods.Total == 1 })

	// And a delete: the cache lets go only when the watch says so.
	if err := client.
		Resource(podGVR).
		Namespace("default").
		Delete(ctx, "created", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if total := instance.OverviewSnapshot().Pods.Total; total != 1 {
		t.Fatalf("the cache dropped a Pod before observing the deletion: %d", total)
	}
	watcher.Delete(toUnstructured(t, created))
	waitFor(t, func() bool { return instance.OverviewSnapshot().Pods.Total == 0 })
}

// Close has to be final: after it returns, nothing this cache started can still
// be watching Kubernetes or holding a store alive.
func TestCloseLeavesNoGoroutinesBehind(t *testing.T) {
	client := newFakeDynamicClient()
	watcher := watch.NewRaceFreeFake()
	client.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, watcher, nil
	})
	settle(t)
	before := runtime.NumGoroutine()

	for range 5 {
		instance := New(client, nil, Config{
			Resources:     []resourceKind{podKind(), nodeKind()},
			StartupJitter: time.Nanosecond,
		}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		instance.Start(ctx)
		if !instance.WaitForSync(ctx) {
			cancel()
			t.Fatal("the cache did not synchronize")
		}
		instance.Close()
		cancel()
	}

	settle(t)
	after := runtime.NumGoroutine()
	// A small margin: the runtime and the fake client keep goroutines of their
	// own, and this is a leak check rather than an exact accounting.
	if after > before+4 {
		t.Fatalf("goroutines grew from %d to %d across five cache lifetimes", before, after)
	}
}

// A cache whose Context ends stops even if nobody calls Close, which is what
// makes an Agent shutdown enough on its own.
func TestCacheStopsWithItsContext(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	instance.Start(ctx)
	syncContext, cancelSync := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelSync()
	if !instance.WaitForSync(syncContext) {
		t.Fatal("the cache did not synchronize")
	}

	cancel()
	stopped := make(chan struct{})
	go func() {
		instance.Close()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the cache did not stop with its Context")
	}
}

// What the cache records about a failure is a short code, never the API
// Server's own message: those quote object names and sometimes object content,
// and this string travels to the Server and into logs.
func TestFailureCodesCarryNoUpstreamDetail(t *testing.T) {
	t.Parallel()
	secretish := "pods \"database-credentials\" is forbidden: token hunter2"
	cases := []struct {
		err  error
		want string
	}{
		{apierrors.NewResourceExpired(secretish), "resource_version_expired"},
		{apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"},
			"database-credentials",
			errors.New(secretish),
		), "cluster_api_forbidden"},
		{apierrors.NewUnauthorized(secretish), "cluster_api_unauthenticated"},
		{apierrors.NewTooManyRequestsError(secretish), "cluster_api_throttled"},
		{apierrors.NewServiceUnavailable(secretish), "cluster_api_unavailable"},
		{context.DeadlineExceeded, "cluster_api_timeout"},
		{errors.New(secretish), "cluster_api_error"},
	}
	for _, testCase := range cases {
		code := failureCode(testCase.err)
		if code != testCase.want {
			t.Fatalf("failureCode(%v) = %q, want %q", testCase.err, code, testCase.want)
		}
		if strings.Contains(code, "hunter2") ||
			strings.Contains(code, "database-credentials") {
			t.Fatalf("a failure code carried upstream detail: %q", code)
		}
	}
}

func settle(t *testing.T) {
	t.Helper()
	for range 20 {
		runtime.Gosched()
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
}
