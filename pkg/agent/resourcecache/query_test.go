package resourcecache

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func namespacedPod(
	namespace string,
	name string,
	node string,
	phase corev1.PodPhase,
	labelValue string,
) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       namespace,
			Name:            name,
			UID:             types.UID(namespace + "/" + name),
			ResourceVersion: "42",
			Labels:          map[string]string{"tier": labelValue},
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func loadedPodCache(t *testing.T, pods ...*corev1.Pod) *Cache {
	t.Helper()
	instance := New(nil, nil, Config{Resources: []resourceKind{podKind()}}, nil)
	objects := make([]any, 0, len(pods))
	for _, pod := range pods {
		objects = append(objects, toUnstructured(t, pod))
	}
	if err := instance.stores[podGVR].Replace(objects, "900"); err != nil {
		t.Fatal(err)
	}
	return instance
}

func decodePods(t *testing.T, page resourcesummary.Page) []resourcesummary.Pod {
	t.Helper()
	pods := make([]resourcesummary.Pod, 0, len(page.Items))
	for _, raw := range page.Items {
		var pod resourcesummary.Pod
		if err := json.Unmarshal(raw, &pod); err != nil {
			t.Fatal(err)
		}
		pods = append(pods, pod)
	}
	return pods
}

func names(pods []resourcesummary.Pod) []string {
	result := make([]string, 0, len(pods))
	for _, pod := range pods {
		result = append(result, pod.Namespace+"/"+pod.Name)
	}
	return result
}

// A cached list orders by Namespace then Name, which is the order Kubernetes
// itself lists in. Anything else would put the same object on a different page
// depending on which path answered.
func TestSummariesAreOrderedLikeAKubernetesListing(t *testing.T) {
	t.Parallel()
	instance := loadedPodCache(t,
		namespacedPod("zeta", "alpha", "node-a", corev1.PodRunning, "web"),
		namespacedPod("alpha", "zeta", "node-a", corev1.PodRunning, "web"),
		namespacedPod("alpha", "alpha", "node-b", corev1.PodPending, "batch"),
		namespacedPod("beta", "mid", "node-a", corev1.PodRunning, "web"),
	)
	page, err := instance.Summaries(Query{Resource: podGVR})
	if err != nil {
		t.Fatal(err)
	}
	got := names(decodePods(t, page))
	want := []string{"alpha/alpha", "alpha/zeta", "beta/mid", "zeta/alpha"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if page.ResourceVersion != "900" || page.Snapshot == "" {
		t.Fatalf("page provenance = %+v", page)
	}
}

func TestSummariesFilterByNamespaceLabelAndField(t *testing.T) {
	t.Parallel()
	instance := loadedPodCache(t,
		namespacedPod("default", "web-0", "node-a", corev1.PodRunning, "web"),
		namespacedPod("default", "web-1", "node-b", corev1.PodRunning, "web"),
		namespacedPod("default", "batch-0", "node-a", corev1.PodSucceeded, "batch"),
		namespacedPod("other", "web-2", "node-a", corev1.PodRunning, "web"),
	)
	cases := []struct {
		name  string
		query Query
		want  []string
	}{
		{
			name:  "namespace",
			query: Query{Resource: podGVR, Namespace: "default"},
			want:  []string{"default/batch-0", "default/web-0", "default/web-1"},
		},
		{
			name:  "label selector",
			query: Query{Resource: podGVR, LabelSelector: "tier=web"},
			want:  []string{"default/web-0", "default/web-1", "other/web-2"},
		},
		{
			name:  "set based label selector",
			query: Query{Resource: podGVR, LabelSelector: "tier in (batch)"},
			want:  []string{"default/batch-0"},
		},
		{
			name:  "field selector on the Node",
			query: Query{Resource: podGVR, FieldSelector: "spec.nodeName=node-b"},
			want:  []string{"default/web-1"},
		},
		{
			name:  "field selector on the phase",
			query: Query{Resource: podGVR, FieldSelector: "status.phase=Succeeded"},
			want:  []string{"default/batch-0"},
		},
		{
			name: "inequality",
			query: Query{
				Resource:      podGVR,
				Namespace:     "default",
				FieldSelector: "status.phase!=Succeeded",
			},
			want: []string{"default/web-0", "default/web-1"},
		},
		{
			name: "namespace and label together",
			query: Query{
				Resource:      podGVR,
				Namespace:     "default",
				LabelSelector: "tier=web",
			},
			want: []string{"default/web-0", "default/web-1"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			page, err := instance.Summaries(testCase.query)
			if err != nil {
				t.Fatal(err)
			}
			got := names(decodePods(t, page))
			if len(got) != len(testCase.want) {
				t.Fatalf("got %v, want %v", got, testCase.want)
			}
			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("got %v, want %v", got, testCase.want)
				}
			}
		})
	}
}

