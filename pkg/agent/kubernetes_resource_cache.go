package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/agent/resourcecache"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// newKubernetesResourceCacheHandler answers queries from the Agent's own read
// model, without touching the Kubernetes API Server.
//
// It answers reads only. Nothing here creates, changes or deletes anything, and
// nothing here decides who may see what: the Server has already checked Tenant,
// Project, Cluster and RBAC before the request was sent, and a second, weaker
// check here would only be a way around the first.
func newKubernetesResourceCacheHandler(
	cache *resourcecache.Cache,
	maxBodyBytes uint64,
	compressionThreshold uint64,
) agentprotocol.ResourceCacheHandler {
	if maxBodyBytes == 0 {
		maxBodyBytes = agentprotocol.DefaultMaxResourceCacheBodySize
	}
	if compressionThreshold == 0 {
		compressionThreshold = agentprotocol.DefaultResourceCompressionThreshold
	}
	return func(
		_ context.Context,
		request *agentv1.ResourceCacheRequest,
	) (*agentv1.ResourceCacheResponse, io.Reader, error) {
		if cache == nil {
			return resourceCacheErrorResponse(
				agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
				"ResourceCacheUnavailable",
				"Agent resource cache is not running",
			), nil, nil
		}
		var payload any
		switch request.GetQuery() {
		case agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT:
			payload = cache.OverviewSnapshot()
		case agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_SUMMARY_LIST:
			page, err := cache.Summaries(resourcecache.Query{
				Resource: schema.GroupVersionResource{
					Group:    request.GetResource().GetGroup(),
					Version:  request.GetResource().GetVersion(),
					Resource: request.GetResource().GetResource(),
				},
				Namespace:     request.GetNamespace(),
				LabelSelector: request.GetLabelSelector(),
				FieldSelector: request.GetFieldSelector(),
				Limit:         int(request.GetLimit()),
				PageToken:     request.GetPageToken(),
			})
			if err != nil {
				return summaryQueryRefusal(err), nil, nil
			}
			payload = page
		default:
			return resourceCacheErrorResponse(
				agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				"UnsupportedResourceCacheQuery",
				"requested Agent resource cache query is unsupported",
			), nil, nil
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, errors.New("marshal Agent resource cache response")
		}
		if uint64(len(body)) > maxBodyBytes {
			return resourceCacheErrorResponse(
				agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
				"ResponseTooLarge",
				"Agent resource cache response exceeds the configured limit",
			), nil, nil
		}
		encoded := agentprotocol.EncodeBody(
			request.GetAcceptedEncodings(),
			body,
			compressionThreshold,
		)
		return &agentv1.ResourceCacheResponse{
			Result:          agentv1.ResultCode_RESULT_CODE_OK,
			ContentType:     kubernetesJSONContentType,
			BodySize:        uint64(len(encoded.Wire)),
			ContentEncoding: encoded.Encoding,
			DecodedBodySize: encoded.DecodedSize,
		}, bytes.NewReader(encoded.Wire), nil
	}
}

// summaryQueryRefusal names why the cache did not answer, in terms the Server
// can act on: two of these mean read Kubernetes instead, one means the page is
// gone, and one means wait.
func summaryQueryRefusal(err error) *agentv1.ResourceCacheResponse {
	switch {
	case errors.Is(err, resourcecache.ErrResourceNotCached):
		return resourceCacheErrorResponse(
			agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			"ResourceNotCached",
			"Agent does not cache this resource",
		)
	case errors.Is(err, resourcecache.ErrQueryUnsupported):
		return resourceCacheErrorResponse(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			"QueryNotCacheable",
			"Agent resource cache cannot answer this query exactly",
		)
	case errors.Is(err, resourcecache.ErrPageToken):
		return resourceCacheErrorResponse(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			"PageTokenExpired",
			"Agent resource cache page is no longer available",
		)
	case errors.Is(err, resourcecache.ErrNotSynced):
		return resourceCacheErrorResponse(
			agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			"ResourceCacheWarmingUp",
			"Agent resource cache has not finished its first listing",
		)
	default:
		return resourceCacheErrorResponse(
			agentv1.ResultCode_RESULT_CODE_INTERNAL,
			"ResourceCacheFailed",
			"Agent resource cache query failed",
		)
	}
}

func resourceCacheErrorResponse(
	result agentv1.ResultCode,
	reason string,
	message string,
) *agentv1.ResourceCacheResponse {
	return &agentv1.ResourceCacheResponse{
		Result:  result,
		Reason:  reason,
		Message: message,
	}
}
