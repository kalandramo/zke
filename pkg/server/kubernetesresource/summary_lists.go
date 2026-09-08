package kubernetesresource

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
)

// cachedTokenPrefix marks a continuation this Server issued from an Agent's
// cache.
//
// A list can page through either source, and the token has to say which: a
// Kubernetes continue token means nothing to the Agent's cache, and a cache
// token means nothing to Kubernetes. Kubernetes tokens are base64 and never
// carry a dot, so the prefix cannot collide with one.
const cachedTokenPrefix = "zc1."

func cachedToken(token string) string {
	if token == "" {
		return ""
	}
	return cachedTokenPrefix + token
}

// splitContinueToken says where a continuation belongs, and hands back the
// token in the form that source expects.
func splitContinueToken(token string) (string, bool) {
	if inner, cached := strings.CutPrefix(token, cachedTokenPrefix); cached {
		return inner, true
	}
	return token, false
}

// cacheableList decides whether this page may come from the Agent's cache.
//
// A continuation is bound to the source that issued it: a page that started
// against Kubernetes finishes against Kubernetes, and one that started against
// the cache finishes against the cache. Mixing them would silently skip or
// repeat rows.
func cacheableList(continueToken string) (string, bool) {
	if continueToken == "" {
		return "", true
	}
	return splitContinueToken(continueToken)
}

// fellBackToKubernetes reports whether this refusal means "read Kubernetes".
func fellBackToKubernetes(err error) bool {
	return errors.Is(err, ErrCacheUnavailable)
}

// ListPodsFromCache answers one Pod page from the Agent's read model.
func (service *Service) listPodsFromCache(
	ctx context.Context,
	input ListPodsInput,
	pageToken string,
) (PodPage, bool, error) {
	page, err := service.listSummaries(ctx, SummaryListInput{
		ClusterID:     input.ClusterID,
		Resource:      podIdentity,
		Namespace:     input.Namespace,
		Limit:         input.Limit,
		ContinueToken: pageToken,
		LabelSelector: input.LabelSelector,
		FieldSelector: input.FieldSelector,
	})
	if err != nil {
		if fellBackToKubernetes(err) {
			return PodPage{}, false, nil
		}
		return PodPage{}, false, err
	}
	summaries, err := decodeSummaries[resourcesummary.Pod](page)
	if err != nil {
		return PodPage{}, false, err
	}
	pods := make([]PodSummary, 0, len(summaries))
	for _, summary := range summaries {
		pods = append(pods, podSummaryFromCache(summary))
	}
	return PodPage{
		Pods:               pods,
		ContinueToken:      cachedToken(page.NextPageToken),
		ResourceVersion:    page.ResourceVersion,
		RemainingItemCount: remainingItemCount(page),
	}, true, nil
}

func podSummaryFromCache(summary resourcesummary.Pod) PodSummary {
	result := PodSummary{
		APIVersion:        "v1",
		Kind:              "Pod",
		Namespace:         summary.Namespace,
		Name:              summary.Name,
		UID:               summary.UID,
		ResourceVersion:   summary.ResourceVersion,
		CreationTimestamp: summary.CreationTimestamp,
		DeletionTimestamp: summary.DeletionTimestamp,
		Labels:            presentMap(summary.Labels),
		Phase:             summary.Phase,
		Reason:            summary.Reason,
		Ready:             summary.Ready,
		NodeName:          summary.NodeName,
		PodIP:             summary.PodIP,
		RestartCount:      summary.RestartCount,
		Images:            presentSlice(summary.Images),
	}
	if summary.Controller != nil {
		result.Controller = &PodOwnerReference{
			APIVersion: summary.Controller.APIVersion,
			Kind:       summary.Controller.Kind,
			Name:       summary.Controller.Name,
			UID:        summary.Controller.UID,
			Controller: true,
			// BlockOwnerDeletion is not part of what a list shows, and the cache
			// does not keep it. A caller that needs it reads the Pod.
		}
	}
	return result
}

