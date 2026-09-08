package kubernetesresource

import (
	"context"
	"encoding/json"
	"fmt"
)

// cachedObjects is one page of Kubernetes objects the Agent holds, with the
// parts no list shows already removed.
//
// The Server projects them with exactly the functions it uses for a page read
// from Kubernetes, so a list served from the cache and the same list served
// from the Cluster cannot disagree about what a row means.
type cachedObjects struct {
	Items              []map[string]any
	ContinueToken      string
	ResourceVersion    string
	RemainingItemCount *int64
}

func (service *Service) listCachedObjects(
	ctx context.Context,
	input summaryListInput,
) (cachedObjects, bool, error) {
	page, err := service.listSummaries(ctx, input)
	if err != nil {
		if fellBackToKubernetes(err) {
			return cachedObjects{}, false, nil
		}
		return cachedObjects{}, false, err
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, raw := range page.Items {
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			return cachedObjects{}, false, fmt.Errorf(
				"%w: decode cached Kubernetes object",
				ErrInvalidResponse,
			)
		}
		items = append(items, object)
	}
	return cachedObjects{
		Items:              items,
		ContinueToken:      cachedToken(page.NextPageToken),
		ResourceVersion:    page.ResourceVersion,
		RemainingItemCount: remainingItemCount(page),
	}, true, nil
}
