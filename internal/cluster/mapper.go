package cluster

import (
	"maps"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubexray/kubexray/internal/graph"
)

// Kinds that kubexray maps. ConfigMaps and Secrets are watched as metadata
// only, so their contents never reach the process.
const (
	KindNamespace   = "Namespace"
	KindNode        = "Node"
	KindPod         = "Pod"
	KindDeployment  = "Deployment"
	KindReplicaSet  = "ReplicaSet"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
	KindJob         = "Job"
	KindService     = "Service"
	KindIngress     = "Ingress"
	KindConfigMap   = "ConfigMap"
	KindSecret      = "Secret"
	KindPVC         = "PersistentVolumeClaim"
	KindQuota       = "ResourceQuota"
)

// baseNode fills the fields every node has. Namespaced objects are grouped
// under their Namespace.
func baseNode(kind string, m metav1.Object) graph.Node {
	n := graph.Node{
		ID:        graph.ID(kind, m.GetNamespace(), m.GetName()),
		Kind:      kind,
		Namespace: m.GetNamespace(),
		Name:      m.GetName(),
		Labels:    m.GetLabels(),
		Created:   m.GetCreationTimestamp().Time,
		Change:    lastChange(m),
		Origin:    originOf(m),
	}
	if ns := m.GetNamespace(); ns != "" {
		n.Parent = graph.ID(KindNamespace, "", ns)
	}
	return n
}

// clusterScoped lists the mapped kinds that have no namespace. Owner
// references don't say whether the owner is namespaced, so this decides.
var clusterScoped = map[string]bool{KindNamespace: true, KindNode: true}

// ownerEdges links an object to each of its owners.
// mapConfigMap maps a ConfigMap with its contents, when values are on.
func mapConfigMap(cm *corev1.ConfigMap) (graph.Node, []graph.Edge) {
	n := baseNode(KindConfigMap, cm)
	n.Status.Ready = true
	n.Data = configData(cm)
	return n, ownerEdges(n.ID, cm)
}

func ownerEdges(id string, m metav1.Object) []graph.Edge {
	edges := make([]graph.Edge, 0, len(m.GetOwnerReferences()))
	for _, ref := range m.GetOwnerReferences() {
		ns := m.GetNamespace()
		if clusterScoped[ref.Kind] {
			ns = "" // e.g. a static pod owned by its Node
		}
		edges = append(edges, graph.NewEdge(graph.EdgeOwns, graph.ID(ref.Kind, ns, ref.Name), id))
	}
	return edges
}

func mapNamespace(ns *corev1.Namespace) (graph.Node, []graph.Edge) {
	n := baseNode(KindNamespace, ns)
	n.Status = graph.Status{Phase: string(ns.Status.Phase), Ready: ns.Status.Phase == corev1.NamespaceActive}
	return n, nil
}

func mapNode(node *corev1.Node) (graph.Node, []graph.Edge) {
	n := baseNode(KindNode, node)
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			n.Status.Ready = c.Status == corev1.ConditionTrue
			n.Status.Since = c.LastTransitionTime.Time
			if !n.Status.Ready {
				n.Status.Reason = c.Reason
			}
		}
	}
	if node.Spec.Unschedulable && n.Status.Reason == "" {
		n.Status.Reason = "SchedulingDisabled" // a NotReady reason is more important
	}
	return n, nil
}

func mapPod(pod *corev1.Pod) (graph.Node, []graph.Edge) {
	n := baseNode(KindPod, pod)
	if pod.Spec.NodeName != "" {
		n.Parent = graph.ID(KindNode, "", pod.Spec.NodeName)
	}
	n.Status = podStatus(pod)
	n.Spec = podSpec(pod, &pod.Spec)

	edges := ownerEdges(n.ID, pod)
	for _, id := range podDependencies(pod) {
		edges = append(edges, graph.NewEdge(graph.EdgeMounts, n.ID, id))
	}
	return n, edges
}

// maxMessage caps the Kubernetes messages kept on a status (ADR-0009).
const maxMessage = 300

