package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
)

const summaryClusterID = "00000000-0000-4000-8000-0000000000a1"

// cacheRequester is an Agent that keeps a resource cache. It answers the cache
// Stream from a fixed page and counts how often the Kubernetes listing path was
// reached instead.
type cacheRequester struct {
	page     resourcesummary.Page
	refusal  *agentv1.ResourceCacheResponse
	err      error
	requests atomic.Int64
	listings atomic.Int64
	lastCall atomic.Pointer[agentv1.ResourceCacheRequest]
	// listBody is what the Kubernetes listing path receives when the cache
	// declines.
	listBody []byte
}

func (requester *cacheRequester) RequestResource(
	_ context.Context,
	_ string,
	_ *agentv1.ResourceRequest,
	_ io.Reader,
	responseBody io.Writer,
) (*agentv1.ResourceResponse, error) {
	requester.listings.Add(1)
	body := requester.listBody
	if body == nil {
		body = []byte(`{"apiVersion":"v1","kind":"PodList","items":[]}`)
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

func (requester *cacheRequester) RequestResourceSummaries(
	_ context.Context,
	_ string,
	request *agentv1.ResourceCacheRequest,
	responseBody io.Writer,
) (*agentv1.ResourceCacheResponse, error) {
	requester.requests.Add(1)
	requester.lastCall.Store(request)
	if requester.err != nil {
		return nil, requester.err
	}
	if requester.refusal != nil {
		return requester.refusal, nil
	}
	body, err := json.Marshal(requester.page)
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

func cachedPodPage(t *testing.T, nextToken string) resourcesummary.Page {
	t.Helper()
	pods := []resourcesummary.Pod{
		{
			ObjectSummary: resourcesummary.ObjectSummary{
				UID: "uid-web-0", Namespace: "default", Name: "web-0",
				ResourceVersion:   "100",
				Labels:            map[string]string{"app": "web"},
				CreationTimestamp: time.Unix(1700000000, 0).UTC(),
			},
			NodeName: "node-a", PodIP: "10.0.0.1", Phase: "Running",
			Ready: true, RestartCount: 2,
			Images: []string{"registry.example.test/web:1"},
			Controller: &resourcesummary.OwnerReference{
				APIVersion: "apps/v1", Kind: "ReplicaSet",
				Name: "web-abc", UID: "uid-rs", Controller: true,
			},
			CPURequestsMillis: 500, MemoryRequestsBytes: 134217728,
		},
		{
			ObjectSummary: resourcesummary.ObjectSummary{
				UID: "uid-web-1", Namespace: "default", Name: "web-1",
				ResourceVersion:   "101",
				CreationTimestamp: time.Unix(1700000001, 0).UTC(),
			},
			NodeName: "node-b", Phase: "Pending",
		},
	}
	items := make([]json.RawMessage, 0, len(pods))
	for _, pod := range pods {
		raw, err := json.Marshal(pod)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, raw)
	}
	page := resourcesummary.Page{
		Items:           items,
		ResourceVersion: "900",
		Snapshot:        "17",
		NextPageToken:   nextToken,
	}
	if nextToken != "" {
		page.Remaining = 5
	}
	return page
}

// A Cluster whose Agent keeps a cache answers the Pod list from it, and the
// rows carry everything the list contract publishes.
func TestListPodsUsesTheAgentCache(t *testing.T) {
	t.Parallel()
	requester := &cacheRequester{page: cachedPodPage(t, "agent-token-1")}
	service := NewService(requester)

	page, err := service.ListPods(context.Background(), ListPodsInput{
		ClusterID: summaryClusterID,
		Namespace: "default",
		Limit:     50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requester.listings.Load() != 0 {
		t.Fatal("the Cluster was listed despite the Agent cache")
	}
	if len(page.Pods) != 2 {
		t.Fatalf("%d Pods returned", len(page.Pods))
	}
	first := page.Pods[0]
	if first.APIVersion != "v1" || first.Kind != "Pod" ||
		first.Namespace != "default" || first.Name != "web-0" ||
		first.UID != "uid-web-0" || first.ResourceVersion != "100" ||
		first.Phase != "Running" || !first.Ready ||
		first.NodeName != "node-a" || first.PodIP != "10.0.0.1" ||
		first.RestartCount != 2 || len(first.Images) != 1 {
		t.Fatalf("cached Pod summary = %+v", first)
	}
	if first.Controller == nil || first.Controller.Kind != "ReplicaSet" ||
		!first.Controller.Controller {
		t.Fatalf("cached Pod controller = %+v", first.Controller)
	}
	// The contract declares these as an object and an array; a nil one would
	// marshal to null.
	if page.Pods[1].Labels == nil || page.Pods[1].Images == nil {
		t.Fatalf("empty collections were left nil: %+v", page.Pods[1])
	}
	if !strings.HasPrefix(page.ContinueToken, "zc1.") {
		t.Fatalf("continuation token = %q, want one marked as cached", page.ContinueToken)
	}
	if page.RemainingItemCount == nil || *page.RemainingItemCount != 5 {
		t.Fatalf("remaining item count = %v", page.RemainingItemCount)
	}
	if page.ResourceVersion != "900" {
		t.Fatalf("resource version = %q", page.ResourceVersion)
	}
}

// A continuation goes back to the source that issued it. A cached page carries
// its own marker; a Kubernetes continue token does not, and must not be sent to
// the cache.
func TestContinuationsStayWithTheirSource(t *testing.T) {
	t.Parallel()
	requester := &cacheRequester{page: cachedPodPage(t, "agent-token-2")}
	service := NewService(requester)

	first, err := service.ListPods(context.Background(), ListPodsInput{
		ClusterID: summaryClusterID, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListPods(context.Background(), ListPodsInput{
		ClusterID:     summaryClusterID,
		Limit:         50,
		ContinueToken: first.ContinueToken,
	}); err != nil {
		t.Fatal(err)
	}
	sent := requester.lastCall.Load()
	if sent.GetPageToken() != "agent-token-2" {
		t.Fatalf("the Agent received page token %q", sent.GetPageToken())
	}
	if requester.listings.Load() != 0 {
		t.Fatal("a cached continuation reached the Kubernetes listing path")
	}

	// A Kubernetes continue token goes to Kubernetes, and the cache is not
	// asked at all.
	before := requester.requests.Load()
	if _, err := service.ListPods(context.Background(), ListPodsInput{
		ClusterID:     summaryClusterID,
		Limit:         50,
		ContinueToken: "eyJ2IjoibWV0YS5rOHMuaW8vdjEifQ",
	}); err != nil {
		t.Fatal(err)
	}
	if requester.requests.Load() != before {
		t.Fatal("a Kubernetes continuation was sent to the Agent cache")
	}
	if requester.listings.Load() != 1 {
		t.Fatalf("%d Kubernetes listings for one Kubernetes continuation",
			requester.listings.Load())
	}
}

// Every reason the cache declines means the same thing to the caller: read
// Kubernetes. None of them is an error the user sees.
func TestListsFallBackWhenTheCacheDeclines(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{
		"ResourceNotCached",
		"QueryNotCacheable",
		"ResourceCacheWarmingUp",
		"ResourceCacheUnavailable",
	} {
		t.Run(reason, func(t *testing.T) {
			requester := &cacheRequester{
				refusal: &agentv1.ResourceCacheResponse{
					Result: agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
					Reason: reason,
				},
			}
			service := NewService(requester)
			if _, err := service.ListPods(context.Background(), ListPodsInput{
				ClusterID: summaryClusterID, Limit: 50,
			}); err != nil {
				t.Fatalf("a declined cache produced an error: %v", err)
			}
			if requester.listings.Load() != 1 {
				t.Fatalf("%d Kubernetes listings after a declined cache",
					requester.listings.Load())
			}
		})
	}
}

// An expired page is not a page from somewhere else. It is refused, and the
// caller starts the listing again.
func TestExpiredCachePageIsRefused(t *testing.T) {
	t.Parallel()
	requester := &cacheRequester{
		refusal: &agentv1.ResourceCacheResponse{
			Result: agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			Reason: "PageTokenExpired",
		},
	}
	service := NewService(requester)
	_, err := service.ListPods(context.Background(), ListPodsInput{
		ClusterID:     summaryClusterID,
		Limit:         50,
		ContinueToken: "zc1.stale-token",
	})
	if !errors.Is(err, ErrCachePageExpired) {
		t.Fatalf("got %v", err)
	}
	if requester.listings.Load() != 0 {
		t.Fatal("an expired page silently returned a different page")
	}
}

// An Agent that predates the cache is listed the way it always was.
func TestListsFallBackForAnAgentWithoutTheCapability(t *testing.T) {
	t.Parallel()
	// A requester that implements only the Resource Stream stands in for one.
	requester := &legacyRequester{}
	service := NewService(requester)
	if _, err := service.ListPods(context.Background(), ListPodsInput{
		ClusterID: summaryClusterID, Limit: 50,
	}); err != nil {
		t.Fatal(err)
	}
	if requester.listings != 1 {
		t.Fatalf("%d Kubernetes listings against an Agent without a cache",
			requester.listings)
	}
}

type legacyRequester struct {
	listings int
}

func (requester *legacyRequester) RequestResource(
	_ context.Context,
	_ string,
	_ *agentv1.ResourceRequest,
	_ io.Reader,
	responseBody io.Writer,
) (*agentv1.ResourceResponse, error) {
	requester.listings++
	body := []byte(`{"apiVersion":"v1","kind":"PodList","items":[]}`)
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

// The cache path applies the same input rules the Kubernetes path applies. A
// request one would refuse must not be answered by the other, or a caller would
// get a different answer depending on which source happened to serve it.
func TestCachedListsRefuseWhatTheClusterPathRefuses(t *testing.T) {
	t.Parallel()
	requester := &cacheRequester{page: cachedPodPage(t, "")}
	service := NewService(requester)
	cases := []struct {
		name  string
		input ListPodsInput
	}{
		{
			name:  "no limit",
			input: ListPodsInput{ClusterID: summaryClusterID},
		},
		{
			name:  "limit past the ceiling",
			input: ListPodsInput{ClusterID: summaryClusterID, Limit: 501},
		},
		{
			name: "invalid Namespace",
			input: ListPodsInput{
				ClusterID: summaryClusterID, Namespace: "Not A Namespace", Limit: 50,
			},
		},
		{
			name: "malformed label selector",
			input: ListPodsInput{
				ClusterID: summaryClusterID, Limit: 50, LabelSelector: "app=@@@",
			},
		},
		{
			name:  "invalid Cluster",
			input: ListPodsInput{ClusterID: "not-a-uuid", Limit: 50},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := service.ListPods(context.Background(), testCase.input); !errors.Is(
				err,
				ErrInvalidInput,
			) {
				t.Fatalf("got %v, want ErrInvalidInput", err)
			}
		})
	}
	if requester.requests.Load() != 0 {
		t.Fatalf("%d invalid requests reached the Agent", requester.requests.Load())
	}
}