func (service *Service) listNodesFromCache(
	ctx context.Context,
	input ListNodesInput,
	pageToken string,
) (NodePage, bool, error) {
	page, err := service.listSummaries(ctx, SummaryListInput{
		ClusterID:     input.ClusterID,
		Resource:      ResourceIdentity{Version: "v1", Resource: "nodes"},
		Limit:         input.Limit,
		ContinueToken: pageToken,
		LabelSelector: input.LabelSelector,
		FieldSelector: input.FieldSelector,
	})
	if err != nil {
		if fellBackToKubernetes(err) {
			return NodePage{}, false, nil
		}
		return NodePage{}, false, err
	}
	summaries, err := decodeSummaries[resourcesummary.Node](page)
	if err != nil {
		return NodePage{}, false, err
	}
	nodes := make([]NodeSummary, 0, len(summaries))
	for _, summary := range summaries {
		nodes = append(nodes, NodeSummary{
			Name:              summary.Name,
			UID:               summary.UID,
			CreationTimestamp: summary.CreationTimestamp,
			Status:            summary.Status,
			Unschedulable:     summary.Unschedulable,
			Roles:             presentSlice(summary.Roles),
			InternalIP:        summary.InternalIP,
			KubernetesVersion: summary.KubernetesVersion,
			OperatingSystem:   summary.OperatingSystem,
			OSImage:           summary.OSImage,
			KernelVersion:     summary.KernelVersion,
			ContainerRuntime:  summary.ContainerRuntime,
			CPUCapacity:       summary.CPUCapacity,
			MemoryCapacity:    summary.MemoryCapacity,
			PodsCapacity:      summary.PodsCapacity,
			CPUAllocatable:    summary.CPUAllocatable,
			MemoryAllocatable: summary.MemoryAllocatable,
			PodsAllocatable:   summary.PodsAllocatable,
		})
	}
	return NodePage{
		Nodes:              nodes,
		ContinueToken:      cachedToken(page.NextPageToken),
		ResourceVersion:    page.ResourceVersion,
		RemainingItemCount: remainingItemCount(page),
	}, true, nil
}

func (service *Service) listNamespacesFromCache(
	ctx context.Context,
	input ListNamespacesInput,
	pageToken string,
) (NamespacePage, bool, error) {
	page, err := service.listSummaries(ctx, SummaryListInput{
		ClusterID:     input.ClusterID,
		Resource:      namespaceIdentity,
		Limit:         input.Limit,
		ContinueToken: pageToken,
		LabelSelector: input.LabelSelector,
		FieldSelector: input.FieldSelector,
	})
	if err != nil {
		if fellBackToKubernetes(err) {
			return NamespacePage{}, false, nil
		}
		return NamespacePage{}, false, err
	}
	summaries, err := decodeSummaries[resourcesummary.Namespace](page)
	if err != nil {
		return NamespacePage{}, false, err
	}
	namespaces := make([]NamespaceSummary, 0, len(summaries))
	for _, summary := range summaries {
		namespaces = append(namespaces, NamespaceSummary{
			Name:              summary.Name,
			UID:               summary.UID,
			ResourceVersion:   summary.ResourceVersion,
			CreationTimestamp: summary.CreationTimestamp,
			// The stored phase, which is what this contract has always
			// published; the overview's reading of a deleting Namespace is a
			// different field and stays out of here.
			Phase:  summary.Phase,
			Labels: presentMap(summary.Labels),
		})
	}
	return NamespacePage{
		Namespaces:         namespaces,
		ContinueToken:      cachedToken(page.NextPageToken),
		ResourceVersion:    page.ResourceVersion,
		RemainingItemCount: remainingItemCount(page),
	}, true, nil
}