func message(m string) string {
	if len(m) <= maxMessage {
		return m
	}
	cut := maxMessage
	for cut > 0 && !utf8.RuneStart(m[cut]) {
		cut-- // don't split a character
	}
	return m[:cut] + "…"
}

// why is one container's explanation of a pod's state.
type why struct {
	reason, container, message string
	exit                       int32
}

// podStatus summarises a pod the way `kubectl get pods` would: the most
// telling reason wins, with the container, exit code and Kubernetes message
// behind it. A container's own termination message is never read: the app
// writes it (ADR-0009).
func podStatus(pod *corev1.Pod) graph.Status {
	s := graph.Status{Phase: string(pod.Status.Phase), Reason: pod.Status.Reason}
	var unschedulable bool
	var scheduler string
	for _, c := range pod.Status.Conditions {
		switch c.Type { //nolint:exhaustive // only these conditions matter
		case corev1.PodReady:
			s.Ready = c.Status == corev1.ConditionTrue
			s.Since = c.LastTransitionTime.Time
		case corev1.PodScheduled:
			unschedulable = c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable
			scheduler = c.Message
		}
	}

	// Init containers run first, so a failing one explains everything after it.
	var initProblem why
	for _, cs := range pod.Status.InitContainerStatuses {
		s.Restarts += cs.RestartCount
		switch {
		case initProblem.reason != "":
		case cs.State.Waiting != nil && !isStarting(cs.State.Waiting.Reason):
			initProblem = waitingWhy(cs)
			initProblem.reason = "Init:" + initProblem.reason
		case cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0:
			initProblem = why{reason: "Init:" + cs.State.Terminated.Reason, container: cs.Name, exit: cs.State.Terminated.ExitCode}
		}
	}

	var waiting, terminated, last why
	for _, cs := range pod.Status.ContainerStatuses {
		s.Restarts += cs.RestartCount
		switch {
		case cs.State.Waiting != nil && (waiting.reason == "" || isStarting(waiting.reason)):
			waiting = waitingWhy(cs)
		case cs.State.Terminated != nil && terminated.reason == "":
			terminated = why{reason: cs.State.Terminated.Reason, container: cs.Name, exit: cs.State.Terminated.ExitCode}
		}
		if t := cs.LastTerminationState.Terminated; t != nil && last.reason == "" {
			last = why{reason: t.Reason, container: cs.Name, exit: t.ExitCode}
		}
	}

	use := func(w why) { s.Reason, s.Container, s.Message, s.Exit = w.reason, w.container, w.message, w.exit }
	switch {
	case pod.DeletionTimestamp != nil:
		s.Reason = "Terminating"
	case pod.Status.Reason == "Evicted":
		s.Message = pod.Status.Message // the kubelet's, e.g. "The node was low on resource: memory."
	case initProblem.reason != "":
		use(initProblem) // e.g. Init:CrashLoopBackOff
	case unschedulable:
		s.Reason, s.Message = corev1.PodReasonUnschedulable, scheduler
	case waiting.reason != "" && !isStarting(waiting.reason):
		use(waiting) // e.g. CrashLoopBackOff, ImagePullBackOff
	case terminated.reason != "":
		use(terminated) // e.g. OOMKilled, Error, Completed
	case waiting.reason != "":
		s.Reason = waiting.reason
	case s.Reason == "" && last.reason != "" && s.Restarts > 0:
		use(last) // recovered, but say why it restarted
	}
	if s.Message == "" && s.Reason == pod.Status.Reason {
		s.Message = pod.Status.Message // the kubelet's, e.g. a node shutdown
	}
	s.Message = message(s.Message)
	return s
}

