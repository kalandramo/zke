package resourcecache

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// The benchmarks in this file are the repeatable measurement entry point the
// container service performance work is judged against. They answer, on
// whatever machine runs them:
//
//   - what one cached Pod costs in memory, at 1000 and at 10000 Pods;
//   - how long the first listing takes to become a usable cache;
//   - what an overview read costs once the cache is warm, which is the number
//     that has to stay flat as a Cluster grows;
//   - what a page of Pod summaries costs, first page and deep page;
//   - what the cache does under a stream of Pod changes.
//
// Every number they print comes from running them. None is written down
// anywhere as a claim.

func benchmarkPod(index int) *unstructured.Unstructured {
	name := fmt.Sprintf("workload-%06d", index)
	namespace := fmt.Sprintf("team-%02d", index%20)
	phase := corev1.PodRunning
	if index%17 == 0 {
		phase = corev1.PodPending
	}
	if index%53 == 0 {
		phase = corev1.PodSucceeded
	}
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       namespace,
			Name:            name,
			UID:             types.UID(namespace + "/" + name),
			ResourceVersion: fmt.Sprintf("%d", 100000+index),
			Labels: map[string]string{
				"app":                          fmt.Sprintf("service-%03d", index%200),
				"app.kubernetes.io/managed-by": "zke",
				"pod-template-hash":            fmt.Sprintf("%08x", index),
			},
			// The fields a compact projection must drop. They are here because
			// a real Pod carries them, and a benchmark run against Pods without
			// them would flatter the cache.
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": string(
					make([]byte, 2048),
				),
				"checksum/config": fmt.Sprintf("%064x", index),
			},
			ManagedFields: []metav1.ManagedFieldsEntry{
				{Manager: "kube-controller-manager", Operation: "Update"},
				{Manager: "kubelet", Operation: "Update", Subresource: "status"},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:           fmt.Sprintf("node-%03d", index%64),
			ServiceAccountName: "default",
			Containers: []corev1.Container{{
				Name:  "application",
				Image: "registry.example.test/application:1.4.2",
				Env: []corev1.EnvVar{
					{Name: "LOG_LEVEL", Value: "info"},
					{Name: "REGION", Value: "example-region-1"},
				},
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				}},
			}, {
				Name:  "sidecar",
				Image: "registry.example.test/sidecar:2.1.0",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				}},
			}},
		},
		Status: corev1.PodStatus{
			Phase:  phase,
			PodIP:  fmt.Sprintf("10.%d.%d.%d", index/65536%256, index/256%256, index%256),
			HostIP: fmt.Sprintf("192.168.%d.%d", index/256%256, index%256),
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "application", Ready: true, RestartCount: int32(index % 5)},
				{Name: "sidecar", Ready: true},
			},
		},
	}
	raw, err := runtimeToUnstructured(pod)
	if err != nil {
		panic(err)
	}
	return raw
}

func runtimeToUnstructured(object any) (*unstructured.Unstructured, error) {
	raw, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: raw}, nil
}

func benchmarkPods(count int) []any {
	objects := make([]any, 0, count)
	for index := range count {
		objects = append(objects, benchmarkPod(index))
	}
	return objects
}

// BenchmarkStoreReplace measures the first listing: turning a Cluster's worth of
// Pods into the compact form the cache keeps, and the memory that form costs.
func BenchmarkStoreReplace(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("pods=%d", count), func(b *testing.B) {
			objects := benchmarkPods(count)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				target := newStore(podKind(), time.Now)
				if err := target.Replace(objects, "1"); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(residentBytesPerObject(b, objects), "B/pod")
		})
	}
}

// residentBytesPerObject is what one cached object costs while it is held.
//
// It is measured against a baseline taken with nothing but the source objects
// alive, and the populated store is kept alive across the second reading, so
// what it reports is the cache's own footprint rather than the garbage the
// projection produced on the way there.
func residentBytesPerObject(b *testing.B, objects []any) float64 {
	b.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	target := newStore(podKind(), time.Now)
	if err := target.Replace(objects, "1"); err != nil {
		b.Fatal(err)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(target)
	resident := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if resident <= 0 {
		return 0
	}
	return float64(resident) / float64(len(objects))
}

// BenchmarkOverviewSnapshot is the number that decides whether the overview
// stopped being expensive: once the cache is warm, an overview read must be a
// copy of counters rather than anything that grows with the Cluster.
func BenchmarkOverviewSnapshot(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("pods=%d", count), func(b *testing.B) {
			instance := New(nil, nil, Config{Resources: []resourceKind{podKind()}}, nil)
			if err := instance.stores[podGVR].Replace(benchmarkPods(count), "1"); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if snapshot := instance.OverviewSnapshot(); snapshot.Pods.Total == 0 {
					b.Fatal("empty snapshot")
				}
			}
		})
	}
}

