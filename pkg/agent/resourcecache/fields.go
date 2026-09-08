package resourcecache

import (
	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fieldMatcher decides whether one cached object satisfies a field selector.
type fieldMatcher func(entry) bool

// parseFieldSelector accepts only the fields the cache can answer exactly.
//
// A selector naming anything else is refused rather than approximated: the
// caller then reads Kubernetes, which is the only place that can answer it, and
// nobody is shown a list that is quietly missing rows.
func parseFieldSelector(
	gvr schema.GroupVersionResource,
	selector string,
) (fieldMatcher, error) {
	if selector == "" {
		return nil, nil
	}
	parsed, err := fields.ParseSelector(selector)
	if err != nil {
		return nil, ErrQueryUnsupported
	}
	requirements := parsed.Requirements()
	if len(requirements) == 0 {
		return nil, nil
	}
	readers := make([]func(entry) (string, bool), 0, len(requirements))
	for _, requirement := range requirements {
		// Only equality and inequality. Kubernetes field selectors have no
		// other operators, and a selector that somehow carried one would be
		// something this cache had not been asked about before.
		if requirement.Operator != "=" && requirement.Operator != "==" &&
			requirement.Operator != "!=" {
			return nil, ErrQueryUnsupported
		}
		reader, supported := fieldReader(gvr, requirement.Field)
		if !supported {
			return nil, ErrQueryUnsupported
		}
		readers = append(readers, reader)
	}
	return func(item entry) bool {
		for index, requirement := range requirements {
			value, present := readers[index](item)
			if !present {
				return false
			}
			equal := value == requirement.Value
			if requirement.Operator == "!=" {
				equal = !equal
			}
			if !equal {
				return false
			}
		}
		return true
	}, nil
}

func fieldReader(
	gvr schema.GroupVersionResource,
	field string,
) (func(entry) (string, bool), bool) {
	switch field {
	case "metadata.name":
		return func(item entry) (string, bool) { return item.key.name, true }, true
	case "metadata.namespace":
		return func(item entry) (string, bool) { return item.key.namespace, true }, true
	}
	switch gvr {
	case schema.GroupVersionResource{Version: "v1", Resource: "pods"}:
		switch field {
		case "spec.nodeName":
			return func(item entry) (string, bool) {
				pod, ok := item.summary.(resourcesummary.Pod)
				return pod.NodeName, ok
			}, true
		case "status.phase":
			return func(item entry) (string, bool) {
				pod, ok := item.summary.(resourcesummary.Pod)
				return pod.Phase, ok
			}, true
		}
	case schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}:
		if field == "status.phase" {
			return func(item entry) (string, bool) {
				namespace, ok := item.summary.(resourcesummary.Namespace)
				// The stored phase, not the overview's reading of it: a field
				// selector is written against what Kubernetes holds.
				return namespace.Phase, ok
			}, true
		}
	}
	return nil, false
}