// waitingWhy explains a waiting container. A crash loop's own message only
// says it's backing off, so the last exit says more: a container that keeps
// running out of memory is OOMKilled, not just crash-looping.
func waitingWhy(cs corev1.ContainerStatus) why {
	w := why{reason: cs.State.Waiting.Reason, container: cs.Name, message: cs.State.Waiting.Message}
	t := cs.LastTerminationState.Terminated
	if t != nil {
		w.exit = t.ExitCode
	}
	if w.reason == "CrashLoopBackOff" {
		w.message = ""
		if t != nil && t.Reason == "OOMKilled" {
			w.reason = t.Reason
		}
	}
	return w
}

// isStarting reports whether a waiting reason is a normal part of startup
// rather than a problem.
func isStarting(reason string) bool {
	return reason == "ContainerCreating" || reason == "PodInitializing"
}

// podDependencies returns the IDs of the ConfigMaps, Secrets and PVCs a pod
// uses, through volumes or environment variables, without duplicates.
func podDependencies(pod *corev1.Pod) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(kind, name string) {
		if name == "" {
			return
		}
		id := graph.ID(kind, pod.Namespace, name)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	for _, v := range pod.Spec.Volumes {
		switch {
		case v.ConfigMap != nil:
			add(KindConfigMap, v.ConfigMap.Name)
		case v.Secret != nil:
			add(KindSecret, v.Secret.SecretName)
		case v.PersistentVolumeClaim != nil:
			add(KindPVC, v.PersistentVolumeClaim.ClaimName)
		case v.Projected != nil:
			for _, src := range v.Projected.Sources {
				if src.ConfigMap != nil {
					add(KindConfigMap, src.ConfigMap.Name)
				}
				if src.Secret != nil {
					add(KindSecret, src.Secret.Name)
				}
			}
		}
	}

	containers := make([]corev1.Container, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	containers = append(containers, pod.Spec.InitContainers...)
	containers = append(containers, pod.Spec.Containers...)
	for _, c := range containers {
		for _, src := range c.EnvFrom {
			if src.ConfigMapRef != nil {
				add(KindConfigMap, src.ConfigMapRef.Name)
			}
			if src.SecretRef != nil {
				add(KindSecret, src.SecretRef.Name)
			}
		}
		for _, env := range c.Env {
			if env.ValueFrom == nil {
				continue
			}
			if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
				add(KindConfigMap, ref.Name)
			}
			if ref := env.ValueFrom.SecretKeyRef; ref != nil {
				add(KindSecret, ref.Name)
			}
		}
	}
	for _, s := range pod.Spec.ImagePullSecrets {
		add(KindSecret, s.Name)
	}
	return ids
}

// replicas builds a workload status; ready means fully rolled out.
func replicas(desired *int32, ready, updated int32) graph.Status {
	d := int32(1) // Kubernetes defaults spec.replicas to 1
	if desired != nil {
		d = *desired
	}
	return graph.Status{
		Ready:    ready == d && updated == d,
		Replicas: &graph.Replicas{Desired: d, Ready: ready, Updated: updated},
	}
}

func mapDeployment(d *appsv1.Deployment) (graph.Node, []graph.Edge) {
	n := baseNode(KindDeployment, d)
	n.Status = replicas(d.Spec.Replicas, d.Status.ReadyReplicas, d.Status.UpdatedReplicas)
	n.Spec = podSpec(d, &d.Spec.Template.Spec)
	n.Spec.Labels = maps.Clone(d.Spec.Template.Labels)
	for _, c := range d.Status.Conditions {
		switch {
		case c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded":
			n.Status.Reason = c.Reason
		case c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue:
			// Pods couldn't be created: a quota, an admission webhook, pod security.
			n.Status.Message = message(c.Message)
			n.Status.Since = c.LastTransitionTime.Time
			if n.Status.Reason == "" {
				n.Status.Reason = c.Reason
			}
		}
	}
	return n, ownerEdges(n.ID, d)
}

func mapReplicaSet(rs *appsv1.ReplicaSet) (graph.Node, []graph.Edge) {
	n := baseNode(KindReplicaSet, rs)
	// A ReplicaSet has no rollout of its own, so all its pods count as updated.
	n.Status = replicas(rs.Spec.Replicas, rs.Status.ReadyReplicas, rs.Status.Replicas)
	n.Spec = podSpec(rs, &rs.Spec.Template.Spec)
	return n, ownerEdges(n.ID, rs)
}