// A field the cache does not index is refused rather than approximated: the
// caller reads Kubernetes, which is the only place that can answer it.
func TestUnindexedFieldSelectorIsRefused(t *testing.T) {
	t.Parallel()
	instance := loadedPodCache(t,
		namespacedPod("default", "web-0", "node-a", corev1.PodRunning, "web"),
	)
	for _, selector := range []string{
		"spec.serviceAccountName=default",
		"status.podIP=10.0.0.1",
		"metadata.ownerReferences[0].name=web",
	} {
		if _, err := instance.Summaries(Query{
			Resource:      podGVR,
			FieldSelector: selector,
		}); !errors.Is(err, ErrQueryUnsupported) {
			t.Fatalf("selector %q returned %v", selector, err)
		}
	}
	// A malformed label selector is refused the same way.
	if _, err := instance.Summaries(Query{
		Resource:      podGVR,
		LabelSelector: "tier=@@@",
	}); !errors.Is(err, ErrQueryUnsupported) {
		t.Fatalf("a malformed label selector returned %v", err)
	}
}

func TestSummariesPageThroughEveryObjectExactlyOnce(t *testing.T) {
	t.Parallel()
	pods := make([]*corev1.Pod, 0, 25)
	for index := range 25 {
		pods = append(pods, namespacedPod(
			"default",
			fmt.Sprintf("web-%02d", index),
			"node-a",
			corev1.PodRunning,
			"web",
		))
	}
	instance := loadedPodCache(t, pods...)

	seen := make([]string, 0, len(pods))
	query := Query{Resource: podGVR, Limit: 7}
	snapshot := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not finish")
		}
		page, err := instance.Summaries(query)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot == "" {
			snapshot = page.Snapshot
		} else if page.Snapshot != snapshot {
			t.Fatalf("page %d came from a different snapshot", pages)
		}
		seen = append(seen, names(decodePods(t, page))...)
		if page.NextPageToken == "" {
			if page.Remaining != 0 {
				t.Fatalf("the last page still reports %d remaining", page.Remaining)
			}
			break
		}
		query.PageToken = page.NextPageToken
	}
	if len(seen) != len(pods) {
		t.Fatalf("saw %d objects across the pages, want %d", len(seen), len(pods))
	}
	unique := map[string]struct{}{}
	for index, name := range seen {
		if _, repeated := unique[name]; repeated {
			t.Fatalf("%s appeared twice", name)
		}
		unique[name] = struct{}{}
		want := fmt.Sprintf("default/web-%02d", index)
		if name != want {
			t.Fatalf("position %d = %s, want %s", index, name, want)
		}
	}
}

