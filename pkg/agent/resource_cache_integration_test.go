package agent

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/agent/resourcecache"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func cachePod(t *testing.T, name string, cpu string, memory string) *unstructured.Unstructured {
	t.Helper()
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: name, UID: types.UID("uid-" + name), ResourceVersion: "1",
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-a",
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "registry.example.test/app:1",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpu),
					corev1.ResourceMemory: resource.MustParse(memory),
				}},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	raw, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: raw}
}

// The Cluster overview travels from the Agent's cache to the Server over a real
// QUIC connection, and an Agent that has no cache is told apart from one that
// does — which is what makes a Server upgrade ahead of its Agents safe.
func TestOverviewSnapshotOverRealQUIC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real QUIC listener timing is not stable on Windows")
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		k8sruntime.NewScheme(),
		resourceCacheIntegrationListKinds(),
		cachePod(t, "web-0", "500m", "128Mi"),
		cachePod(t, "web-1", "250m", "64Mi"),
	)
	cache := resourcecache.New(client, nil, resourcecache.Config{
		StartupJitter: time.Nanosecond,
	}, nil)
	cacheContext, stopCache := context.WithCancel(context.Background())
	defer stopCache()
	cache.Start(cacheContext)
	defer cache.Close()

	limits := defaultResourceTestLimits()
	environment := startAgentStreamEnvironment(
		t,
		connectionServices{
			resourceHandler: newKubernetesResourceHandler(
				client, nil, limits.maxBodyBytes, 0, "zke-system",
			),
			resourceCacheHandler: newKubernetesResourceCacheHandler(
				cache, limits.maxBodyBytes, 0,
			),
		},
		limits,
	)
	service := kubernetesresource.NewService(environment.manager)

	ctx, cancel := context.WithTimeout(environment.ctx, 10*time.Second)
	defer cancel()
	deadline := time.Now().Add(8 * time.Second)
	for {
		snapshot, err := service.ClusterOverviewSnapshot(ctx, testClusterID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Pods.Total == 2 {
			if snapshot.Resources.CPURequestedMillis != 750 ||
				snapshot.Resources.MemoryRequestedBytes != 192*1024*1024 ||
				snapshot.Pods.Ready != 2 {
				t.Fatalf("unexpected snapshot totals: %+v", snapshot)
			}
			if snapshot.GeneratedAt.IsZero() || len(snapshot.Sections) == 0 {
				t.Fatalf("snapshot carries no provenance: %+v", snapshot)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cache never reached the expected contents: %+v", snapshot)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The fake dynamic client panics when a Reflector lists a GVR whose ListKind
// was not registered. Production discovery does not need this mapping, but the
// integration cache starts every built-in resource just like a real Agent, so
// its fake must describe all of them even when only Pods contain test data.
func resourceCacheIntegrationListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "nodes"}:                                          "NodeList",
		{Version: "v1", Resource: "namespaces"}:                                     "NamespaceList",
		{Version: "v1", Resource: "pods"}:                                           "PodList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:                     "DeploymentList",
		{Group: "apps", Version: "v1", Resource: "statefulsets"}:                    "StatefulSetList",
		{Group: "apps", Version: "v1", Resource: "daemonsets"}:                      "DaemonSetList",
		{Group: "batch", Version: "v1", Resource: "jobs"}:                           "JobList",
		{Group: "batch", Version: "v1", Resource: "cronjobs"}:                       "CronJobList",
		{Version: "v1", Resource: "persistentvolumes"}:                              "PersistentVolumeList",
		{Version: "v1", Resource: "persistentvolumeclaims"}:                         "PersistentVolumeClaimList",
		{Version: "v1", Resource: "services"}:                                       "ServiceList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}:          "IngressList",
		{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}:        "StorageClassList",
		{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}: "HorizontalPodAutoscalerList",
		{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}:          "PodDisruptionBudgetList",
		{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}:    "NetworkPolicyList",
		{Version: "v1", Resource: "resourcequotas"}:                                 "ResourceQuotaList",
		{Version: "v1", Resource: "limitranges"}:                                    "LimitRangeList",
		{Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses"}:    "PriorityClassList",
	}
}

func TestResourceCacheIntegrationFakeCoversEveryCachedResource(t *testing.T) {
	t.Parallel()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		k8sruntime.NewScheme(),
		resourceCacheIntegrationListKinds(),
	)
	cache := resourcecache.New(client, nil, resourcecache.Config{
		StartupJitter: time.Nanosecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cache.Start(ctx)
	defer cache.Close()
	if !cache.WaitForSync(ctx) {
		t.Fatal("the integration fake did not cover every cached resource")
	}
}

// An Agent that predates the cache never receives the new request: it does not
// advertise the capability, so the Server is told so before opening a Stream
// and falls back to the listings it has always used.
func TestOverviewSnapshotFallsBackForAnAgentWithoutACache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real QUIC listener timing is not stable on Windows")
	}
	limits := defaultResourceTestLimits()
	environment := startAgentStreamEnvironment(
		t,
		connectionServices{
			resourceHandler: newKubernetesResourceHandler(
				dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme()),
				nil,
				limits.maxBodyBytes,
				0,
				"zke-system",
			),
		},
		limits,
	)
	service := kubernetesresource.NewService(environment.manager)

	ctx, cancel := context.WithTimeout(environment.ctx, 5*time.Second)
	defer cancel()
	_, err := service.ClusterOverviewSnapshot(ctx, testClusterID)
	if !errors.Is(err, kubernetesresource.ErrAgentUnsupported) {
		t.Fatalf("an Agent without a cache reported %v", err)
	}
}