func mapStatefulSet(ss *appsv1.StatefulSet) (graph.Node, []graph.Edge) {
	n := baseNode(KindStatefulSet, ss)
	n.Status = replicas(ss.Spec.Replicas, ss.Status.ReadyReplicas, ss.Status.UpdatedReplicas)
	n.Spec = podSpec(ss, &ss.Spec.Template.Spec)
	n.Spec.Labels = maps.Clone(ss.Spec.Template.Labels)
	return n, ownerEdges(n.ID, ss)
}

func mapDaemonSet(ds *appsv1.DaemonSet) (graph.Node, []graph.Edge) {
	n := baseNode(KindDaemonSet, ds)
	desired := ds.Status.DesiredNumberScheduled
	n.Status = replicas(&desired, ds.Status.NumberReady, ds.Status.UpdatedNumberScheduled)
	n.Spec = podSpec(ds, &ds.Spec.Template.Spec)
	return n, ownerEdges(n.ID, ds)
}

func mapJob(job *batchv1.Job) (graph.Node, []graph.Edge) {
	n := baseNode(KindJob, job)
	n.Spec = podSpec(job, &job.Spec.Template.Spec)
	n.Status.Phase = "Running"
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type { //nolint:exhaustive // only terminal conditions matter
		case batchv1.JobComplete:
			n.Status.Phase, n.Status.Ready = "Complete", true
		case batchv1.JobFailed:
			n.Status.Phase, n.Status.Reason = "Failed", c.Reason
		}
	}
	return n, ownerEdges(n.ID, job)
}

func mapService(svc *corev1.Service) (graph.Node, []graph.Edge) {
	n := baseNode(KindService, svc)
	n.Status = graph.Status{Phase: string(svc.Spec.Type), Ready: true}
	n.Selector = maps.Clone(svc.Spec.Selector)
	return n, ownerEdges(n.ID, svc)
}

// mapResourceQuota keeps only who changed a quota and when: refused pods
// name the quota (ADR-0009), and an edit to it can be the cause.
func mapResourceQuota(q *corev1.ResourceQuota) (graph.Node, []graph.Edge) {
	n := baseNode(KindQuota, q)
	n.Status.Ready = true
	return n, ownerEdges(n.ID, q)
}

func mapIngress(ing *networkingv1.Ingress) (graph.Node, []graph.Edge) {
	n := baseNode(KindIngress, ing)
	n.Status = graph.Status{Ready: len(ing.Status.LoadBalancer.Ingress) > 0}

	edges := ownerEdges(n.ID, ing)
	seen := map[string]bool{}
	route := func(b *networkingv1.IngressBackend) {
		if b == nil || b.Service == nil {
			return
		}
		e := graph.NewEdge(graph.EdgeRoutes, n.ID, graph.ID(KindService, ing.Namespace, b.Service.Name))
		if !seen[e.ID] {
			seen[e.ID] = true
			edges = append(edges, e)
		}
	}
	route(ing.Spec.DefaultBackend)
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			route(&p.Backend)
		}
	}
	return n, edges
}

func mapPVC(pvc *corev1.PersistentVolumeClaim) (graph.Node, []graph.Edge) {
	n := baseNode(KindPVC, pvc)
	n.Status = graph.Status{Phase: string(pvc.Status.Phase), Ready: pvc.Status.Phase == corev1.ClaimBound}
	return n, ownerEdges(n.ID, pvc)
}

// mapMetadata maps a metadata-only object (ConfigMap, Secret).
func mapMetadata(kind string) func(*metav1.PartialObjectMetadata) (graph.Node, []graph.Edge) {
	return func(m *metav1.PartialObjectMetadata) (graph.Node, []graph.Edge) {
		n := baseNode(kind, m)
		n.Status.Ready = true
		return n, ownerEdges(n.ID, m)
	}
}
