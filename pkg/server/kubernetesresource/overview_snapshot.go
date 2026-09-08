package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

// OverviewSnapshotRequester is the Agent connection that can ask for the
// Cluster overview the Agent already holds.
//
// It is a separate interface from ResourceRequester because it is a separate
// capability: an Agent that predates the resource cache implements the one and
// not the other, and the caller has to be able to tell.
type OverviewSnapshotRequester interface {
	RequestOverviewSnapshot(
		ctx context.Context,
		clusterID string,
		responseBody io.Writer,
	) (*agentv1.ResourceCacheResponse, error)
}

// ClusterOverviewSnapshot reads the Cluster overview from the Agent's cache.
//
// Nothing about authorization changes here: the caller has already been checked
// for cluster.read on this Cluster, and the Agent answers for its own Cluster
// only. What changes is the cost — the Agent answers from a read model it keeps
// current with watches, instead of listing ten resources for every request.
func (service *Service) ClusterOverviewSnapshot(
	ctx context.Context,
	clusterID string,
) (resourcesummary.OverviewSnapshot, error) {
	if service == nil || ctx == nil || !validation.IsUUID(clusterID) {
		return resourcesummary.OverviewSnapshot{}, ErrInvalidInput
	}
	requester, ok := service.requester.(OverviewSnapshotRequester)
	if !ok {
		return resourcesummary.OverviewSnapshot{}, ErrAgentUnsupported
	}
	body := service.newResponseBuffer()
	defer body.Release()
	response, err := requester.RequestOverviewSnapshot(ctx, clusterID, body)
	if err != nil {
		return resourcesummary.OverviewSnapshot{}, snapshotRequestError(err)
	}
	if err := resourceCacheResponseError(response); err != nil {
		return resourcesummary.OverviewSnapshot{}, err
	}
	var snapshot resourcesummary.OverviewSnapshot
	if err := json.Unmarshal(body.Bytes(), &snapshot); err != nil {
		return resourcesummary.OverviewSnapshot{}, fmt.Errorf(
			"%w: decode Cluster overview snapshot",
			ErrInvalidResponse,
		)
	}
	if snapshot.GeneratedAt.IsZero() || len(snapshot.Sections) == 0 {
		return resourcesummary.OverviewSnapshot{}, fmt.Errorf(
			"%w: Cluster overview snapshot is incomplete",
			ErrInvalidResponse,
		)
	}
	return snapshot, nil
}

func snapshotRequestError(err error) error {
	if errors.Is(err, agentconn.ErrResourceCacheCapabilityMissing) {
		return ErrAgentUnsupported
	}
	return requestError(err)
}

func resourceCacheResponseError(response *agentv1.ResourceCacheResponse) error {
	if response == nil {
		return ErrInvalidResponse
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if response.GetContentType() != kubernetesJSONContentType ||
			response.GetBodySize() == 0 {
			return ErrInvalidResponse
		}
		return nil
	}
	switch response.GetResult() {
	case agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		return ErrInvalidInput
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		return ErrClusterAccessDenied
	case agentv1.ResultCode_RESULT_CODE_NOT_FOUND:
		return ErrResourceNotFound
	case agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		return ErrResponseTooLarge
	case agentv1.ResultCode_RESULT_CODE_UNAVAILABLE:
		// The Agent is there but its cache is not running. The caller falls back
		// to reading the Cluster directly rather than reporting a failure.
		return ErrAgentUnsupported
	case agentv1.ResultCode_RESULT_CODE_TIMEOUT:
		return ErrClusterTimeout
	case agentv1.ResultCode_RESULT_CODE_CANCELED:
		return context.Canceled
	default:
		return ErrUpstreamFailure
	}
}
