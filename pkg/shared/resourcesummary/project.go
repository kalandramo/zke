package resourcesummary

import (
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	resourcehelper "k8s.io/component-helpers/resource"
)

// Workload resource names, matching the plural Kubernetes names the Server's
// workload API already uses.
const (
	WorkloadDeployments  = "deployments"
	WorkloadStatefulSets = "statefulsets"
	WorkloadDaemonSets   = "daemonsets"
	WorkloadJobs         = "jobs"
	WorkloadCronJobs     = "cronjobs"
)

func objectSummary(meta metav1.ObjectMeta) ObjectSummary {
	summary := ObjectSummary{
		UID:               string(meta.UID),
		Namespace:         meta.Namespace,
		Name:              meta.Name,
		ResourceVersion:   meta.ResourceVersion,
		CreationTimestamp: meta.CreationTimestamp.Time,
	}
	if len(meta.Labels) > 0 {
		summary.Labels = make(map[string]string, len(meta.Labels))
		for key, value := range meta.Labels {
			summary.Labels[key] = value
		}
	}
	if meta.DeletionTimestamp != nil {
		deletion := meta.DeletionTimestamp.Time
		summary.DeletionTimestamp = &deletion
	}
	return summary
}

// ProjectPod keeps the Kubernetes scheduling reading of a Pod's requests.
//
// resourcehelper.PodRequests is the same function the scheduler and
// `kubectl describe node` use, so init containers, restartable init
// containers, Pod-level resources and overhead are all accounted for the way
// Kubernetes accounts for them. A terminal Pod holds nothing, and contributes
// nothing.
func ProjectPod(pod *corev1.Pod) Pod {
	terminal := pod.Status.Phase == corev1.PodSucceeded ||
		pod.Status.Phase == corev1.PodFailed
	summary := Pod{
		ObjectSummary: objectSummary(pod.ObjectMeta),
		NodeName:      pod.Spec.NodeName,
		PodIP:         pod.Status.PodIP,
		Phase:         string(pod.Status.Phase),
		Reason:        pod.Status.Reason,
		Ready:         PodReady(pod),
		Terminating:   pod.DeletionTimestamp != nil,
		Terminal:      terminal,
		RestartCount:  PodRestartCount(pod.Status),
		Images:        PodAllImages(pod.Spec),
	}
	if controller := podController(pod.OwnerReferences); controller != nil {
		summary.Controller = controller
	}
	if !terminal {
		requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
		cpu := requests[corev1.ResourceCPU]
		memory := requests[corev1.ResourceMemory]
		if cpu.Sign() > 0 {
			summary.CPURequestsMillis = cpu.MilliValue()
		}
		if memory.Sign() > 0 {
			summary.MemoryRequestsBytes = memory.Value()
		}
	}
	return summary
}

func PodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func PodRestartCount(status corev1.PodStatus) int64 {
	total := int64(0)
	for _, container := range status.InitContainerStatuses {
		total += int64(container.RestartCount)
	}
	for _, container := range status.ContainerStatuses {
		total += int64(container.RestartCount)
	}
	return total
}

