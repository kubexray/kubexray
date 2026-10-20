package cluster

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"

	"github.com/kubexray/kubexray/internal/graph"
)

type testCluster struct {
	client  *kubefake.Clientset
	graph   *graph.Graph
	watcher *Watcher
}

func startWatcher(t *testing.T, typed []runtime.Object, metaObjs ...runtime.Object) *testCluster {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := kubefake.NewClientset(typed...)
	g := graph.New()
	w := New(client, metadatafake.NewSimpleMetadataClient(scheme, metaObjs...), g, slog.New(slog.DiscardHandler))
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return &testCluster{client: client, graph: g, watcher: w}
}

// eventually flushes the graph until check passes on the snapshot, since
// informer events are delivered asynchronously.
func (c *testCluster) eventually(t *testing.T, what string, check func(nodes map[string]graph.Node, edges []string) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.graph.Flush()
		snap := c.graph.Snapshot()
		nodes := map[string]graph.Node{}
		for _, n := range snap.Nodes {
			nodes[n.ID] = n
		}
		edges := make([]string, 0, len(snap.Edges))
		for _, e := range snap.Edges {
			edges = append(edges, e.ID)
		}
		if check(nodes, edges) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\nnodes: %v\nedges: %v", what, slices.Sorted(maps.Keys(nodes)), edges)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func hasEdge(edges []string, id string) bool { return slices.Contains(edges, id) }

func metaObj(kind, ns, name string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: kind},
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
	}
}

func apiPod(name string, podLabels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "shop", Name: name, Labels: podLabels,
			OwnerReferences: []metav1.OwnerReference{{Kind: KindReplicaSet, Name: "api-1"}},
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-1",
			Volumes: []corev1.Volume{
				{VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}},
				{VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "tls"}}},
			},
		},
	}
}

func apiService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "api"}},
	}
}

func TestInitialSyncBuildsGraph(t *testing.T) {
	t.Parallel()
	c := startWatcher(t,
		[]runtime.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}},
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
				Namespace: "shop", Name: "api-1",
				OwnerReferences: []metav1.OwnerReference{{Kind: KindDeployment, Name: "api"}},
			}},
			apiPod("api-1-a", map[string]string{"app": "api"}),
			apiService(),
			&networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
				Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{Name: "api"},
				}},
			},
		},
		metaObj(KindConfigMap, "shop", "cfg"),
		metaObj(KindSecret, "shop", "tls"),
	)

	const pod = "Pod/shop/api-1-a"
	wantNodes := []string{
		"Namespace/shop", "Node/worker-1", "Deployment/shop/api", "ReplicaSet/shop/api-1", pod,
		"Service/shop/api", "Ingress/shop/web", "ConfigMap/shop/cfg", "Secret/shop/tls",
	}
	wantEdges := []string{
		"owns:Deployment/shop/api:ReplicaSet/shop/api-1",
		"owns:ReplicaSet/shop/api-1:" + pod,
		"mounts:" + pod + ":ConfigMap/shop/cfg",
		"mounts:" + pod + ":Secret/shop/tls",
		"selects:Service/shop/api:" + pod,
		"routes:Ingress/shop/web:Service/shop/api",
	}
	c.eventually(t, "full graph", func(nodes map[string]graph.Node, edges []string) bool {
		for _, id := range wantNodes {
			if _, ok := nodes[id]; !ok {
				return false
			}
		}
		for _, id := range wantEdges {
			if !hasEdge(edges, id) {
				return false
			}
		}
		return nodes[pod].Parent == "Node/worker-1"
	})
}

