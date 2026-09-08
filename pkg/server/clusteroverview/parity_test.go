package clusteroverview

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/agent/resourcecache"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// listingRequester answers the Server's Resource Stream from a fixed set of
// objects, so the Server's real listing path runs end to end in this test.
type listingRequester struct {
	objects map[schema.GroupVersionResource][]*unstructured.Unstructured

	// The overview reads its sections in parallel, so the tally is shared.
	mutex  sync.Mutex
	listed map[schema.GroupVersionResource]int
}

func (requester *listingRequester) RequestResource(
	_ context.Context,
	_ string,
	request *agentv1.ResourceRequest,
	_ io.Reader,
	responseBody io.Writer,
) (*agentv1.ResourceResponse, error) {
	gvr := schema.GroupVersionResource{
		Group:    request.GetResource().GetGroup(),
		Version:  request.GetResource().GetVersion(),
		Resource: request.GetResource().GetResource(),
	}
	requester.mutex.Lock()
	requester.listed[gvr]++
	requester.mutex.Unlock()
	items := make([]any, 0, len(requester.objects[gvr]))
	for _, object := range requester.objects[gvr] {
		items = append(items, object.Object)
	}
	list := map[string]any{
		"apiVersion": gvr.GroupVersion().String(),
		"kind":       listKind(gvr.Resource),
		"metadata":   map[string]any{"resourceVersion": "1000"},
		"items":      items,
	}
	body, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	if _, err := responseBody.Write(body); err != nil {
		return nil, err
	}
	return &agentv1.ResourceResponse{
		Result:               agentv1.ResultCode_RESULT_CODE_OK,
		KubernetesStatusCode: 200,
		ContentType:          "application/json",
		BodySize:             uint64(len(body)),
	}, nil
}

func listKind(resourceName string) string {
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
	}[resourceName]
}

