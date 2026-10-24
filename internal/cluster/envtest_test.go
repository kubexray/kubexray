//go:build envtest

// Watcher tests against a real API server (envtest: etcd and kube-apiserver,
// no controllers), for what fakes can't show: managedFields as the API
// server writes them, metadata-only informers, field selectors. Run with
// `make test-envtest`.
package cluster

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/kubexray/kubexray/internal/graph"
)

// apiServer starts etcd and kube-apiserver for the test.
func apiServer(t *testing.T) *rest.Config {
	t.Helper()
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting envtest (is KUBEBUILDER_ASSETS set? use make test-envtest): %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	return cfg
}

// as returns a client that writes as the given field manager, the way kubectl
// (kubectl-patch, kubectl-client-side-apply) or Helm identify themselves.
func as(t *testing.T, cfg *rest.Config, manager string) *kubernetes.Clientset {
	t.Helper()
	c := rest.CopyConfig(cfg)
	c.UserAgent = manager
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestWatcherAgainstAnAPIServer(t *testing.T) {
	cfg := apiServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	helm, kubectl := as(t, cfg, "helm"), as(t, cfg, "kubectl-patch")
	create := metav1.CreateOptions{FieldManager: "helm"}

	if _, err := helm.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}, create); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "cfg"}, Data: map[string]string{"LOG_LEVEL": "info", "PASSWORD": "never-in-the-graph"}}
	if _, err := helm.CoreV1().ConfigMaps("shop").Create(ctx, cm, create); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app": "api"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "nginx:1.27"}}},
			},
		},
	}
	if _, err := helm.AppsV1().Deployments("shop").Create(ctx, dep, create); err != nil {
		t.Fatal(err)
	}
	if _, err := helm.CoreV1().Services("shop").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec:       corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Port: 80}}},
	}, create); err != nil {
		t.Fatal(err)
	}
	// envtest runs no controllers: the pod a ReplicaSet would create is made
	// by hand, mounting the ConfigMap.
	if _, err := helm.CoreV1().Pods("shop").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-1", Labels: labels},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "api", Image: "nginx:1.27", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}}}},
		},
	}, create); err != nil {
		t.Fatal(err)
	}

	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	w := New(as(t, cfg, "kubexray"), meta, g, slog.New(slog.DiscardHandler))
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := &testCluster{graph: g, watcher: w}
	c.eventually(t, "the shop's objects and edges", func(nodes map[string]graph.Node, edges []string) bool {
		_, svc := nodes["Service/shop/api"]
		return svc && hasEdge(edges, "selects:Service/shop/api:Pod/shop/api-1") && hasEdge(edges, "mounts:Pod/shop/api-1:ConfigMap/shop/cfg")
	})

	// Someone edits the ConfigMap with kubectl after Helm created it: the
	// API server's managedFields say who, and the graph has it. Their times
	// have second precision, so the edit comes a second later, as any real
	// edit does.
	time.Sleep(1100 * time.Millisecond)
	patch := []byte(`{"data":{"LOG_LEVEL":"verbose"}}`)
	if _, err := kubectl.CoreV1().ConfigMaps("shop").Patch(ctx, "cfg", types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "kubectl-patch"}); err != nil {
		t.Fatal(err)
	}
	c.eventually(t, "the edit attributed to kubectl patch", func(nodes map[string]graph.Node, _ []string) bool {
		ch := nodes["ConfigMap/shop/cfg"].Change
		return ch != nil && ch.By == "kubectl-patch" && ch.Tool == "kubectl" && ch.Via == "patch"
	})
	nodes := map[string]graph.Node{}
	for _, n := range g.Snapshot().Nodes {
		nodes[n.ID] = n
	}
	if ch := nodes["Deployment/shop/api"].Change; ch == nil || ch.Tool != "helm" {
		t.Errorf("Deployment change = %+v, want Helm", ch)
	}
	// ConfigMaps come through the metadata-only informer: contents never
	// reach kubexray. The model has no field for them; the attribution above
	// proves the object itself arrived.
	if spec := nodes["Deployment/shop/api"].Spec; spec == nil || len(spec.Containers) == 0 || spec.Containers[0].Image != "nginx:1.27" || spec.Labels["app"] != "api" {
		t.Errorf("Deployment spec = %+v", spec)
	}
	raw, err := json.Marshal(g.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "never-in-the-graph") {
		t.Error("a ConfigMap's contents reached the graph")
	}
}

func TestWithoutSecretsAgainstAnAPIServer(t *testing.T) {
	cfg := apiServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	admin := as(t, cfg, "kubectl-create")
	if _, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.CoreV1().Secrets("shop").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	w := New(as(t, cfg, "kubexray"), meta, g, slog.New(slog.DiscardHandler)).WithoutSecrets()
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Start returns once every handler has processed the initial list: what
	// isn't in the graph now wasn't listed.
	g.Flush()
	nodes := map[string]bool{}
	for _, n := range g.Snapshot().Nodes {
		nodes[n.ID] = true
	}
	if !nodes["Namespace/shop"] || nodes["Secret/shop/tls"] {
		t.Errorf("after the initial sync: namespace %v, Secret %v; want the namespace and no Secret", nodes["Namespace/shop"], nodes["Secret/shop/tls"])
	}
}