// PodImages is the set of images a Pod template runs, deduplicated and sorted.
//
// A list shows which images are in play, not how many containers happen to
// share one, and a stable order keeps the same Pod from appearing to change
// every time it is read.
func PodImages(spec corev1.PodSpec) []string {
	unique := make(map[string]struct{}, len(spec.Containers)+len(spec.InitContainers))
	for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, container := range containers {
			if container.Image != "" {
				unique[container.Image] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(unique))
	for image := range unique {
		result = append(result, image)
	}
	sort.Strings(result)
	return result
}

// PodAllImages adds the ephemeral containers a running Pod has been given.
// They belong to the Pod rather than to any template, so they are not part of
// what a workload reports.
func PodAllImages(spec corev1.PodSpec) []string {
	result := PodImages(spec)
	for _, container := range spec.EphemeralContainers {
		result = append(result, container.Image)
	}
	return result
}

func podController(owners []metav1.OwnerReference) *OwnerReference {
	for index := range owners {
		owner := owners[index]
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		return &OwnerReference{
			APIVersion: owner.APIVersion,
			Kind:       owner.Kind,
			Name:       owner.Name,
			UID:        string(owner.UID),
			Controller: true,
		}
	}
	return nil
}

func ProjectNode(node *corev1.Node) Node {
	taintKeys := make([]string, 0, len(node.Spec.Taints))
	for _, taint := range node.Spec.Taints {
		taintKeys = append(taintKeys, taint.Key)
	}
	sort.Strings(taintKeys)
	return Node{
		ObjectSummary:     objectSummary(node.ObjectMeta),
		Status:            NodeStatus(node),
		Unschedulable:     node.Spec.Unschedulable,
		Roles:             NodeRoles(node.Labels),
		InternalIP:        nodeAddress(node.Status.Addresses, corev1.NodeInternalIP),
		KubernetesVersion: node.Status.NodeInfo.KubeletVersion,
		OperatingSystem:   node.Status.NodeInfo.OperatingSystem,
		OSImage:           node.Status.NodeInfo.OSImage,
		KernelVersion:     node.Status.NodeInfo.KernelVersion,
		ContainerRuntime:  node.Status.NodeInfo.ContainerRuntimeVersion,
		CPUCapacity:       quantityText(node.Status.Capacity, corev1.ResourceCPU),
		MemoryCapacity:    quantityText(node.Status.Capacity, corev1.ResourceMemory),
		PodsCapacity:      quantityText(node.Status.Capacity, corev1.ResourcePods),
		CPUAllocatable:    quantityText(node.Status.Allocatable, corev1.ResourceCPU),
		MemoryAllocatable: quantityText(node.Status.Allocatable, corev1.ResourceMemory),
		PodsAllocatable:   quantityText(node.Status.Allocatable, corev1.ResourcePods),
		TaintCount:        len(node.Spec.Taints),
		TaintKeys:         taintKeys,
	}
}

func NodeStatus(node *corev1.Node) string {
	for _, condition := range node.Status.Conditions {
		if condition.Type != corev1.NodeReady {
			continue
		}
		switch condition.Status {
		case corev1.ConditionTrue:
			return "ready"
		case corev1.ConditionFalse:
			return "not_ready"
		default:
			return "unknown"
		}
	}
	return "unknown"
}

func NodeRoles(nodeLabels map[string]string) []string {
	const rolePrefix = "node-role.kubernetes.io/"
	roles := make(map[string]struct{})
	for key, value := range nodeLabels {
		if strings.HasPrefix(key, rolePrefix) {
			role := strings.TrimPrefix(key, rolePrefix)
			if role != "" {
				roles[role] = struct{}{}
			}
			continue
		}
		if key == "kubernetes.io/role" && value != "" {
			roles[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(roles))
	for role := range roles {
		result = append(result, role)
	}
	sort.Strings(result)
	return result
}

func nodeAddress(addresses []corev1.NodeAddress, kind corev1.NodeAddressType) string {
	for _, address := range addresses {
		if address.Type == kind {
			return address.Address
		}
	}
	return ""
}

func quantityText(resources corev1.ResourceList, name corev1.ResourceName) string {
	value, exists := resources[name]
	if !exists {
		return ""
	}
	return value.String()
}

// ProjectNamespace reports a Namespace being deleted as terminating whatever
// its stored phase says, which is the reading the overview has always used.
func ProjectNamespace(namespace *corev1.Namespace) Namespace {
	status := strings.ToLower(string(namespace.Status.Phase))
	if namespace.DeletionTimestamp != nil {
		status = strings.ToLower(string(corev1.NamespaceTerminating))
	}
	return Namespace{
		ObjectSummary: objectSummary(namespace.ObjectMeta),
		Phase:         string(namespace.Status.Phase),
		Status:        status,
	}
}

func ProjectDeployment(workload *appsv1.Deployment) Workload {
	desired := DesiredReplicas(workload.Spec.Replicas)
	return Workload{
		ObjectSummary:      objectSummary(workload.ObjectMeta),
		Resource:           WorkloadDeployments,
		Generation:         workload.Generation,
		ObservedGeneration: workload.Status.ObservedGeneration,
		Status:             DeploymentStatus(workload, desired),
		Images:             PodImages(workload.Spec.Template.Spec),
		Replicas: &WorkloadReplicas{
			Desired:     desired,
			Current:     workload.Status.Replicas,
			Updated:     workload.Status.UpdatedReplicas,
			Ready:       workload.Status.ReadyReplicas,
			Available:   workload.Status.AvailableReplicas,
			Unavailable: workload.Status.UnavailableReplicas,
		},
	}
}

// DeploymentStatus is the reading the workload list and the Cluster overview
// both publish. It lives here, beside the projection, so the Agent cache and
// the Server's own listing cannot drift into reporting the same Deployment
// differently.
func DeploymentStatus(workload *appsv1.Deployment, desired int32) string {
	if workload.Spec.Paused {
		return "suspended"
	}
	for _, condition := range workload.Status.Conditions {
		// Named after the Kubernetes reason it reads rather than reduced to a
		// health word: what has happened is that this rollout did not finish
		// inside progressDeadlineSeconds, which is not the same as the workload
		// having lost capacity — the previous ReplicaSet may still be serving
		// every request while the new one fails to start at all.
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return "progress_deadline_exceeded"
		}
	}
	if workload.Status.ObservedGeneration >= workload.Generation &&
		workload.Status.AvailableReplicas >= desired &&
		workload.Status.UnavailableReplicas == 0 {
		return "available"
	}
	return "progressing"
}

func ProjectStatefulSet(workload *appsv1.StatefulSet) Workload {
	desired := DesiredReplicas(workload.Spec.Replicas)
	return Workload{
		ObjectSummary:      objectSummary(workload.ObjectMeta),
		Resource:           WorkloadStatefulSets,
		Generation:         workload.Generation,
		ObservedGeneration: workload.Status.ObservedGeneration,
		Status:             StatefulSetStatus(workload, desired),
		Images:             PodImages(workload.Spec.Template.Spec),
		Replicas: &WorkloadReplicas{
			Desired:     desired,
			Current:     workload.Status.CurrentReplicas,
			Updated:     workload.Status.UpdatedReplicas,
			Ready:       workload.Status.ReadyReplicas,
			Available:   workload.Status.AvailableReplicas,
			Unavailable: max(0, desired-workload.Status.ReadyReplicas),
		},
	}
}

func StatefulSetStatus(workload *appsv1.StatefulSet, desired int32) string {
	if workload.Status.ObservedGeneration >= workload.Generation &&
		workload.Status.ReadyReplicas >= desired &&
		workload.Status.CurrentRevision == workload.Status.UpdateRevision {
		return "available"
	}
	return "progressing"
}

func ProjectDaemonSet(workload *appsv1.DaemonSet) Workload {
	return Workload{
		ObjectSummary:      objectSummary(workload.ObjectMeta),
		Resource:           WorkloadDaemonSets,
		Generation:         workload.Generation,
		ObservedGeneration: workload.Status.ObservedGeneration,
		Status:             DaemonSetStatus(workload),
		Images:             PodImages(workload.Spec.Template.Spec),
		Replicas: &WorkloadReplicas{
			Desired:     workload.Status.DesiredNumberScheduled,
			Current:     workload.Status.CurrentNumberScheduled,
			Updated:     workload.Status.UpdatedNumberScheduled,
			Ready:       workload.Status.NumberReady,
			Available:   workload.Status.NumberAvailable,
			Unavailable: workload.Status.NumberUnavailable,
		},
	}
}

func DaemonSetStatus(workload *appsv1.DaemonSet) string {
	if workload.Status.ObservedGeneration >= workload.Generation &&
		workload.Status.NumberAvailable >= workload.Status.DesiredNumberScheduled &&
		workload.Status.NumberUnavailable == 0 {
		return "available"
	}
	return "progressing"
}

func ProjectJob(workload *batchv1.Job) Workload {
	return Workload{
		ObjectSummary: objectSummary(workload.ObjectMeta),
		Resource:      WorkloadJobs,
		Generation:    workload.Generation,
		Status:        JobStatus(workload),
		Images:        PodImages(workload.Spec.Template.Spec),
		Job: &WorkloadJob{
			Parallelism:    cloneInt32(workload.Spec.Parallelism),
			Completions:    cloneInt32(workload.Spec.Completions),
			Active:         workload.Status.Active,
			Succeeded:      workload.Status.Succeeded,
			Failed:         workload.Status.Failed,
			StartTime:      timePointer(workload.Status.StartTime),
			CompletionTime: timePointer(workload.Status.CompletionTime),
		},
	}
}

func JobStatus(workload *batchv1.Job) string {
	if workload.Spec.Suspend != nil && *workload.Spec.Suspend {
		return "suspended"
	}
	for _, condition := range workload.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobFailed:
			return "failed"
		case batchv1.JobComplete:
			return "completed"
		}
	}
	if workload.Status.Active > 0 {
		return "running"
	}
	return "pending"
}

