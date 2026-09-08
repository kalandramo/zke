package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
)

// errCacheUnavailable means this page cannot come from the Agent's cache: the
// Agent has none, does not follow this resource, has not finished its first
// listing, or cannot express the query exactly.
//
// It is not a failure. It is the signal to read Kubernetes, which is what the
// caller did before the cache existed and still does whenever the cache cannot
// answer with the same result.
var errCacheUnavailable = errors.New("Agent resource cache cannot answer this list")

// ErrCachePageExpired is a continuation token whose page is gone. The caller
// starts the listing again rather than returning a page from somewhere else.
var ErrCachePageExpired = errors.New("Agent resource cache page has expired")

// summaryRequester is the Agent connection that can answer a cached list.
type summaryRequester interface {
	RequestResourceSummaries(
		ctx context.Context,
		clusterID string,
		request *agentv1.ResourceCacheRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceCacheResponse, error)
}

// summaryListInput is one page of one cached resource.
type summaryListInput struct {
	ClusterID     string
	Resource      ResourceIdentity
	Namespace     string
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

// listSummaries asks the Agent's cache for one page.
//
// Authorization is unchanged and unaffected: every caller reaching here has
// already been checked for the Cluster and Namespace it named, and this only
// changes where the same rows are read from.
func (service *Service) listSummaries(
	ctx context.Context,
	input summaryListInput,
) (resourcesummary.Page, error) {
	if service == nil || ctx == nil {
		return resourcesummary.Page{}, ErrInvalidInput
	}
	requester, ok := service.requester.(summaryRequester)
	if !ok {
		return resourcesummary.Page{}, errCacheUnavailable
	}
	// The same rules the Kubernetes listing path applies. A request that the
	// other path would refuse has to be refused here too, or a caller would get
	// a different answer depending on which source happened to serve it.
	if err := validateListResourcesInput(ListResourcesInput{
		ClusterID:     input.ClusterID,
		Resource:      input.Resource,
		Namespace:     input.Namespace,
		Limit:         input.Limit,
		LabelSelector: input.LabelSelector,
		FieldSelector: input.FieldSelector,
	}); err != nil {
		return resourcesummary.Page{}, err
	}
	if input.Limit > int64(agentprotocol.MaxResourceCacheLimit) ||
		len(input.ContinueToken) > maxContinueTokenBytes ||
		strings.TrimSpace(input.ContinueToken) != input.ContinueToken {
		return resourcesummary.Page{}, ErrInvalidInput
	}
	body := service.newResponseBuffer()
	defer body.Release()
	response, err := requester.RequestResourceSummaries(
		ctx,
		input.ClusterID,
		&agentv1.ResourceCacheRequest{
			Query:         agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_SUMMARY_LIST,
			Resource:      protocolResource(input.Resource),
			Namespace:     input.Namespace,
			LabelSelector: input.LabelSelector,
			FieldSelector: input.FieldSelector,
			Limit:         uint32(input.Limit),
			PageToken:     input.ContinueToken,
		},
		body,
	)
	if err != nil {
		if errors.Is(err, agentconn.ErrResourceCacheCapabilityMissing) {
			return resourcesummary.Page{}, errCacheUnavailable
		}
		return resourcesummary.Page{}, requestError(err)
	}
	if err := summaryResponseError(response); err != nil {
		return resourcesummary.Page{}, err
	}
	var page resourcesummary.Page
	if err := json.Unmarshal(body.Bytes(), &page); err != nil {
		return resourcesummary.Page{}, fmt.Errorf(
			"%w: decode cached resource page",
			ErrInvalidResponse,
		)
	}
	return page, nil
}

// summaryResponseError separates the refusals that mean "read Kubernetes" from
// the ones that are genuine failures, because the caller acts on them
// differently: the first is a silent fallback, the second is an error.
func summaryResponseError(response *agentv1.ResourceCacheResponse) error {
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
	switch response.GetReason() {
	case "ResourceNotCached", "QueryNotCacheable", "ResourceCacheWarmingUp",
		"ResourceCacheUnavailable", "UnsupportedResourceCacheQuery":
		return errCacheUnavailable
	case "PageTokenExpired":
		return ErrCachePageExpired
	}
	return resourceCacheResponseError(response)
}

// decodeSummaries turns one cached page into the typed summaries the Server's
// list APIs return.
func decodeSummaries[T any](page resourcesummary.Page) ([]T, error) {
	items := make([]T, 0, len(page.Items))
	for _, raw := range page.Items {
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf(
				"%w: decode cached resource summary",
				ErrInvalidResponse,
			)
		}
		items = append(items, item)
	}
	return items, nil
}

// remainingItemCount reports what is left after this page, in the shape the
// list responses already use.
func remainingItemCount(page resourcesummary.Page) *int64 {
	if page.Remaining <= 0 {
		return nil
	}
	remaining := page.Remaining
	return &remaining
}
