package resourcecache

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// projectStripped keeps a Kubernetes object with the parts no list shows
// removed, rather than reducing it to a named summary.
//
// It exists for the resources whose list contract *is* their spec — a Service,
// an Ingress, a NetworkPolicy: what the page shows is the object, so a named
// summary of one would be a second copy of the same fields with a second set of
// names to keep in step. Serving the stripped object instead lets the Server run
// the projection it already has, so those contracts cannot drift.
//
// It is used only for resources a Cluster holds tens or hundreds of, never for
// the ones it holds tens of thousands of, and never for anything whose body is
// sensitive: Secrets and ConfigMaps are not cached at all, and what is removed
// below is exactly where large opaque content collects.
func projectStripped() func(*unstructured.Unstructured) (entry, error) {
	return func(object *unstructured.Unstructured) (entry, error) {
		if object.GetName() == "" {
			return entry{}, fmt.Errorf(
				"%w: cached object has no name",
				errUnexpectedObject,
			)
		}
		// No contribution: none of the resources projected this way feeds a
		// section of the Cluster overview.
		return entry{
			key: objectKey{
				namespace: object.GetNamespace(),
				name:      object.GetName(),
			},
			uid:       string(object.GetUID()),
			labels:    object.GetLabels(),
			createdAt: object.GetCreationTimestamp().Time,
			summary:   strip(object),
		}, nil
	}
}

// strip removes what a list never shows and what would otherwise make a cached
// object unbounded.
//
// managedFields is the field-management history, which is often larger than the
// object it describes. Annotations are where whole rendered configurations get
// stored — a last-applied-configuration, an ingress controller snippet — and no
// list summary reads them; the detail request, which goes to Kubernetes, still
// returns them in full.
func strip(object *unstructured.Unstructured) map[string]any {
	copied := object.DeepCopy()
	metadata, found, err := unstructured.NestedMap(copied.Object, "metadata")
	if err != nil || !found {
		return copied.Object
	}
	delete(metadata, "managedFields")
	delete(metadata, "annotations")
	copied.Object["metadata"] = metadata
	return copied.Object
}

// strippedResourceVersion reads the identity a stripped object carries, so the
// section's ResourceVersion advances with the objects it holds.
func strippedResourceVersion(summary any) (string, bool) {
	object, ok := summary.(map[string]any)
	if !ok {
		return "", false
	}
	version, found, err := unstructured.NestedString(
		object,
		"metadata",
		"resourceVersion",
	)
	if err != nil || !found {
		return "", false
	}
	return version, true
}
