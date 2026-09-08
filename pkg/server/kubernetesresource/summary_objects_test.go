package kubernetesresource

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
)

// objectRequester answers both paths from the same objects: the Kubernetes
// listing path with a full list, and the cache path with the same objects
// stripped the way the Agent strips them.
type objectRequester struct {
	objects  []map[string]any
	listKind string
	listings int
	cached   int
}

func (requester *objectRequester) RequestResource(
	_ context.Context,
	_ string,
	request *agentv1.ResourceRequest,
	_ io.Reader,
	responseBody io.Writer,
) (*agentv1.ResourceResponse, error) {
	requester.listings++
	items := make([]any, 0, len(requester.objects))
	for _, object := range requester.objects {
		items = append(items, object)
	}
	group := request.GetResource().GetGroup()
	apiVersion := request.GetResource().GetVersion()
	if group != "" {
		apiVersion = group + "/" + apiVersion
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": apiVersion,
		"kind":       requester.listKind,
		"metadata":   map[string]any{"resourceVersion": "1000"},
		"items":      items,
	})
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

func (requester *objectRequester) RequestResourceSummaries(
	_ context.Context,
	_ string,
	_ *agentv1.ResourceCacheRequest,
	responseBody io.Writer,
) (*agentv1.ResourceCacheResponse, error) {
	requester.cached++
	items := make([]json.RawMessage, 0, len(requester.objects))
	for _, object := range requester.objects {
		// What the Agent holds: the object without the parts no list shows.
		stripped := map[string]any{}
		for key, value := range object {
			stripped[key] = value
		}
		if metadata, ok := stripped["metadata"].(map[string]any); ok {
			copied := map[string]any{}
			for key, value := range metadata {
				if key == "managedFields" || key == "annotations" {
					continue
				}
				copied[key] = value
			}
			stripped["metadata"] = copied
		}
		raw, err := json.Marshal(stripped)
		if err != nil {
			return nil, err
		}
		items = append(items, raw)
	}
	body, err := json.Marshal(resourcesummary.Page{
		Items:           items,
		ResourceVersion: "1000",
		Snapshot:        "3",
	})
	if err != nil {
		return nil, err
	}
	if _, err := responseBody.Write(body); err != nil {
		return nil, err
	}
	return &agentv1.ResourceCacheResponse{
		Result:      agentv1.ResultCode_RESULT_CODE_OK,
		ContentType: "application/json",
		BodySize:    uint64(len(body)),
	}, nil
}

func serviceObject() map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"namespace":       "default",
			"name":            "frontend",
			"uid":             "uid-frontend",
			"resourceVersion": "300",
			"labels":          map[string]any{"app": "frontend"},
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": "{}",
			},
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
		},
		"spec": map[string]any{
			"type":       "ClusterIP",
			"clusterIP":  "10.96.0.10",
			"clusterIPs": []any{"10.96.0.10"},
			"selector":   map[string]any{"app": "frontend"},
			"ports": []any{map[string]any{
				"name": "http", "port": int64(80), "protocol": "TCP",
				"targetPort": int64(8080),
			}},
			"sessionAffinity": "None",
		},
		"status": map[string]any{"loadBalancer": map[string]any{}},
	}
}

func resourceQuotaObject() map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "ResourceQuota",
		"metadata": map[string]any{
			"namespace": "default", "name": "team-quota",
			"uid": "uid-quota", "resourceVersion": "310",
			"annotations":   map[string]any{"note": "keep"},
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
		},
		"spec": map[string]any{
			"hard":   map[string]any{"cpu": "10", "memory": "20Gi"},
			"scopes": []any{"NotTerminating"},
		},
		"status": map[string]any{
			"hard": map[string]any{"cpu": "10", "memory": "20Gi"},
			"used": map[string]any{"cpu": "3", "memory": "6Gi"},
		},
	}
}

// The two paths must produce the same rows. A list served from the Agent's
// cache and the same list served from Kubernetes go through the same
// projection, so the only way they could differ is if the cache had dropped
// something the projection reads.
func TestExtendedListsMatchBetweenCacheAndCluster(t *testing.T) {
	t.Parallel()

	t.Run("services", func(t *testing.T) {
		requester := &objectRequester{
			objects:  []map[string]any{serviceObject()},
			listKind: "ServiceList",
		}
		service := NewService(requester)
		fromCache, err := service.ListNetworkingResources(
			context.Background(),
			ListNetworkingResourcesInput{
				ClusterID: summaryClusterID,
				Resource:  NetworkingServices,
				Namespace: "default",
				Limit:     50,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if requester.cached != 1 || requester.listings != 0 {
			t.Fatalf("cache reads=%d listings=%d", requester.cached, requester.listings)
		}
		fromCluster, err := service.ListNetworkingResources(
			context.Background(),
			ListNetworkingResourcesInput{
				ClusterID:     summaryClusterID,
				Resource:      NetworkingServices,
				Namespace:     "default",
				Limit:         50,
				ContinueToken: "eyJ2IjoibWV0YS5rOHMuaW8vdjEifQ",
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if requester.listings != 1 {
			t.Fatalf("%d Kubernetes listings", requester.listings)
		}
		if len(fromCache.Resources) != 1 {
			t.Fatalf("%d rows from the cache", len(fromCache.Resources))
		}
		if !reflect.DeepEqual(fromCache.Resources, fromCluster.Resources) {
			t.Fatalf("rows differ:\n cache   %+v\n cluster %+v",
				fromCache.Resources, fromCluster.Resources)
		}
		if fromCache.Resources[0].Service == nil ||
			fromCache.Resources[0].Service.Spec.Type != "ClusterIP" ||
			len(fromCache.Resources[0].Service.Spec.Ports) != 1 {
			t.Fatalf("the cached Service lost its spec: %+v", fromCache.Resources[0])
		}
	})

	t.Run("resource quotas", func(t *testing.T) {
		requester := &objectRequester{
			objects:  []map[string]any{resourceQuotaObject()},
			listKind: "ResourceQuotaList",
		}
		service := NewService(requester)
		fromCache, err := service.ListPolicyResources(
			context.Background(),
			ListPolicyResourcesInput{
				ClusterID: summaryClusterID,
				Resource:  PolicyResourceQuotas,
				Namespace: "default",
				Limit:     50,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		fromCluster, err := service.ListPolicyResources(
			context.Background(),
			ListPolicyResourcesInput{
				ClusterID:     summaryClusterID,
				Resource:      PolicyResourceQuotas,
				Namespace:     "default",
				Limit:         50,
				ContinueToken: "eyJ2IjoibWV0YS5rOHMuaW8vdjEifQ",
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromCache.Resources, fromCluster.Resources) {
			t.Fatalf("rows differ:\n cache   %+v\n cluster %+v",
				fromCache.Resources, fromCluster.Resources)
		}
		if len(fromCache.Resources) != 1 ||
			fromCache.Resources[0].ResourceQuota == nil ||
			fromCache.Resources[0].ResourceQuota.Used["cpu"] != "3" {
			t.Fatalf("the cached quota lost its numbers: %+v", fromCache.Resources)
		}
	})
}
