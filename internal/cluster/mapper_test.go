package cluster

import (
	"fmt"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kubexray/kubexray/internal/graph"
)

func meta(ns, name string, owners ...metav1.OwnerReference) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: ns, Name: name, OwnerReferences: owners}
}

func owner(kind, name string) metav1.OwnerReference {
	return metav1.OwnerReference{Kind: kind, Name: name}
}

func edgeIDs(edges []graph.Edge) []string {
	ids := make([]string, 0, len(edges))
	for _, e := range edges {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	return ids
}

func assertEdges(t *testing.T, got []graph.Edge, want ...string) {
	t.Helper()
	slices.Sort(want)
	if fmt.Sprint(edgeIDs(got)) != fmt.Sprint(want) {
		t.Errorf("edges = %v\nwant    %v", edgeIDs(got), want)
	}
}

func TestBaseNode(t *testing.T) {
	t.Parallel()
	n, _ := mapService(&corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Namespace: "shop", Name: "api", Labels: map[string]string{"app": "api"},
		Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{secret stuff}"},
	}})

	if n.ID != "Service/shop/api" || n.Kind != KindService || n.Namespace != "shop" || n.Name != "api" {
		t.Errorf("identity = %+v", n)
	}
	if n.Parent != "Namespace/shop" {
		t.Errorf("Parent = %q, want Namespace/shop", n.Parent)
	}
	if n.Labels["app"] != "api" {
		t.Errorf("Labels = %v", n.Labels)
	}
}

func TestClusterScopedHasNoParent(t *testing.T) {
	t.Parallel()
	n, _ := mapNode(&corev1.Node{ObjectMeta: meta("", "worker-1")})
	if n.ID != "Node/worker-1" || n.Parent != "" {
		t.Errorf("node = %+v", n)
	}
}

func TestMapNode(t *testing.T) {
	t.Parallel()
	ready := corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionTrue}
	notReady := corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"}

	tests := []struct {
		name   string
		node   corev1.Node
		ready  bool
		reason string
	}{
		{"ready", corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{ready}}}, true, ""},
		{"not ready", corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{notReady}}}, false, "KubeletNotReady"},
		{"cordoned", corev1.Node{Spec: corev1.NodeSpec{Unschedulable: true}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{ready}}}, true, "SchedulingDisabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n, _ := mapNode(&tt.node)
			if n.Status.Ready != tt.ready || n.Status.Reason != tt.reason {
				t.Errorf("status = %+v, want ready=%v reason=%q", n.Status, tt.ready, tt.reason)
			}
		})
	}
}

func TestMapPodParentAndEdges(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{
		ObjectMeta: meta("shop", "api-7d9f-x2k", owner(KindReplicaSet, "api-7d9f")),
		Spec: corev1.PodSpec{
			NodeName: "worker-1",
			Volumes: []corev1.Volume{
				{VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}},
				{VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "tls"}}},
				{VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}},
				{VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
					{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "projected-cm"}}},
					{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "projected-secret"}}},
				}}}},
			},
			InitContainers: []corev1.Container{{
				EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "init-env"}}}},
			}},
			Containers: []corev1.Container{{
				EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}}, // duplicate
				Env: []corev1.EnvVar{
					{Name: "PLAIN", Value: "x"},
					{Name: "DB", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}}}},
					{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "flags"}}}},
				},
			}},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}},
		},
	}

	n, edges := mapPod(pod)
	if n.ID != "Pod/shop/api-7d9f-x2k" || n.Parent != "Node/worker-1" {
		t.Errorf("node = %+v", n)
	}
	p := "Pod/shop/api-7d9f-x2k"
	assertEdges(t, edges,
		"owns:ReplicaSet/shop/api-7d9f:"+p,
		"mounts:"+p+":ConfigMap/shop/cfg",
		"mounts:"+p+":Secret/shop/tls",
		"mounts:"+p+":PersistentVolumeClaim/shop/data",
		"mounts:"+p+":ConfigMap/shop/projected-cm",
		"mounts:"+p+":Secret/shop/projected-secret",
		"mounts:"+p+":Secret/shop/init-env",
		"mounts:"+p+":Secret/shop/db",
		"mounts:"+p+":ConfigMap/shop/flags",
		"mounts:"+p+":Secret/shop/registry",
	)
}

func TestUnscheduledPodIsGroupedByNamespace(t *testing.T) {
	t.Parallel()
	n, _ := mapPod(&corev1.Pod{ObjectMeta: meta("shop", "pending")})
	if n.Parent != "Namespace/shop" {
		t.Errorf("Parent = %q, want Namespace/shop", n.Parent)
	}
}

