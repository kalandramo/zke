package resourcecache

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

var serviceGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}

func kindFor(t *testing.T, gvr schema.GroupVersionResource) resourceKind {
	t.Helper()
	for _, kind := range CachedResources() {
		if kind.gvr == gvr {
			return kind
		}
	}
	t.Fatalf("%s is not a cached resource", gvr)
	return resourceKind{}
}

func testService(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            "frontend",
			UID:             types.UID("uid-frontend"),
			ResourceVersion: "300",
			Labels:          map[string]string{"app": "frontend"},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": `{"password":"hunter2"}`,
				"nginx.ingress.kubernetes.io/configuration-snippet": strings.Repeat(
					"proxy_set_header X-Secret hunter2;", 100,
				),
			},
			ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "kubectl", Operation: "Apply"},
			},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"app": "frontend"},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
			},
			ClusterIP:  "10.96.0.10",
			ClusterIPs: []string{"10.96.0.10"},
		},
	}
	return toUnstructured(t, service)
}

// A cached object keeps the spec its list shows and loses the parts that make
// an object large and the parts that carry content nobody asked the cache to
// hold.
func TestStrippedProjectionDropsWhatNoListShows(t *testing.T) {
	t.Parallel()
	target := newStore(kindFor(t, serviceGVR), time.Now)
	if err := target.Add(testService(t)); err != nil {
		t.Fatal(err)
	}
	target.mutex.RLock()
	stored := target.items[objectKey{namespace: "default", name: "frontend"}]
	target.mutex.RUnlock()

	encoded, err := json.Marshal(stored.summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"managedFields",
		"annotations",
		"hunter2",
		"last-applied-configuration",
		"configuration-snippet",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("the cached object carries %q", forbidden)
		}
	}
	for _, required := range []string{
		"frontend", "10.96.0.10", "ClusterIP", "\"port\":80", "uid-frontend",
	} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("the cached object lost %q: %s", required, encoded)
		}
	}
	if stored.uid != "uid-frontend" || stored.labels["app"] != "frontend" {
		t.Fatalf("identity was lost: %+v", stored)
	}
	if version, ok := strippedResourceVersion(stored.summary); !ok || version != "300" {
		t.Fatalf("resource version = %q", version)
	}

	// Stripping must not disturb the object the caller handed in: the reflector
	// owns it, and other consumers read it after this.
	original := testService(t)
	strip(original)
	if _, found, _ := unstructured.NestedMap(
		original.Object, "metadata", "annotations",
	); !found {
		t.Fatal("stripping modified the caller's object")
	}
}

// Nothing whose body is sensitive is cached at all. This is a boundary, not an
// optimisation, so it is asserted rather than assumed.
func TestSensitiveResourcesAreNotCached(t *testing.T) {
	t.Parallel()
	for _, forbidden := range []schema.GroupVersionResource{
		{Version: "v1", Resource: "secrets"},
		{Version: "v1", Resource: "configmaps"},
		{Version: "v1", Resource: "events"},
		{Group: "events.k8s.io", Version: "v1", Resource: "events"},
	} {
		for _, kind := range CachedResources() {
			if kind.gvr == forbidden {
				t.Fatalf("%s is cached", forbidden)
			}
		}
	}
}

// Every extended resource is optional, because a Cluster may legitimately not
// have the API group.
func TestExtendedResourcesAreOptional(t *testing.T) {
	t.Parallel()
	for _, kind := range ExtendedResources() {
		if !kind.optional {
			t.Fatalf("%s is not marked optional", kind.gvr)
		}
		if kind.section != "" {
			t.Fatalf("%s contributes to overview section %q", kind.gvr, kind.section)
		}
	}
}

// A Cluster without an API group leaves that resource unavailable and every
// other one working — including when several of them are missing at once.
func TestExtendedResourcesToleratePartialClusters(t *testing.T) {
	t.Parallel()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		k8sruntime.NewScheme(),
		map[schema.GroupVersionResource]string{
			podGVR:     "PodList",
			serviceGVR: "ServiceList",
		},
	)
	discoveryClient := &discoveryfake.FakeDiscovery{Fake: &k8stesting.Fake{
		Resources: []*metav1.APIResourceList{{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "pods", Namespaced: true},
				{Name: "services", Namespaced: true},
			},
		}},
	}}
	instance := New(client, discoveryClient, Config{
		Resources: []resourceKind{
			podKind(),
			kindFor(t, serviceGVR),
			kindFor(t, schema.GroupVersionResource{
				Group: "networking.k8s.io", Version: "v1", Resource: "ingresses",
			}),
		},
		StartupJitter: time.Nanosecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	instance.Start(ctx)
	defer instance.Close()
	waitFor(t, func() bool {
		page, err := instance.Summaries(Query{Resource: serviceGVR})
		return err == nil && page.Items != nil
	})

	// The Ingress API is not installed, so its list is not cached and the caller
	// reads Kubernetes for it. That is a different answer from "there are none".
	if _, err := instance.Summaries(Query{Resource: schema.GroupVersionResource{
		Group: "networking.k8s.io", Version: "v1", Resource: "ingresses",
	}}); err == nil {
		t.Fatal("an absent API answered a query")
	}
}
