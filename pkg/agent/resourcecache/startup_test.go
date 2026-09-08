package resourcecache

import (
	"context"
	"testing"
	"time"
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
