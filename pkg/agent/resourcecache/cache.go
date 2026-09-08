package resourcecache

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
)

const (
	// One resource is one LIST of a whole Cluster. Starting them all at once is
	// how a fleet of Agents reconnecting turns into a thundering herd against
	// the control planes they just reconnected to.
	defaultSyncConcurrency = 2
	// A random wait before the first LIST, so a Server restart that reconnects
	// every Agent at once does not put every Cluster's control plane under the
	// same spike at the same instant.
	defaultStartupJitter = 5 * time.Second
	// Watches are re-established from the last ResourceVersion; this only
	// bounds how long one watch request may hang before it is renewed.
	defaultMinWatchTimeout = 10 * time.Minute
	// Pages for the initial LIST. Reading a large Cluster in pages keeps one
	// response from having to be built whole on the API Server.
	defaultListPageSize   = 500
	defaultRetryInterval  = 2 * time.Second
	defaultMaxRetryWait   = 30 * time.Second
	defaultRelistCooldown = time.Second
)

// ErrNotSynced is a query made before the cache has anything true to say.
//
// It is returned rather than an empty result on purpose: an unsynchronised
// section knows nothing, and reporting nothing as zero would put a confident
// wrong number in front of an operator.
var ErrNotSynced = errors.New("resource cache has not synchronized")

// ErrResourceNotCached is a query for a resource this cache does not follow.
// The caller falls back to reading Kubernetes directly.
var ErrResourceNotCached = errors.New("resource is not cached")

type Config struct {
	// SyncConcurrency bounds how many resources may be doing their initial LIST
	// at the same time.
	SyncConcurrency int
	// StartupJitter is the largest random delay before the first LIST.
	StartupJitter   time.Duration
	MinWatchTimeout time.Duration
	ListPageSize    int64
	RetryInterval   time.Duration
	MaxRetryWait    time.Duration
	// Resources overrides what is cached. Empty means the core resources.
	Resources []resourceKind
}

// Cache is the Agent's single read model for Kubernetes resources.
//
// One GVR has exactly one watch in one Agent process, and every consumer —
// the Cluster overview, the Pod list, the Node list, the workload lists —
// reads that one copy. Nothing here decides who may see what: the Server
// checks Tenant, Project, Cluster and RBAC before a query ever arrives, and a
// cache that answered that question too would be a second, weaker place for it
// to be answered.
type Cache struct {
	client    dynamic.Interface
	discovery discovery.DiscoveryInterface
	config    Config
	logger    *slog.Logger
	now       func() time.Time

	// stores is written once, before the watches start, and only read after.
	stores map[schema.GroupVersionResource]*store
	order  []schema.GroupVersionResource

	mutex   sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	// running tracks every goroutine this cache owns, so Close can promise that
	// none of them can still be watching Kubernetes or writing to a store.
	running sync.WaitGroup
	// absent records resources Discovery says this Cluster does not have. They
	// are reported as unavailable sections rather than failing the cache.
	absent map[schema.GroupVersionResource]struct{}
	// paging holds the sorted keys of queries that have more than one page.
	paging *pagingSessions
}