func ProjectCronJob(workload *batchv1.CronJob) Workload {
	suspend := workload.Spec.Suspend != nil && *workload.Spec.Suspend
	return Workload{
		ObjectSummary: objectSummary(workload.ObjectMeta),
		Resource:      WorkloadCronJobs,
		Generation:    workload.Generation,
		Status:        CronJobStatus(workload),
		Images:        PodImages(workload.Spec.JobTemplate.Spec.Template.Spec),
		CronJob: &WorkloadCronJob{
			Schedule:           workload.Spec.Schedule,
			Suspend:            suspend,
			Active:             len(workload.Status.Active),
			LastScheduleTime:   timePointer(workload.Status.LastScheduleTime),
			LastSuccessfulTime: timePointer(workload.Status.LastSuccessfulTime),
		},
	}
}

func CronJobStatus(workload *batchv1.CronJob) string {
	if workload.Spec.Suspend != nil && *workload.Spec.Suspend {
		return "suspended"
	}
	if len(workload.Status.Active) > 0 {
		return "running"
	}
	return "scheduled"
}

func ProjectPersistentVolume(volume *corev1.PersistentVolume) PersistentVolume {
	capacity := volume.Spec.Capacity[corev1.ResourceStorage]
	summary := PersistentVolume{
		ObjectSummary: objectSummary(volume.ObjectMeta),
		// The phase as `kubectl get pv` prints it, and nothing derived: a
		// volume being deleted keeps the phase that says whether anything still
		// claims it.
		Status:        strings.ToLower(string(volume.Status.Phase)),
		Capacity:      capacity.String(),
		AccessModes:   accessModes(volume.Spec.AccessModes),
		ReclaimPolicy: string(volume.Spec.PersistentVolumeReclaimPolicy),
		StorageClass:  volume.Spec.StorageClassName,
	}
	if capacity.Sign() > 0 {
		summary.CapacityBytes = capacity.Value()
	}
	if volume.Spec.ClaimRef != nil {
		summary.ClaimRef = volume.Spec.ClaimRef.Namespace + "/" + volume.Spec.ClaimRef.Name
	}
	if volume.Spec.VolumeMode != nil {
		summary.VolumeMode = string(*volume.Spec.VolumeMode)
	}
	return summary
}

