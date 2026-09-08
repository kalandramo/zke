package resourcecache

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ExtendedResources are the rest of what the container service's built-in lists
// read: the storage, service, routing, autoscaling and policy pages.
//
// They are kept apart from the core ten for two reasons. None of them feeds the
// Cluster overview, so none of them has a section to contribute to; and every
// one of them is optional, because a Cluster may legitimately not have the API
// group — a Cluster with no Ingress controller has no Ingresses, and that is not
// a fault to report.
//
// They are cached as stripped objects rather than named summaries because their
// list contracts publish the object's own spec. See projectStripped.
//
// Three resources the design names are deliberately absent, and the reason is
// the same for all three: they are the ones a Cluster holds in the thousands.
// EndpointSlices change on every Pod readiness transition, and ReplicaSets and
// ControllerRevisions accumulate with every rollout that keeps a revision
// history. Following them would make the cache's memory and event rate a
// function of churn rather than of what the pages show, which is the property
// this cache exists to have. Their lists keep reading Kubernetes directly.
func ExtendedResources() []resourceKind {
	stripped := func(
		gvr schema.GroupVersionResource,
		namespaced bool,
	) resourceKind {
		kind := resourceKind{
			gvr:        gvr,
			namespaced: namespaced,
			optional:   true,
		}
		kind.project = projectStripped(kind, nil)
		return kind
	}
	return []resourceKind{
		stripped(schema.GroupVersionResource{
			Version: "v1", Resource: "services",
		}, true),
		stripped(schema.GroupVersionResource{
			Group: "networking.k8s.io", Version: "v1", Resource: "ingresses",
		}, true),
		stripped(schema.GroupVersionResource{
			Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses",
		}, false),
		stripped(schema.GroupVersionResource{
			Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers",
		}, true),
		stripped(schema.GroupVersionResource{
			Group: "policy", Version: "v1", Resource: "poddisruptionbudgets",
		}, true),
		stripped(schema.GroupVersionResource{
			Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies",
		}, true),
		stripped(schema.GroupVersionResource{
			Version: "v1", Resource: "resourcequotas",
		}, true),
		stripped(schema.GroupVersionResource{
			Version: "v1", Resource: "limitranges",
		}, true),
		stripped(schema.GroupVersionResource{
			Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses",
		}, false),
	}
}

// CachedResources is everything one Agent follows.
func CachedResources() []resourceKind {
	return append(CoreResources(), ExtendedResources()...)
}
