package resourcecache

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The first listing of a resource is a listing of a whole Cluster. Doing all of
// them at once is how a fleet of Agents reconnecting turns into a spike against
// the control planes they just reconnected to, so only a bounded number may be
// listing at a time.
func TestInitialSyncConcurrencyIsBounded(t *testing.T) {
	t.Parallel()
	kinds := CoreResources()
	client := newBlockingDynamicClient()

	const limit = 2
	instance := New(client, nil, Config{
		Resources:       kinds,
		SyncConcurrency: limit,
		StartupJitter:   time.Nanosecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	instance.Start(ctx)
	defer instance.Close()

	// Let the bound be reached and held long enough that an unbounded
	// implementation would have started every resource by now.
	waitFor(t, func() bool {
		inFlight, _, _ := client.observed()
		return inFlight == limit
	})
	time.Sleep(200 * time.Millisecond)
	_, peak, started := client.observed()
	if peak > limit {
		t.Fatalf("%d resources listed at once, limit is %d", peak, limit)
	}
	if started >= len(kinds) {
		t.Fatalf("%d of %d resources started listing behind a bound of %d",
			started, len(kinds), limit)
	}

	close(client.release)
	if !instance.WaitForSync(ctx) {
		t.Fatal("the cache did not synchronize once the listings were let through")
	}
	if _, finalPeak, finalStarted := client.observed(); finalStarted != len(kinds) ||
		finalPeak > limit {
		t.Fatalf("%d of %d resources listed, peak concurrency %d",
			finalStarted, len(kinds), finalPeak)
	}
}

// A resource that repeatedly fails its first LIST must not hold one of the
// startup slots forever. Other resources can still warm and serve their pages.
func TestFailedInitialListsDoNotStarveOtherResources(t *testing.T) {
	t.Parallel()
	kinds := CoreResources()[:3]
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kinds[0].gvr: "NodeList",
			kinds[1].gvr: "NamespaceList",
			kinds[2].gvr: "PodList",
		},
	)
	for _, resource := range []string{"nodes", "namespaces"} {
		resource := resource
		client.PrependReactor("list", resource, func(
			k8stesting.Action,
		) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Resource: resource},
				"",
				errors.New("denied"),
			)
		})
	}
	instance := New(client, nil, Config{
		Resources:       kinds,
		SyncConcurrency: 2,
		StartupJitter:   time.Nanosecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	instance.Start(ctx)
	defer instance.Close()

	waitFor(t, func() bool {
		_, synced := instance.stores[podGVR].state()
		return synced
	})
}

// The wait before the first listing is random, so a Server restart that
// reconnects every Agent does not put every Cluster's control plane under the
// same spike at the same instant.
func TestStartupJitterIsRandomAndBounded(t *testing.T) {
	t.Parallel()
	const window = time.Second
	seen := map[time.Duration]struct{}{}
	for range 200 {
		delay := jitter(window)
		if delay < 0 || delay >= window {
			t.Fatalf("jitter %s is outside [0, %s)", delay, window)
		}
		seen[delay] = struct{}{}
	}
	if len(seen) < 100 {
		t.Fatalf("jitter produced only %d distinct delays in 200 draws", len(seen))
	}
	if jitter(0) != 0 {
		t.Fatal("a zero window produced a delay")
	}
}

func listKindName(resource string) string {
	return map[string]string{
		"nodes":                  "NodeList",
		"namespaces":             "NamespaceList",
		"pods":                   "PodList",
		"persistentvolumes":      "PersistentVolumeList",
		"persistentvolumeclaims": "PersistentVolumeClaimList",
		"deployments":            "DeploymentList",
		"statefulsets":           "StatefulSetList",
		"daemonsets":             "DaemonSetList",
		"jobs":                   "JobList",
		"cronjobs":               "CronJobList",
	}[resource]
}