func TestPodStatus(t *testing.T) {
	t.Parallel()
	waiting := func(reason string) corev1.ContainerState {
		return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}
	}
	terminated := func(reason string) corev1.ContainerState {
		return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason}}
	}
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	readyCond := []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}

	tests := []struct {
		name     string
		pod      corev1.Pod
		ready    bool
		reason   string
		restarts int32
	}{
		{
			name: "healthy",
			pod: corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodRunning, Conditions: readyCond,
				ContainerStatuses: []corev1.ContainerStatus{{State: running}},
			}},
			ready: true,
		},
		{
			name: "crash loop wins over creating",
			pod: corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{State: waiting("ContainerCreating")},
				{State: waiting("CrashLoopBackOff"), RestartCount: 4},
			}}},
			reason: "CrashLoopBackOff", restarts: 4,
		},
		{
			name: "creating only",
			pod: corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{State: waiting("ContainerCreating")},
			}}},
			reason: "ContainerCreating",
		},
		{
			name: "oom killed",
			pod: corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{State: terminated("OOMKilled"), RestartCount: 1},
			}}},
			reason: "OOMKilled", restarts: 1,
		},
		{
			name: "recovered after oom",
			pod: corev1.Pod{Status: corev1.PodStatus{Conditions: readyCond, ContainerStatuses: []corev1.ContainerStatus{
				{State: running, RestartCount: 2, LastTerminationState: terminated("OOMKilled")},
			}}},
			ready: true, reason: "OOMKilled", restarts: 2,
		},
		{
			name:   "evicted",
			pod:    corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}},
			reason: "Evicted",
		},
		{
			name: "terminating wins",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{}},
				Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{State: waiting("CrashLoopBackOff")}}},
			},
			reason: "Terminating",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := podStatus(&tt.pod)
			if s.Ready != tt.ready || s.Reason != tt.reason || s.Restarts != tt.restarts {
				t.Errorf("status = %+v, want ready=%v reason=%q restarts=%d", s, tt.ready, tt.reason, tt.restarts)
			}
		})
	}
}

func TestWorkloadStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mapFn   func() (graph.Node, []graph.Edge)
		ready   bool
		desired int32
	}{
		{"deployment rolled out", func() (graph.Node, []graph.Edge) {
			return mapDeployment(&appsv1.Deployment{
				Spec:   appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
				Status: appsv1.DeploymentStatus{ReadyReplicas: 3, UpdatedReplicas: 3},
			})
		}, true, 3},
		{"deployment mid-rollout", func() (graph.Node, []graph.Edge) {
			return mapDeployment(&appsv1.Deployment{
				Spec:   appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
				Status: appsv1.DeploymentStatus{ReadyReplicas: 3, UpdatedReplicas: 1},
			})
		}, false, 3},
		{"replicas default to 1", func() (graph.Node, []graph.Edge) {
			return mapStatefulSet(&appsv1.StatefulSet{Status: appsv1.StatefulSetStatus{ReadyReplicas: 1, UpdatedReplicas: 1}})
		}, true, 1},
		{"scaled to zero", func() (graph.Node, []graph.Edge) {
			return mapReplicaSet(&appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Replicas: ptr.To[int32](0)}})
		}, true, 0},
		{"daemonset uses scheduled count", func() (graph.Node, []graph.Edge) {
			return mapDaemonSet(&appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 4, NumberReady: 3, UpdatedNumberScheduled: 4}})
		}, false, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n, _ := tt.mapFn()
			if n.Status.Ready != tt.ready || n.Status.Replicas == nil || n.Status.Replicas.Desired != tt.desired {
				t.Errorf("status = %+v (replicas %+v), want ready=%v desired=%d", n.Status, n.Status.Replicas, tt.ready, tt.desired)
			}
		})
	}
}

func TestDeploymentProgressDeadline(t *testing.T) {
	t.Parallel()
	n, _ := mapDeployment(&appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"},
	}}})
	if n.Status.Reason != "ProgressDeadlineExceeded" {
		t.Errorf("Reason = %q", n.Status.Reason)
	}
}

