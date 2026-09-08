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
