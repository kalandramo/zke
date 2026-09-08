package resourcecache

import (
	"context"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

// blockingDynamicClient answers LIST only when the test lets it, and reports how
// many listings were in flight at once.
//
// The fake dynamic client cannot be used for this: it serialises every reaction
// behind one lock, so concurrent listings would be invisible whether or not the
// cache actually bounded them.
type blockingDynamicClient struct {
	release chan struct{}

	mutex    sync.Mutex
	inFlight int
	peak     int
	started  map[schema.GroupVersionResource]int
}

func newBlockingDynamicClient() *blockingDynamicClient {
	return &blockingDynamicClient{
		release: make(chan struct{}),
		started: map[schema.GroupVersionResource]int{},
	}
}

func (client *blockingDynamicClient) Resource(
	gvr schema.GroupVersionResource,
) dynamic.NamespaceableResourceInterface {
	return &blockingResource{client: client, gvr: gvr}
}

func (client *blockingDynamicClient) enter(gvr schema.GroupVersionResource) {
	client.mutex.Lock()
	client.inFlight++
	client.started[gvr]++
	if client.inFlight > client.peak {
		client.peak = client.inFlight
	}
	client.mutex.Unlock()
	<-client.release
	client.mutex.Lock()
	client.inFlight--
	client.mutex.Unlock()
}

func (client *blockingDynamicClient) observed() (int, int, int) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	return client.inFlight, client.peak, len(client.started)
}

type blockingResource struct {
	// Embedded so the methods this test never calls do not have to be written
	// out; calling one would panic, which is the right outcome for a test that
	// started using a path it did not mean to.
	dynamic.ResourceInterface
	client *blockingDynamicClient
	gvr    schema.GroupVersionResource
}

func (resource *blockingResource) Namespace(string) dynamic.ResourceInterface {
	return resource
}

func (resource *blockingResource) List(
	ctx context.Context,
	_ metav1.ListOptions,
) (*unstructured.UnstructuredList, error) {
	resource.client.enter(resource.gvr)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion(resource.gvr.GroupVersion().String())
	list.SetKind(listKindName(resource.gvr.Resource))
	list.SetResourceVersion("1")
	return list, nil
}

func (resource *blockingResource) Watch(
	ctx context.Context,
	_ metav1.ListOptions,
) (watch.Interface, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return watch.NewRaceFreeFake(), nil
}