func TestMapJob(t *testing.T) {
	t.Parallel()
	cond := func(typ batchv1.JobConditionType, status corev1.ConditionStatus, reason string) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: meta("ops", "backup", owner("CronJob", "nightly")),
			Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: typ, Status: status, Reason: reason}}},
		}
	}
	tests := []struct {
		name  string
		job   *batchv1.Job
		phase string
		ready bool
	}{
		{"running", &batchv1.Job{}, "Running", false},
		{"complete", cond(batchv1.JobComplete, corev1.ConditionTrue, ""), "Complete", true},
		{"failed", cond(batchv1.JobFailed, corev1.ConditionTrue, "BackoffLimitExceeded"), "Failed", false},
		{"condition not true", cond(batchv1.JobFailed, corev1.ConditionFalse, ""), "Running", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n, _ := mapJob(tt.job)
			if n.Status.Phase != tt.phase || n.Status.Ready != tt.ready {
				t.Errorf("status = %+v, want phase=%s ready=%v", n.Status, tt.phase, tt.ready)
			}
		})
	}

	_, edges := mapJob(cond(batchv1.JobComplete, corev1.ConditionTrue, ""))
	assertEdges(t, edges, "owns:CronJob/ops/nightly:Job/ops/backup")
}

func TestMapIngress(t *testing.T) {
	t.Parallel()
	backend := func(svc string) networkingv1.IngressBackend {
		return networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svc}}
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: meta("shop", "web"),
		Spec: networkingv1.IngressSpec{
			DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "fallback"}},
			Rules: []networkingv1.IngressRule{
				{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
					{Path: "/api", Backend: backend("api")},
					{Path: "/v2", Backend: backend("api")}, // duplicate
					{Path: "/static", Backend: networkingv1.IngressBackend{Resource: &corev1.TypedLocalObjectReference{Kind: "Bucket", Name: "b"}}},
				}}}},
				{Host: "no-http.example.com"},
			},
		},
	}
	n, edges := mapIngress(ing)
	if n.Status.Ready {
		t.Error("ingress without a load balancer address should not be ready")
	}
	assertEdges(t, edges, "routes:Ingress/shop/web:Service/shop/fallback", "routes:Ingress/shop/web:Service/shop/api")
}

func TestMapPVCAndNamespace(t *testing.T) {
	t.Parallel()
	pvc, _ := mapPVC(&corev1.PersistentVolumeClaim{Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}})
	if pvc.Status.Ready || pvc.Status.Phase != "Pending" {
		t.Errorf("pvc status = %+v", pvc.Status)
	}
	ns, _ := mapNamespace(&corev1.Namespace{Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}})
	if ns.Status.Ready || ns.Status.Phase != "Terminating" {
		t.Errorf("namespace status = %+v", ns.Status)
	}
}

func TestMapMetadata(t *testing.T) {
	t.Parallel()
	n, edges := mapMetadata(KindSecret)(&metav1.PartialObjectMetadata{ObjectMeta: meta("shop", "tls", owner("Certificate", "web"))})
	if n.ID != "Secret/shop/tls" || n.Kind != KindSecret || !n.Status.Ready {
		t.Errorf("node = %+v", n)
	}
	assertEdges(t, edges, "owns:Certificate/shop/web:Secret/shop/tls")
}

func TestOwnerEdgesToClusterScopedOwner(t *testing.T) {
	t.Parallel()
	_, edges := mapPod(&corev1.Pod{ObjectMeta: meta("kube-system", "etcd-node1", owner(KindNode, "node1"))})
	assertEdges(t, edges, "owns:Node/node1:Pod/kube-system/etcd-node1")
}

func TestCordonedNotReadyNodeKeepsNotReadyReason(t *testing.T) {
	t.Parallel()
	n, _ := mapNode(&corev1.Node{
		Spec: corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady"},
		}},
	})
	if n.Status.Reason != "KubeletNotReady" {
		t.Errorf("Reason = %q, want KubeletNotReady", n.Status.Reason)
	}
}

func TestInitContainerStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		init     corev1.ContainerStatus
		reason   string
		restarts int32
	}{
		{
			name:   "crash looping",
			init:   corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, RestartCount: 3},
			reason: "Init:CrashLoopBackOff", restarts: 3,
		},
		{
			name:   "failed",
			init:   corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}},
			reason: "Init:Error",
		},
		{
			name: "completed successfully",
			init: corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}},
		},
		{
			name: "still starting",
			init: corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := podStatus(&corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{tt.init}}})
			if s.Reason != tt.reason || s.Restarts != tt.restarts {
				t.Errorf("status = %+v, want reason=%q restarts=%d", s, tt.reason, tt.restarts)
			}
		})
	}
}