// A page reads the objects as they are now, so an object deleted mid-listing is
// simply absent rather than shown as it used to be.
func TestPagingReadsCurrentObjects(t *testing.T) {
	t.Parallel()
	pods := make([]*corev1.Pod, 0, 10)
	for index := range 10 {
		pods = append(pods, namespacedPod(
			"default", fmt.Sprintf("web-%02d", index),
			"node-a", corev1.PodRunning, "web",
		))
	}
	instance := loadedPodCache(t, pods...)
	first, err := instance.Summaries(Query{Resource: podGVR, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.stores[podGVR].Delete(
		toUnstructured(t, pods[7]),
	); err != nil {
		t.Fatal(err)
	}
	second, err := instance.Summaries(Query{
		Resource:  podGVR,
		Limit:     5,
		PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names(decodePods(t, second)) {
		if name == "default/web-07" {
			t.Fatal("a deleted object was returned from a paging session")
		}
	}
	if len(second.Items) != 4 {
		t.Fatalf("second page holds %d items, want 4", len(second.Items))
	}
}

func TestPageTokensAreRefusedWhenTheyDoNotBelong(t *testing.T) {
	t.Parallel()
	pods := make([]*corev1.Pod, 0, 10)
	for index := range 10 {
		pods = append(pods, namespacedPod(
			"default", fmt.Sprintf("web-%02d", index),
			"node-a", corev1.PodRunning, "web",
		))
	}
	instance := loadedPodCache(t, pods...)
	page, err := instance.Summaries(Query{Resource: podGVR, Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextPageToken == "" {
		t.Fatal("a partial listing produced no continuation")
	}

	t.Run("a malformed token", func(t *testing.T) {
		if _, err := instance.Summaries(Query{
			Resource:  podGVR,
			PageToken: "not-a-token!!",
		}); !errors.Is(err, ErrPageToken) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("a different query", func(t *testing.T) {
		if _, err := instance.Summaries(Query{
			Resource:      podGVR,
			LabelSelector: "tier=web",
			PageToken:     page.NextPageToken,
		}); !errors.Is(err, ErrPageToken) {
			t.Fatal("a token was accepted for a different query")
		}
		if _, err := instance.Summaries(Query{
			Resource:  podGVR,
			Namespace: "other",
			PageToken: page.NextPageToken,
		}); !errors.Is(err, ErrPageToken) {
			t.Fatal("a token was accepted for a different Namespace")
		}
	})

	t.Run("another Cluster's cache", func(t *testing.T) {
		// A second Agent process, standing in for another Cluster. Its session
		// table has never heard of this token.
		other := loadedPodCache(t, pods...)
		if _, err := other.Summaries(Query{
			Resource:  podGVR,
			PageToken: page.NextPageToken,
		}); !errors.Is(err, ErrPageToken) {
			t.Fatal("a token from one Cluster was accepted by another")
		}
	})

	t.Run("an expired page", func(t *testing.T) {
		instance.paging.mutex.Lock()
		for _, session := range instance.paging.sessions {
			session.createdAt = time.Now().Add(-2 * maxPagingSessionAge)
		}
		instance.paging.mutex.Unlock()
		if _, err := instance.Summaries(Query{
			Resource:  podGVR,
			PageToken: page.NextPageToken,
		}); !errors.Is(err, ErrPageToken) {
			t.Fatalf("an expired page was accepted: %v", err)
		}
	})
}

// Paging sessions are bounded: a Server that starts listings and never finishes
// them cannot grow the Agent's memory without limit.
func TestPagingSessionsAreBounded(t *testing.T) {
	t.Parallel()
	pods := make([]*corev1.Pod, 0, 10)
	for index := range 10 {
		pods = append(pods, namespacedPod(
			"default", fmt.Sprintf("web-%02d", index),
			"node-a", corev1.PodRunning, "web",
		))
	}
	instance := loadedPodCache(t, pods...)
	for range maxPagingSessions * 3 {
		if _, err := instance.Summaries(Query{Resource: podGVR, Limit: 1}); err != nil {
			t.Fatal(err)
		}
	}
	instance.paging.mutex.Lock()
	held := len(instance.paging.sessions)
	instance.paging.mutex.Unlock()
	if held > maxPagingSessions {
		t.Fatalf("%d paging sessions held, ceiling is %d", held, maxPagingSessions)
	}
}

// A section that has not listed yet has nothing true to say, and says so rather
// than returning an empty page that reads as an empty Cluster.
func TestSummariesRefuseAnUnsyncedSection(t *testing.T) {
	t.Parallel()
	instance := New(nil, nil, Config{Resources: []resourceKind{podKind()}}, nil)
	if _, err := instance.Summaries(Query{Resource: podGVR}); !errors.Is(err, ErrNotSynced) {
		t.Fatalf("got %v", err)
	}
}

func TestSummariesRefuseAnUncachedResource(t *testing.T) {
	t.Parallel()
	instance := loadedPodCache(t)
	if _, err := instance.Summaries(Query{Resource: nodeGVR}); !errors.Is(
		err,
		ErrResourceNotCached,
	) {
		t.Fatalf("got %v", err)
	}
	// A Namespace filter on a cluster-scoped resource is not something the
	// cache can express, because the resource has no Namespace.
	instance = New(nil, nil, Config{Resources: []resourceKind{nodeKind()}}, nil)
	if err := instance.stores[nodeGVR].Replace(nil, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Summaries(Query{
		Resource:  nodeGVR,
		Namespace: "default",
	}); !errors.Is(err, ErrQueryUnsupported) {
		t.Fatalf("got %v", err)
	}
}