func ProjectPersistentVolumeClaim(claim *corev1.PersistentVolumeClaim) PersistentVolumeClaim {
	requested := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	capacity := claim.Status.Capacity[corev1.ResourceStorage]
	summary := PersistentVolumeClaim{
		ObjectSummary: objectSummary(claim.ObjectMeta),
		Status:        strings.ToLower(string(claim.Status.Phase)),
		Requested:     requested.String(),
		Capacity:      capacity.String(),
		AccessModes:   accessModes(claim.Spec.AccessModes),
		VolumeName:    claim.Spec.VolumeName,
	}
	if requested.Sign() > 0 {
		summary.RequestedBytes = requested.Value()
	}
	if capacity.Sign() > 0 {
		summary.CapacityBytes = capacity.Value()
	}
	if claim.Spec.StorageClassName != nil {
		summary.StorageClass = *claim.Spec.StorageClassName
	}
	if claim.Spec.VolumeMode != nil {
		summary.VolumeMode = string(*claim.Spec.VolumeMode)
	}
	return summary
}

func accessModes(modes []corev1.PersistentVolumeAccessMode) []string {
	result := make([]string, 0, len(modes))
	for _, mode := range modes {
		result = append(result, string(mode))
	}
	return result
}

// DesiredReplicas reads the Kubernetes default: a workload without an explicit
// replica count wants one.
func DesiredReplicas(value *int32) int32 {
	if value == nil {
		return 1
	}
	return *value
}

func cloneInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func timePointer(value *metav1.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := value.Time
	return &copied
}