func parityObjects(t *testing.T) map[schema.GroupVersionResource][]*unstructured.Unstructured {
	t.Helper()
	deletion := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	always := corev1.ContainerRestartPolicyAlways
	suspended := true

	pods := []*corev1.Pod{
		parityPod("web-0", corev1.PodRunning, true, "500m", "128Mi", nil),
		parityPod("web-1", corev1.PodPending, false, "250m", "64Mi", nil),
		parityPod("batch-0", corev1.PodSucceeded, false, "2", "1Gi", nil),
		parityPod("mesh-0", corev1.PodRunning, true, "100m", "32Mi", []corev1.Container{
			{
				Name:          "sidecar",
				Image:         "registry.example.test/sidecar:1",
				RestartPolicy: &always,
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("150m"),
					corev1.ResourceMemory: resource.MustParse("48Mi"),
				}},
			},
			{
				Name:  "setup",
				Image: "registry.example.test/setup:1",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				}},
			},
		}),
	}
	pods[3].Spec.Overhead = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("50m"),
		corev1.ResourceMemory: resource.MustParse("16Mi"),
	}
	terminating := parityPod("web-2", corev1.PodRunning, true, "300m", "96Mi", nil)
	terminating.DeletionTimestamp = &deletion
	terminating.Finalizers = []string{"example.test/cleanup"}
	pods = append(pods, terminating)

	activeNamespace := &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "ns-default"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
	closingNamespace := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "retired", UID: "ns-retired",
			DeletionTimestamp: &deletion,
			Finalizers:        []string{"kubernetes"},
		},
		Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: "web", UID: "deploy-web", Generation: 3,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(2)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "web", Image: "registry.example.test/web:1"}},
			}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 3, Replicas: 2, ReadyReplicas: 2,
			UpdatedReplicas: 2, AvailableReplicas: 2,
		},
	}
	rollingOut := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: "api", UID: "deploy-api", Generation: 5,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(3)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "api", Image: "registry.example.test/api:2"}},
			}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 4, Replicas: 3, ReadyReplicas: 1,
			UpdatedReplicas: 1, AvailableReplicas: 1, UnavailableReplicas: 2,
		},
	}
	statefulSet := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: "database", UID: "sts-database", Generation: 2,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr(int32(3)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "db", Image: "registry.example.test/db:1"}},
			}},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2, Replicas: 3, ReadyReplicas: 2,
			CurrentRevision: "rev-1", UpdateRevision: "rev-2",
		},
	}
	daemonSet := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system", Name: "agent", UID: "ds-agent", Generation: 1,
		},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "agent", Image: "registry.example.test/agent:1"}},
			}},
		},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration: 1, DesiredNumberScheduled: 2, CurrentNumberScheduled: 2,
			NumberReady: 2, NumberAvailable: 2, UpdatedNumberScheduled: 2,
		},
	}
	completedJob := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: "migrate", UID: "job-migrate",
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "migrate", Image: "registry.example.test/migrate:1"}},
			}},
		},
		Status: batchv1.JobStatus{
			Succeeded: 1,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}
	suspendedCronJob := &batchv1.CronJob{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: "report", UID: "cron-report",
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 3 * * *",
			Suspend:  &suspended,
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "report", Image: "registry.example.test/report:1"}},
				}},
			}},
		},
	}

	objects := map[schema.GroupVersionResource][]*unstructured.Unstructured{}
	add := func(gvr schema.GroupVersionResource, values ...any) {
		for _, value := range values {
			raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(value)
			if err != nil {
				t.Fatal(err)
			}
			objects[gvr] = append(objects[gvr], &unstructured.Unstructured{Object: raw})
		}
	}
	add(schema.GroupVersionResource{Version: "v1", Resource: "nodes"},
		parityNode("node-a", "v1.31.4", "4", "8Gi", "110", true, false),
		parityNode("node-b", "v1.30.8", "2", "4Gi", "110", false, true),
	)
	add(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"},
		activeNamespace, closingNamespace,
	)
	for _, pod := range pods {
		add(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, pod)
	}
	add(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"},
		parityVolume("volume-a", corev1.VolumeBound, "20Gi"),
		parityVolume("volume-b", corev1.VolumeReleased, "10Gi"),
	)
	add(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"},
		parityClaim("claim-a", corev1.ClaimBound, "20Gi"),
		parityClaim("claim-b", corev1.ClaimPending, "5Gi"),
	)
	add(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		deployment, rollingOut,
	)
	add(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, statefulSet)
	add(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, daemonSet)
	add(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, completedJob)
	add(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, suspendedCronJob)
	return objects
}

func ptr[T any](value T) *T { return &value }

func parityPod(
	name string,
	phase corev1.PodPhase,
	ready bool,
	cpu string,
	memory string,
	initContainers []corev1.Container,
) *corev1.Pod {
	conditions := []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	if ready {
		conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: name, UID: types.UID(name + "-uid"),
			ResourceVersion: "100",
		},
		Spec: corev1.PodSpec{
			NodeName:       "node-a",
			InitContainers: initContainers,
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "registry.example.test/app:1",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpu),
					corev1.ResourceMemory: resource.MustParse(memory),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: phase, Conditions: conditions},
	}
}

func parityNode(
	name string,
	version string,
	cpu string,
	memory string,
	pods string,
	ready bool,
	unschedulable bool,
) *corev1.Node {
	condition := corev1.ConditionFalse
	if ready {
		condition = corev1.ConditionTrue
	}
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
		corev1.ResourcePods:   resource.MustParse(pods),
	}
	return &corev1.Node{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
		ObjectMeta: metav1.ObjectMeta{
			Name: name, UID: types.UID(name + "-uid"), ResourceVersion: "10",
		},
		Spec: corev1.NodeSpec{Unschedulable: unschedulable},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: condition}},
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: version},
			Capacity:   capacity,
			// Allocatable deliberately differs from capacity, so a projection
			// that confused the two would show up here.
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(memory),
				corev1.ResourcePods:   resource.MustParse("100"),
			},
		},
	}
}