func New(
	client dynamic.Interface,
	discoveryClient discovery.DiscoveryInterface,
	config Config,
	logger *slog.Logger,
) *Cache {
	if config.SyncConcurrency <= 0 {
		config.SyncConcurrency = defaultSyncConcurrency
	}
	if config.StartupJitter < 0 {
		config.StartupJitter = 0
	}
	if config.StartupJitter == 0 {
		config.StartupJitter = defaultStartupJitter
	}
	if config.MinWatchTimeout <= 0 {
		config.MinWatchTimeout = defaultMinWatchTimeout
	}
	if config.ListPageSize <= 0 {
		config.ListPageSize = defaultListPageSize
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = defaultRetryInterval
	}
	if config.MaxRetryWait < config.RetryInterval {
		config.MaxRetryWait = max(defaultMaxRetryWait, config.RetryInterval)
	}
	kinds := config.Resources
	if len(kinds) == 0 {
		kinds = CoreResources()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	instance := &Cache{
		client:    client,
		discovery: discoveryClient,
		config:    config,
		logger:    logger,
		now:       time.Now,
		stores:    make(map[schema.GroupVersionResource]*store, len(kinds)),
		order:     make([]schema.GroupVersionResource, 0, len(kinds)),
		absent:    make(map[schema.GroupVersionResource]struct{}),
	}
	instance.paging = newPagingSessions(instance.now)
	for _, kind := range kinds {
		// One GVR, one store, one watch. A second registration of the same
		// resource is a duplicate watch on the API Server and a second copy of
		// the same objects in this process.
		if _, exists := instance.stores[kind.gvr]; exists {
			continue
		}
		instance.stores[kind.gvr] = newStore(kind, instance.now)
		instance.order = append(instance.order, kind.gvr)
	}
	return instance
}

// Start begins filling the cache and returns immediately.
//
// It never blocks its caller. The Agent's connection, heartbeat, terminals and
// writes must not wait behind ten Cluster-wide listings, so the listing happens
// entirely on goroutines this cache owns and Close waits for.
func (instance *Cache) Start(ctx context.Context) {
	if instance == nil || instance.client == nil {
		return
	}
	instance.mutex.Lock()
	if instance.started || instance.stopped {
		instance.mutex.Unlock()
		return
	}
	instance.started = true
	runContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	instance.cancel = cancel
	instance.mutex.Unlock()

	instance.running.Add(1)
	go func() {
		defer instance.running.Done()
		instance.run(runContext, ctx)
	}()
}

func (instance *Cache) run(runContext context.Context, parent context.Context) {
	// Stopping with the caller's Context as well as with Close, so a cache that
	// is never closed still ends when the Agent does.
	instance.running.Add(1)
	go func() {
		defer instance.running.Done()
		select {
		case <-parent.Done():
			instance.stop()
		case <-runContext.Done():
		}
	}()

	if !sleepContext(runContext, jitter(instance.config.StartupJitter)) {
		return
	}
	instance.resolveAvailability(runContext)

	// A counting semaphore rather than a worker pool: each resource holds a
	// slot only until its first LIST lands, then keeps watching without one.
	slots := make(chan struct{}, instance.config.SyncConcurrency)
	for _, gvr := range instance.order {
		target := instance.stores[gvr]
		if instance.unavailable(gvr) {
			continue
		}
		instance.running.Add(1)
		go func() {
			defer instance.running.Done()
			instance.follow(runContext, target, slots)
		}()
	}
}

// resolveAvailability asks the Cluster which of these resources it actually
// has. An optional API that is not installed leaves its section unavailable and
// nothing else: a Cluster without the batch group is still a Cluster whose
// Nodes and Pods are worth caching.
func (instance *Cache) resolveAvailability(ctx context.Context) {
	if instance.discovery == nil {
		return
	}
	byGroupVersion := make(map[string][]schema.GroupVersionResource)
	for _, gvr := range instance.order {
		byGroupVersion[gvr.GroupVersion().String()] = append(
			byGroupVersion[gvr.GroupVersion().String()],
			gvr,
		)
	}
	for groupVersion, resources := range byGroupVersion {
		if ctx.Err() != nil {
			return
		}
		list, err := instance.discovery.ServerResourcesForGroupVersion(groupVersion)
		if apierrors.IsNotFound(err) {
			for _, gvr := range resources {
				instance.markAbsent(gvr, "api_group_absent")
			}
			continue
		}
		if err != nil {
			// Discovery itself failed. The resources are attempted anyway: a
			// transient discovery failure must not silently turn off a cache
			// the Cluster is perfectly able to serve.
			instance.logger.Debug(
				"Agent resource cache could not discover a group version",
				slog.String("group_version", groupVersion),
				slog.String("error", err.Error()),
			)
			continue
		}
		present := make(map[string]struct{}, len(list.APIResources))
		for _, apiResource := range list.APIResources {
			present[apiResource.Name] = struct{}{}
		}
		for _, gvr := range resources {
			if _, found := present[gvr.Resource]; !found {
				instance.markAbsent(gvr, "resource_absent")
			}
		}
	}
}

func (instance *Cache) markAbsent(gvr schema.GroupVersionResource, code string) {
	instance.mutex.Lock()
	instance.absent[gvr] = struct{}{}
	instance.mutex.Unlock()
	instance.stores[gvr].recordFailure(code)
	instance.logger.Info(
		"Agent resource cache skipped a resource this Cluster does not have",
		slog.String("resource", gvr.String()),
		slog.String("reason", code),
	)
}

func (instance *Cache) unavailable(gvr schema.GroupVersionResource) bool {
	instance.mutex.Lock()
	defer instance.mutex.Unlock()
	_, absent := instance.absent[gvr]
	return absent
}

// follow runs one resource's LIST and WATCH for the life of the cache.
//
// The reflector reconnects its own watch from the last ResourceVersion, so an
// interruption costs one watch request rather than a new listing. It returns
// here only when the watch cannot be resumed — an expired ResourceVersion, a
// revoked permission — and that is where a fresh LIST is allowed.
func (instance *Cache) follow(ctx context.Context, target *store, slots chan struct{}) {
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	released := false
	release := func() {
		if !released {
			released = true
			<-slots
		}
	}
	defer release()
	instance.running.Add(1)
	go func() {
		defer instance.running.Done()
		select {
		case <-target.waitForSync():
		case <-ctx.Done():
		}
		release()
	}()

	reflector := cache.NewReflectorWithOptions(
		instance.listerWatcher(target.kind),
		&unstructured.Unstructured{},
		target,
		cache.ReflectorOptions{
			Name: "zke-resource-cache/" + target.kind.gvr.String(),
			// No periodic resync. A full relist on a timer is precisely the
			// repeated Cluster-wide read this cache exists to remove; the watch
			// keeps the contents current between the necessary relists.
			ResyncPeriod:    0,
			MinWatchTimeout: instance.config.MinWatchTimeout,
		},
	)
	reflector.WatchListPageSize = instance.config.ListPageSize

	wait := instance.config.RetryInterval
	for ctx.Err() == nil {
		err := reflector.ListAndWatchWithContext(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			wait = instance.config.RetryInterval
			// A clean return still means the watch has to be re-established;
			// a short pause keeps that from becoming a tight loop.
			if !sleepContext(ctx, defaultRelistCooldown) {
				return
			}
			target.recordRestart()
			continue
		}
		code := failureCode(err)
		target.recordFailure(code)
		target.recordRestart()
		instance.logger.Debug(
			"Agent resource cache watch restarted",
			slog.String("resource", target.kind.gvr.String()),
			slog.String("reason", code),
		)
		if !sleepContext(ctx, jitter(wait)) {
			return
		}
		wait = min(wait*2, instance.config.MaxRetryWait)
	}
}

// listerWatcher is a first LIST followed by a WATCH, and deliberately not
// client-go's streaming watch-list.
//
// Watch-list is the newer way to fill a cache, but it depends on the API Server
// answering with a bookmark, and an Agent connects to whatever Kubernetes the
// Cluster runs. Where that bookmark does not arrive the reflector waits out a
// ten-second timeout per resource before falling back — a startup penalty paid
// on exactly the older or smaller control planes this cache is meant to protect.
// Saying plainly that this ListerWatcher does not offer watch-list semantics
// gives every Cluster the same predictable path.
type listerWatcher struct {
	*cache.ListWatch
}

func (listerWatcher) IsWatchListSemanticsUnSupported() bool { return true }

func (instance *Cache) listerWatcher(kind resourceKind) cache.ListerWatcher {
	resourceClient := func() dynamic.ResourceInterface {
		return instance.client.Resource(kind.gvr)
	}
	return listerWatcher{ListWatch: &cache.ListWatch{
		ListWithContextFunc: func(
			ctx context.Context,
			options metav1.ListOptions,
		) (runtime.Object, error) {
			return resourceClient().List(ctx, options)
		},
		WatchFuncWithContext: func(
			ctx context.Context,
			options metav1.ListOptions,
		) (watch.Interface, error) {
			return resourceClient().Watch(ctx, options)
		},
	}}
}

// failureCode reduces a Kubernetes failure to something safe to record and to
// report. The API Server's own message can quote object content, so it is
// logged nowhere and returned nowhere.
func failureCode(err error) string {
	switch {
	case apierrors.IsResourceExpired(err), apierrors.IsGone(err):
		return "resource_version_expired"
	case apierrors.IsForbidden(err):
		return "cluster_api_forbidden"
	case apierrors.IsUnauthorized(err):
		return "cluster_api_unauthenticated"
	case apierrors.IsNotFound(err):
		return "resource_absent"
	case apierrors.IsTooManyRequests(err):
		return "cluster_api_throttled"
	case apierrors.IsTimeout(err), errors.Is(err, context.DeadlineExceeded):
		return "cluster_api_timeout"
	case apierrors.IsServiceUnavailable(err), apierrors.IsInternalError(err):
		return "cluster_api_unavailable"
	default:
		return "cluster_api_error"
	}
}

// Close stops every watch and releases every store.
//
// It waits: after it returns, no goroutine this cache started can still be
// reading Kubernetes or writing into a store, which is what makes an Agent
// shutdown final rather than merely quiet.
func (instance *Cache) Close() {
	if instance == nil {
		return
	}
	instance.stop()
	instance.running.Wait()
}

func (instance *Cache) stop() {
	instance.mutex.Lock()
	cancel := instance.cancel
	instance.cancel = nil
	instance.stopped = true
	instance.mutex.Unlock()
	if cancel != nil {
		cancel()
	}
}

// WaitForSync blocks until every available resource has completed its first
// LIST, or until ctx ends. It exists for tests and for a deliberate readiness
// check; nothing in the request path waits on it.
func (instance *Cache) WaitForSync(ctx context.Context) bool {
	for _, gvr := range instance.order {
		if instance.unavailable(gvr) {
			continue
		}
		select {
		case <-instance.stores[gvr].waitForSync():
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func jitter(window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(window)))
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
