package cluster

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubexray/kubexray/internal/graph"
)

func hpa(maxReplicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"metadata": map[string]any{
			"namespace": "shop", "name": "checkout",
			"labels":      map[string]any{"app.kubernetes.io/name": "checkout"},
			"annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{}"},
			"uid":         "abc",
		},
		"spec": map[string]any{
			"maxReplicas":    maxReplicas,
			"minReplicas":    int64(2),
			"scaleTargetRef": map[string]any{"kind": "Deployment", "name": "checkout"},
			"metrics":        []any{map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu"}}},
		},
		"status": map[string]any{"currentReplicas": int64(3)},
	}}
}

func TestManifest(t *testing.T) {
	t.Parallel()
	m := manifest(hpa(10).Object)
	want := map[string]string{
		"spec.maxReplicas":                          "10",
		"spec.scaleTargetRef.name":                  "checkout",
		"spec.metrics[0].resource.name":             "cpu",
		`metadata.labels["app.kubernetes.io/name"]`: "checkout",
	}
	for path, text := range want {
		if m[path].Text != text || !strings.HasPrefix(m[path].Hash, "fp:") {
			t.Errorf("%s = %+v, want %q", path, m[path], text)
		}
	}
	for path := range m {
		if strings.HasPrefix(path, "status") || strings.Contains(path, "uid") || strings.Contains(path, "annotations") {
			t.Errorf("kept %s", path)
		}
	}
}

func TestManifestMasksCredentials(t *testing.T) {
	t.Parallel()
	obj := func(k string, v any) map[string]any { return map[string]any{k: v} }
	containers := []any{map[string]any{
		"name": "report",
		"env": []any{
			map[string]any{"name": "DB_PASSWORD", "value": "hunter2"},
			map[string]any{"name": "DB_URL", "value": "postgres://app:" + "s3cret@db/shop"},
			map[string]any{"name": "LOG_LEVEL", "value": "info"},
		},
	}}
	cron := obj("spec", obj("jobTemplate", obj("spec", obj("template", obj("spec", obj("containers", containers))))))
	m := manifest(cron)
	base := "spec.jobTemplate.spec.template.spec.containers[report].env"
	if v := m[base+"[DB_PASSWORD].value"]; v.Text != "" || v.Masked != "name" {
		t.Errorf("password = %+v", v)
	}
	if v := m[base+"[DB_URL].value"]; v.Text != "postgres://[redacted]@db/shop" || v.Masked != "value" {
		t.Errorf("url = %+v", v)
	}
	if v := m[base+"[LOG_LEVEL].value"]; v.Text != "info" {
		t.Errorf("log level = %+v", v)
	}
	for k, v := range graph.WithoutValues(graph.Node{Manifest: m}).Manifest {
		if v.Text != "" || v.Hash == "" {
			t.Errorf("%s without values = %+v", k, v)
		}
	}
}

func TestManifestKeysListsByName(t *testing.T) {
	t.Parallel()
	before := manifest(map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"name": "http", "port": int64(80)}}}})
	after := manifest(map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"name": "grpc", "port": int64(9000)}, map[string]any{"name": "http", "port": int64(80)}}}})
	if before["spec.ports[http].port"] != after["spec.ports[http].port"] {
		t.Errorf("inserting an item changed another: %v → %v", before, after)
	}
}

func TestParseResource(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]schema.GroupVersionResource{
		"argoproj.io/v1alpha1/applications": {Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"},
		"v1/serviceaccounts":                {Version: "v1", Resource: "serviceaccounts"},
	} {
		if got, err := ParseResource(in); err != nil || got != want {
			t.Errorf("%s = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"applications", "a/b/c/d", "/v1/x", "v1/secrets"} {
		if _, err := ParseResource(bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestWatcherKeepsManifests(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hpas := schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
	roles := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	apps := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		hpas: "HorizontalPodAutoscalerList", roles: "RoleList", apps: "ApplicationList", podMetrics: "PodMetricsList",
	}, hpa(10))
	dyn.PrependReactor("list", "roles", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(roles.GroupResource(), "", nil)
	})
	dyn.PrependReactor("list", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(apps.GroupResource(), "")
	})
	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	w := New(kubefake.NewClientset(), metadatafake.NewSimpleMetadataClient(scheme), g, slog.New(slog.DiscardHandler)).
		WithValues().WithManifests(dyn, []schema.GroupVersionResource{hpas, roles, apps})
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err) // a forbidden or missing resource doesn't stop it
	}
	c := &testCluster{graph: g, watcher: w}
	const id = "HorizontalPodAutoscaler/shop/checkout"
	c.eventually(t, "the HPA's manifest", func(nodes map[string]graph.Node, _ []string) bool {
		return nodes[id].Manifest["spec.maxReplicas"].Text == "10"
	})

	if _, err := dyn.Resource(hpas).Namespace("shop").Update(ctx, hpa(20), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.eventually(t, "the HPA's new maxReplicas", func(nodes map[string]graph.Node, _ []string) bool {
		return nodes[id].Manifest["spec.maxReplicas"].Text == "20"
	})
}

func TestWorkloadManifestLeavesOutWhatSpecCovers(t *testing.T) {
	t.Parallel()
	surge := intstr.FromString("50%")
	replicas := int32(3)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &surge}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				NodeSelector:       map[string]string{"pool": "spot"},
				ServiceAccountName: "api",
				Containers:         []corev1.Container{{Name: "api", Image: "nginx", Env: []corev1.EnvVar{{Name: "X", Value: "y"}}}},
			}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 3},
	}
	n, _ := mapDeployment(d)
	if n.Manifest["spec.strategy.rollingUpdate.maxSurge"].Text != "50%" || n.Manifest["spec.template.spec.nodeSelector.pool"].Text != "spot" {
		t.Errorf("manifest = %v", n.Manifest)
	}
	for path := range n.Manifest {
		for _, c := range covered {
			if strings.HasPrefix(path, c) {
				t.Errorf("kept %s, which the spec covers", path)
			}
		}
		if strings.HasPrefix(path, "status") {
			t.Errorf("kept %s", path)
		}
	}
}