func parityVolume(
	name string,
	phase corev1.PersistentVolumePhase,
	capacity string,
) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid")},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)},
		},
		Status: corev1.PersistentVolumeStatus{Phase: phase},
	}
}

func parityClaim(
	name string,
	phase corev1.PersistentVolumeClaimPhase,
	requested string,
) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: name, UID: types.UID(name + "-uid"),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(requested)},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
}

// The two paths must agree.
//
// The same Cluster is read once through the Server's ten listings and once
// through the Agent's resource cache, and every number the overview publishes
// has to come out the same. Otherwise upgrading an Agent would silently change
// what the page means.
func TestOverviewIsIdenticalFromListingsAndFromTheAgentCache(t *testing.T) {
	t.Parallel()
	objects := parityObjects(t)

	requester := &listingRequester{
		objects: objects,
		listed:  map[schema.GroupVersionResource]int{},
	}
	fromListing, err := NewService(
		kubernetesresource.NewService(requester),
		Config{CacheTTL: -1},
	).Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}

	listKinds := map[schema.GroupVersionResource]string{}
	seed := make([]runtime.Object, 0, 16)
	for gvr, items := range objects {
		listKinds[gvr] = listKind(gvr.Resource)
		for _, item := range items {
			seed = append(seed, item)
		}
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		listKinds,
		seed...,
	)
	cache := resourcecache.New(client, nil, resourcecache.Config{
		StartupJitter: time.Nanosecond,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cache.Start(ctx)
	defer cache.Close()
	if !cache.WaitForSync(ctx) {
		t.Fatal("the Agent cache did not synchronize")
	}
	snapshot := cache.OverviewSnapshot()
	if !snapshot.CacheSynced {
		t.Fatalf("the cache reported itself unsynchronized: %+v", snapshot.Sections)
	}
	fromCache := overviewFromSnapshot(snapshot)

	if fromCache.Partial {
		t.Fatalf("the cache path reported issues: %+v", fromCache.Issues)
	}
	if !reflect.DeepEqual(fromListing.Nodes, fromCache.Nodes) {
		t.Fatalf("nodes differ:\n listing %+v\n cache   %+v", fromListing.Nodes, fromCache.Nodes)
	}
	if !reflect.DeepEqual(fromListing.Namespaces, fromCache.Namespaces) {
		t.Fatalf("namespaces differ:\n listing %+v\n cache   %+v",
			fromListing.Namespaces, fromCache.Namespaces)
	}
	if !reflect.DeepEqual(fromListing.Pods, fromCache.Pods) {
		t.Fatalf("pods differ:\n listing %+v\n cache   %+v", fromListing.Pods, fromCache.Pods)
	}
	if !reflect.DeepEqual(fromListing.Workloads, fromCache.Workloads) {
		t.Fatalf("workloads differ:\n listing %+v\n cache   %+v",
			fromListing.Workloads, fromCache.Workloads)
	}
	if !reflect.DeepEqual(fromListing.Storage, fromCache.Storage) {
		t.Fatalf("storage differs:\n listing %+v\n cache   %+v",
			fromListing.Storage, fromCache.Storage)
	}
	if !reflect.DeepEqual(fromListing.Resources, fromCache.Resources) {
		t.Fatalf("resources differ:\n listing %+v\n cache   %+v",
			fromListing.Resources, fromCache.Resources)
	}
	// The fixture is only worth this much if it actually exercises the parts
	// that are easy to get wrong.
	if fromListing.Pods.Terminating != 1 ||
		fromListing.Resources.NonTerminalPods != 4 ||
		fromListing.Workloads.StatusCounts["progressing"] != 2 {
		t.Fatalf("the fixture stopped covering the interesting cases: %+v", fromListing)
	}
}