func TestPodLabelChangeUpdatesSelection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := startWatcher(t, []runtime.Object{apiPod("p", map[string]string{"app": "api"}), apiService()})
	const edge = "selects:Service/shop/api:Pod/shop/p"

	c.eventually(t, "pod selected", func(_ map[string]graph.Node, edges []string) bool { return hasEdge(edges, edge) })

	if _, err := c.client.CoreV1().Pods("shop").Update(ctx, apiPod("p", map[string]string{"app": "other"}), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.eventually(t, "pod deselected", func(_ map[string]graph.Node, edges []string) bool { return !hasEdge(edges, edge) })

	if _, err := c.client.CoreV1().Pods("shop").Update(ctx, apiPod("p", map[string]string{"app": "api"}), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.eventually(t, "pod reselected", func(_ map[string]graph.Node, edges []string) bool { return hasEdge(edges, edge) })
}

func TestServiceCreatedAfterPods(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := startWatcher(t, []runtime.Object{
		apiPod("a", map[string]string{"app": "api"}),
		apiPod("b", map[string]string{"app": "api"}),
		apiPod("c", map[string]string{"app": "web"}),
	})
	c.eventually(t, "pods", func(nodes map[string]graph.Node, _ []string) bool { return len(nodes) == 3 })

	if _, err := c.client.CoreV1().Services("shop").Create(ctx, apiService(), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.eventually(t, "selector edges", func(_ map[string]graph.Node, edges []string) bool {
		return hasEdge(edges, "selects:Service/shop/api:Pod/shop/a") &&
			hasEdge(edges, "selects:Service/shop/api:Pod/shop/b") &&
			!hasEdge(edges, "selects:Service/shop/api:Pod/shop/c")
	})

	if err := c.client.CoreV1().Services("shop").Delete(ctx, "api", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	c.eventually(t, "service and its edges removed", func(nodes map[string]graph.Node, edges []string) bool {
		_, ok := nodes["Service/shop/api"]
		return !ok && len(edges) == 0
	})
}

func TestPodDeletionCleansUpEdges(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := startWatcher(t, []runtime.Object{apiPod("p", map[string]string{"app": "api"}), apiService()})
	c.eventually(t, "pod selected", func(_ map[string]graph.Node, edges []string) bool {
		return hasEdge(edges, "selects:Service/shop/api:Pod/shop/p")
	})

	if err := c.client.CoreV1().Pods("shop").Delete(ctx, "p", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	c.eventually(t, "pod removed", func(nodes map[string]graph.Node, edges []string) bool {
		_, ok := nodes["Pod/shop/p"]
		return !ok && len(edges) == 0
	})

	// No edge bookkeeping may be left behind for a deleted pod, or pod churn
	// would leak memory.
	c.watcher.mu.Lock()
	defer c.watcher.mu.Unlock()
	if len(c.watcher.owned) != 0 {
		t.Errorf("edge bookkeeping left after pod deletion: %v", c.watcher.owned)
	}
}

func TestOwnerReferenceChangeReplacesEdges(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	rs := func(owner string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Namespace: "shop", Name: "rs",
			OwnerReferences: []metav1.OwnerReference{{Kind: KindDeployment, Name: owner}},
		}}
	}
	c := startWatcher(t, []runtime.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "old"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "new"}},
		rs("old"),
	})
	c.eventually(t, "old owner edge", func(_ map[string]graph.Node, edges []string) bool {
		return hasEdge(edges, "owns:Deployment/shop/old:ReplicaSet/shop/rs")
	})

	if _, err := c.client.AppsV1().ReplicaSets("shop").Update(ctx, rs("new"), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.eventually(t, "new owner edge only", func(_ map[string]graph.Node, edges []string) bool {
		return hasEdge(edges, "owns:Deployment/shop/new:ReplicaSet/shop/rs") &&
			!hasEdge(edges, "owns:Deployment/shop/old:ReplicaSet/shop/rs")
	})
}

func TestStripObject(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:          "p",
		Labels:        map[string]string{"app": "api"},
		Annotations:   map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"kind":"Secret","data":{"password":"aHVudGVyMg=="}}`},
		ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
	}}
	out, err := stripObject(pod)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*corev1.Pod)
	if got.Annotations != nil || got.ManagedFields != nil {
		t.Errorf("annotations/managed fields not stripped: %+v", got.ObjectMeta)
	}
	if got.Labels["app"] != "api" {
		t.Error("labels must be kept")
	}
}
