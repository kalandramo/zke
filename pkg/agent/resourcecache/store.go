package resourcecache

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

// objectKey identifies one cached object within its resource. Namespace is
// empty for cluster-scoped resources.
type objectKey struct {
	namespace string
	name      string
}

// entry is one object as the cache keeps it: the compact summary the Server
// reads, the contribution the overview counts, and just enough identity to
// filter and sort without touching the summary.
type entry struct {
	key          objectKey
	uid          string
	labels       map[string]string
	createdAt    time.Time
	summary      any
	contribution contribution
}

// store is the compact accumulator one reflector writes into.
//
// It deliberately does not keep Kubernetes objects. Every object that arrives
// is projected on the way in and the original is dropped, so the memory this
// holds is a function of what the pages display rather than of how large the
// objects happen to be — no managedFields, no annotations, no Secret or
// ConfigMap bodies, and nothing a list never shows.
//
// It implements cache.ReflectorStore, which is what lets client-go's reflector
// drive it: one LIST becomes Replace, and every watch event becomes Add, Update
// or Delete.
type store struct {
	kind resourceKind

	mutex  sync.RWMutex
	items  map[objectKey]entry
	totals sectionTotals
	// generation changes on every mutation. A paging token carries the
	// generation it was issued against, so a page can say which snapshot of the
	// cache it belongs to.
	generation uint64

	synced          bool
	resourceVersion string
	lastSuccessAt   time.Time
	lastEventAt     time.Time
	watchRestarts   int64
	// failureCode is a short, stable code — never an API Server message, which
	// can quote object content.
	failureCode string

	// syncedOnce closes the first time a LIST completes, so startup can bound
	// how many resources are listing at the same time.
	syncedOnce sync.Once
	syncedCh   chan struct{}

	now func() time.Time
}

func newStore(kind resourceKind, now func() time.Time) *store {
	if now == nil {
		now = time.Now
	}
	return &store{
		kind:     kind,
		items:    make(map[objectKey]entry),
		totals:   newSectionTotals(),
		syncedCh: make(chan struct{}),
		now:      now,
	}
}

func (target *store) Add(object any) error    { return target.upsert(object) }
func (target *store) Update(object any) error { return target.upsert(object) }

// Resync exists for the reflector's interface. Nothing here needs one: there is
// no downstream handler to re-deliver to, and a periodic full relist is exactly
// the load this cache exists to remove.
func (target *store) Resync() error { return nil }

func (target *store) upsert(object any) error {
	projected, err := target.project(object)
	if err != nil {
		return err
	}
	now := target.now()
	target.mutex.Lock()
	defer target.mutex.Unlock()
	if previous, exists := target.items[projected.key]; exists {
		// Subtract what this object used to contribute before adding what it
		// contributes now. Anything else drifts, permanently: the counters are
		// never recomputed from the objects.
		target.totals.apply(previous.contribution, -1)
	}
	target.items[projected.key] = projected
	target.totals.apply(projected.contribution, 1)
	target.observeLocked(projected.summary, now)
	return nil
}

func (target *store) Delete(object any) error {
	key, ok := deletedKey(object)
	if !ok {
		projected, err := target.project(object)
		if err != nil {
			return err
		}
		key = projected.key
	}
	now := target.now()
	target.mutex.Lock()
	defer target.mutex.Unlock()
	existing, exists := target.items[key]
	if !exists {
		return nil
	}
	target.totals.apply(existing.contribution, -1)
	delete(target.items, key)
	target.generation++
	target.lastEventAt = now
	return nil
}

// Replace installs a complete listing.
//
// It builds the new contents beside the old ones and swaps them in under one
// lock, so a reader never sees a half-synchronised section — which, during the
// re-list that follows an expired ResourceVersion, would look exactly like a
// Cluster that had just lost most of its objects.
func (target *store) Replace(objects []any, resourceVersion string) error {
	items := make(map[objectKey]entry, len(objects))
	totals := newSectionTotals()
	for _, object := range objects {
		projected, err := target.project(object)
		if err != nil {
			return err
		}
		if previous, duplicate := items[projected.key]; duplicate {
			totals.apply(previous.contribution, -1)
		}
		items[projected.key] = projected
		totals.apply(projected.contribution, 1)
	}
	now := target.now()
	target.mutex.Lock()
	target.items = items
	target.totals = totals
	target.generation++
	target.resourceVersion = resourceVersion
	target.synced = true
	target.lastSuccessAt = now
	target.lastEventAt = now
	target.failureCode = ""
	target.mutex.Unlock()

	target.syncedOnce.Do(func() { close(target.syncedCh) })
	return nil
}

func (target *store) observeLocked(summary any, now time.Time) {
	target.generation++
	target.lastEventAt = now
	target.lastSuccessAt = now
	if version := summaryResourceVersion(summary); version != "" {
		target.resourceVersion = version
	}
}

func (target *store) project(object any) (entry, error) {
	structured, ok := object.(*unstructured.Unstructured)
	if !ok {
		return entry{}, fmt.Errorf(
			"%w: %s received an object that is not unstructured",
			errUnexpectedObject,
			target.kind.gvr.Resource,
		)
	}
	return target.kind.project(structured)
}

var errUnexpectedObject = errors.New("unexpected cached object")

// deletedKey recovers identity from a tombstone.
//
// A watch that falls behind delivers the fact that an object is gone without
// the object itself. Dropping the event would leave the object counted forever;
// the tombstone carries the key it was stored under, which is all a removal
// needs.
func deletedKey(object any) (objectKey, bool) {
	tombstone, ok := object.(cache.DeletedFinalStateUnknown)
	if !ok {
		return objectKey{}, false
	}
	if structured, isObject := tombstone.Obj.(*unstructured.Unstructured); isObject {
		return objectKey{
			namespace: structured.GetNamespace(),
			name:      structured.GetName(),
		}, true
	}
	namespace, name, err := cache.SplitMetaNamespaceKey(tombstone.Key)
	if err != nil {
		return objectKey{}, false
	}
	return objectKey{namespace: namespace, name: name}, true
}

// recordListFailure records that a listing or watch attempt failed, without
// disturbing what the cache already holds: the last good contents keep
// answering, marked by the section health rather than replaced by zeroes.
func (target *store) recordFailure(code string) {
	target.mutex.Lock()
	defer target.mutex.Unlock()
	target.failureCode = code
}

func (target *store) recordRestart() {
	target.mutex.Lock()
	defer target.mutex.Unlock()
	target.watchRestarts++
}

func (target *store) waitForSync() <-chan struct{} { return target.syncedCh }

// snapshotTotals copies the running aggregate.
//
// The copy is what keeps an overview read cheap: it holds the section lock for
// the length of a struct copy and two small maps, never for a walk of every
// object.
func (target *store) snapshotTotals() (sectionTotals, sectionHealth) {
	target.mutex.RLock()
	defer target.mutex.RUnlock()
	return target.totals.clone(), sectionHealth{
		Synced:          target.synced,
		ResourceVersion: target.resourceVersion,
		LastSuccessAt:   target.lastSuccessAt,
		LastEventAt:     target.lastEventAt,
		WatchRestarts:   target.watchRestarts,
		Observed:        int64(len(target.items)),
		FailureCode:     target.failureCode,
	}
}

// sectionHealth is what the cache can honestly say about one resource.
type sectionHealth struct {
	Synced          bool
	ResourceVersion string
	LastSuccessAt   time.Time
	LastEventAt     time.Time
	WatchRestarts   int64
	Observed        int64
	FailureCode     string
}