func (service *Service) listWorkloadsFromCache(
	ctx context.Context,
	input ListWorkloadsInput,
	pageToken string,
) (WorkloadPage, bool, error) {
	identity, exists := workloadIdentities[input.Resource]
	if !exists {
		return WorkloadPage{}, false, ErrInvalidInput
	}
	page, err := service.listSummaries(ctx, SummaryListInput{
		ClusterID:     input.ClusterID,
		Resource:      identity,
		Namespace:     input.Namespace,
		Limit:         input.Limit,
		ContinueToken: pageToken,
		LabelSelector: input.LabelSelector,
		FieldSelector: input.FieldSelector,
	})
	if err != nil {
		if fellBackToKubernetes(err) {
			return WorkloadPage{}, false, nil
		}
		return WorkloadPage{}, false, err
	}
	summaries, err := decodeSummaries[resourcesummary.Workload](page)
	if err != nil {
		return WorkloadPage{}, false, err
	}
	apiVersion, kind := workloadTypeMeta(input.Resource)
	workloads := make([]WorkloadSummary, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Resource != string(input.Resource) {
			return WorkloadPage{}, false, ErrInvalidResponse
		}
		workloads = append(workloads, WorkloadSummary{
			Resource:           input.Resource,
			APIVersion:         apiVersion,
			Kind:               kind,
			Namespace:          summary.Namespace,
			Name:               summary.Name,
			UID:                summary.UID,
			ResourceVersion:    summary.ResourceVersion,
			CreationTimestamp:  summary.CreationTimestamp,
			Generation:         summary.Generation,
			ObservedGeneration: summary.ObservedGeneration,
			Labels:             presentMap(summary.Labels),
			Status:             summary.Status,
			Images:             presentSlice(summary.Images),
			Replicas:           workloadReplicasFromCache(summary.Replicas),
			Job:                workloadJobFromCache(summary.Job),
			CronJob:            workloadCronJobFromCache(summary.CronJob),
		})
	}
	return WorkloadPage{
		Workloads:          workloads,
		ContinueToken:      cachedToken(page.NextPageToken),
		ResourceVersion:    page.ResourceVersion,
		RemainingItemCount: remainingItemCount(page),
	}, true, nil
}

func workloadTypeMeta(resource WorkloadResource) (string, string) {
	switch resource {
	case WorkloadDeployments:
		return "apps/v1", "Deployment"
	case WorkloadStatefulSets:
		return "apps/v1", "StatefulSet"
	case WorkloadDaemonSets:
		return "apps/v1", "DaemonSet"
	case WorkloadJobs:
		return "batch/v1", "Job"
	case WorkloadCronJobs:
		return "batch/v1", "CronJob"
	default:
		return "", ""
	}
}

func workloadReplicasFromCache(source *resourcesummary.WorkloadReplicas) *WorkloadReplicaStatus {
	if source == nil {
		return nil
	}
	return &WorkloadReplicaStatus{
		Desired:     source.Desired,
		Current:     source.Current,
		Updated:     source.Updated,
		Ready:       source.Ready,
		Available:   source.Available,
		Unavailable: source.Unavailable,
	}
}

func workloadJobFromCache(source *resourcesummary.WorkloadJob) *WorkloadJobStatus {
	if source == nil {
		return nil
	}
	return &WorkloadJobStatus{
		Parallelism:    source.Parallelism,
		Completions:    source.Completions,
		Active:         source.Active,
		Succeeded:      source.Succeeded,
		Failed:         source.Failed,
		StartTime:      source.StartTime,
		CompletionTime: source.CompletionTime,
	}
}

func workloadCronJobFromCache(source *resourcesummary.WorkloadCronJob) *WorkloadCronJobStatus {
	if source == nil {
		return nil
	}
	return &WorkloadCronJobStatus{
		Schedule:           source.Schedule,
		Suspend:            source.Suspend,
		Active:             source.Active,
		LastScheduleTime:   source.LastScheduleTime,
		LastSuccessfulTime: source.LastSuccessfulTime,
	}
}

// presentMap and presentSlice keep the response contract: these fields are
// declared as an object and an array, and a nil one marshals to null.
func presentMap(source map[string]string) map[string]string {
	if source == nil {
		return map[string]string{}
	}
	return maps.Clone(source)
}

func presentSlice(source []string) []string {
	if source == nil {
		return []string{}
	}
	return source
}