// BenchmarkSummaryPage measures a page of Pod summaries: the first page, which
// pays for matching and sorting, and a deep page, which reads from a paging
// session instead.
func BenchmarkSummaryPage(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		instance := New(nil, nil, Config{Resources: []resourceKind{podKind()}}, nil)
		if err := instance.stores[podGVR].Replace(benchmarkPods(count), "1"); err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("first_page/pods=%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				page, err := instance.Summaries(Query{Resource: podGVR, Limit: 100})
				if err != nil {
					b.Fatal(err)
				}
				if len(page.Items) != 100 {
					b.Fatalf("%d items", len(page.Items))
				}
			}
		})
		b.Run(fmt.Sprintf("next_page/pods=%d", count), func(b *testing.B) {
			first, err := instance.Summaries(Query{Resource: podGVR, Limit: 100})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				page, err := instance.Summaries(Query{
					Resource:  podGVR,
					Limit:     100,
					PageToken: first.NextPageToken,
				})
				if err != nil {
					b.Fatal(err)
				}
				if len(page.Items) != 100 {
					b.Fatalf("%d items", len(page.Items))
				}
			}
		})
		b.Run(fmt.Sprintf("namespace_filter/pods=%d", count), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := instance.Summaries(Query{
					Resource:  podGVR,
					Namespace: "team-07",
					Limit:     100,
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkConcurrentReads is several operators reading at once while the
// Cluster keeps changing, which is the case where a lock held too long would
// show up.
func BenchmarkConcurrentReads(b *testing.B) {
	instance := New(nil, nil, Config{Resources: []resourceKind{podKind()}}, nil)
	if err := instance.stores[podGVR].Replace(benchmarkPods(10000), "1"); err != nil {
		b.Fatal(err)
	}
	churn := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		index := 0
		for {
			select {
			case <-churn:
				return
			default:
			}
			_ = instance.stores[podGVR].Update(benchmarkPod(index % 10000))
			index++
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if snapshot := instance.OverviewSnapshot(); snapshot.Pods.Total == 0 {
				b.Error("empty snapshot")
				return
			}
		}
	})
	b.StopTimer()
	close(churn)
	writers.Wait()
}

// BenchmarkWatchEvents is a stream of Pod changes: what one event costs the
// cache, which is what decides whether a busy Cluster keeps the Agent busy.
func BenchmarkWatchEvents(b *testing.B) {
	instance := New(nil, nil, Config{Resources: []resourceKind{podKind()}}, nil)
	target := instance.stores[podGVR]
	if err := target.Replace(benchmarkPods(10000), "1"); err != nil {
		b.Fatal(err)
	}
	updates := make([]any, 0, 512)
	for index := range 512 {
		updates = append(updates, benchmarkPod(index))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		if err := target.Update(updates[index%len(updates)]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkInitialSync measures the whole path a real Agent takes: LIST from
// Kubernetes, project, and become usable.
func BenchmarkInitialSync(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("pods=%d", count), func(b *testing.B) {
			objects := benchmarkPods(count)
			seed := make([]k8sruntime.Object, 0, len(objects))
			for _, object := range objects {
				seed = append(seed, object.(*unstructured.Unstructured))
			}
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
					k8sruntime.NewScheme(),
					map[schema.GroupVersionResource]string{podGVR: "PodList"},
					seed...,
				)
				instance := New(client, nil, Config{
					Resources:     []resourceKind{podKind()},
					StartupJitter: time.Nanosecond,
				}, nil)
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				b.StartTimer()

				instance.Start(ctx)
				if !instance.WaitForSync(ctx) {
					b.Fatal("the cache did not synchronize")
				}
				if snapshot := instance.OverviewSnapshot(); snapshot.Pods.Total != int64(count) {
					b.Fatalf("cached %d Pods, expected %d", snapshot.Pods.Total, count)
				}

				b.StopTimer()
				instance.Close()
				cancel()
				b.StartTimer()
			}
		})
	}
}
