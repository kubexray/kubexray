package cluster

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubexray/kubexray/internal/graph"
)

var base = time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)

// entry is an Update by manager, offset from base; sub "status" marks bookkeeping.
func entry(manager, sub string, offset time.Duration) metav1.ManagedFieldsEntry {
	at := metav1.NewTime(base.Add(offset))
	return metav1.ManagedFieldsEntry{Manager: manager, Operation: update, Subresource: sub, Time: &at}
}

func withFields(fields ...metav1.ManagedFieldsEntry) *metav1.ObjectMeta {
	return &metav1.ObjectMeta{ManagedFields: fields}
}

const update = metav1.ManagedFieldsOperationUpdate

func TestLastChange(t *testing.T) {
	t.Parallel()
	untimed := metav1.ManagedFieldsEntry{Manager: "kubectl", Operation: update, Subresource: "scale"}
	tests := []struct {
		name string
		meta *metav1.ObjectMeta
		want *graph.Change
	}{
		{"no managed fields", withFields(), nil},
		{
			name: "latest write wins",
			meta: withFields(entry("kubectl-client-side-apply", "", 0), entry("kubectl-patch", "", time.Minute)),
			want: &graph.Change{At: base.Add(time.Minute), By: "kubectl-patch", Tool: "kubectl", Via: "patch"},
		},
		{
			name: "status writes are bookkeeping",
			meta: withFields(entry("kubectl", "", 0), entry("kube-controller-manager", "status", time.Minute)),
			want: &graph.Change{At: base, By: "kubectl", Tool: "kubectl"},
		},
		{
			// The deployment controller following up on a human change must not hide it.
			name: "people beat later controller writes",
			meta: withFields(entry("helm", "", 0), entry("kube-controller-manager", "", time.Minute)),
			want: &graph.Change{At: base, By: "helm", Tool: "helm"},
		},
		{
			name: "controller when nobody else wrote it",
			meta: withFields(entry("kube-controller-manager", "", 0)),
			want: &graph.Change{At: base, By: "kube-controller-manager", Tool: "controller"},
		},
		{
			name: "untimed entries are ignored",
			meta: withFields(entry("kubectl-client-side-apply", "", 0), untimed),
			want: &graph.Change{At: base, By: "kubectl-client-side-apply", Tool: "kubectl", Via: "apply"},
		},
		{"only untimed entries", withFields(untimed), nil},
		{
			name: "long manager names are truncated",
			meta: withFields(entry(strings.Repeat("x", 100), "", 0)),
			want: &graph.Change{At: base, By: strings.Repeat("x", 64), Tool: "other"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := lastChange(tt.meta)
			if (got == nil) != (tt.want == nil) || (got != nil && !got.Equal(*tt.want)) {
				t.Errorf("lastChange = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestToolOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		manager   string
		op        metav1.ManagedFieldsOperationType
		tool, via string
	}{
		{"kubectl", update, "kubectl", ""},
		{"kubectl", metav1.ManagedFieldsOperationApply, "kubectl", "apply"},
		{"kubectl-client-side-apply", update, "kubectl", "apply"},
		{"kubectl-patch", update, "kubectl", "patch"},
		{"kubectl-edit", update, "kubectl", "edit"},
		{"helm", update, "helm", ""},
		{"argocd-controller", update, "argocd", ""},
		{"argocd-application-controller", update, "argocd", ""},
		{"kustomize-controller", update, "flux", ""},
		{"helm-controller", update, "flux", ""}, // Flux's, not Helm's
		{"kube-controller-manager", update, "controller", ""},
		{"kubelet", update, "controller", ""},
		{"terraform-provider-kubernetes", update, "other", ""},
		{"Go-http-client", update, "other", ""},
	}
	for _, tt := range tests {
		if tool, via := toolOf(tt.manager, tt.op); tool != tt.tool || via != tt.via {
			t.Errorf("toolOf(%q, %s) = %q, %q; want %q, %q", tt.manager, tt.op, tool, via, tt.tool, tt.via)
		}
	}
}

func TestOriginOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		labels      map[string]string
		annotations map[string]string
		want        *graph.Origin
	}{
		{"none", nil, nil, nil},
		{"helm annotation", nil, map[string]string{annotationHelmRelease: "shop"}, &graph.Origin{Tool: "helm", Name: "shop"}},
		{"helm label", map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "shop"}, nil, &graph.Origin{Tool: "helm", Name: "shop"}},
		{"argo tracking id", nil, map[string]string{annotationArgoTrack: "shop-prod:apps/Deployment:shop/api"}, &graph.Origin{Tool: "argocd", Name: "shop-prod"}},
		{"argo label", map[string]string{"argocd.argoproj.io/instance": "shop-prod"}, nil, &graph.Origin{Tool: "argocd", Name: "shop-prod"}},
		{"flux kustomization", map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}, nil, &graph.Origin{Tool: "flux", Name: "apps"}},
		{
			name:        "flux helm release beats helm annotations",
			labels:      map[string]string{"helm.toolkit.fluxcd.io/name": "shop"},
			annotations: map[string]string{annotationHelmRelease: "shop"},
			want:        &graph.Origin{Tool: "flux", Name: "shop"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := originOf(&metav1.ObjectMeta{Labels: tt.labels, Annotations: tt.annotations})
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("originOf = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDeploymentSpec(t *testing.T) {
	t.Parallel()
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api", Generation: 7, Annotations: map[string]string{annotationRevision: "14"}},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "migrate", Image: "shop/migrate:3"}},
			Containers: []corev1.Container{{
				Name: "api", Image: "nginx:1.27-alpine",
				Command: []string{"sh", "-c", "secret-token-in-a-command"},
				Env:     []corev1.EnvVar{{Name: "DB_PASSWORD", Value: "hunter2"}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
				},
			}},
		}}},
	}
	n, _ := mapDeployment(d)
	want := graph.Spec{Generation: 7, Revision: "14", Containers: []graph.Container{
		{Name: "migrate", Image: "shop/migrate:3", Init: true},
		{Name: "api", Image: "nginx:1.27-alpine", Requests: graph.Resources{CPU: "100m", Memory: "128Mi"}, Limits: graph.Resources{Memory: "256Mi"}},
	}}
	if n.Spec == nil || !n.Spec.Equal(want) {
		t.Errorf("spec = %+v\nwant   %+v", n.Spec, want)
	}
}

func TestEveryWorkloadHasASpec(t *testing.T) {
	t.Parallel()
	tmpl := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}}}
	nodes := map[string]graph.Node{}
	nodes["Pod"], _ = mapPod(&corev1.Pod{Spec: tmpl.Spec})
	nodes["ReplicaSet"], _ = mapReplicaSet(&appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Template: tmpl}})
	nodes["StatefulSet"], _ = mapStatefulSet(&appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: tmpl}})
	nodes["DaemonSet"], _ = mapDaemonSet(&appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: tmpl}})
	for kind, n := range nodes {
		if n.Spec == nil || len(n.Spec.Containers) != 1 || n.Spec.Containers[0].Image != "img" {
			t.Errorf("%s spec = %+v", kind, n.Spec)
		}
	}
}

func TestChangeAndOriginOnEveryNode(t *testing.T) {
	t.Parallel()
	meta := metav1.ObjectMeta{
		Namespace: "shop", Name: "x",
		Annotations:   map[string]string{annotationHelmRelease: "shop"},
		ManagedFields: []metav1.ManagedFieldsEntry{entry("helm", "", 0)},
	}
	svc, _ := mapService(&corev1.Service{ObjectMeta: meta})
	cm, _ := mapMetadata(KindConfigMap)(&metav1.PartialObjectMetadata{ObjectMeta: meta})
	for _, n := range []graph.Node{svc, cm} {
		if n.Change == nil || n.Change.Tool != "helm" || n.Origin == nil || n.Origin.Name != "shop" {
			t.Errorf("%s: change = %+v, origin = %+v", n.Kind, n.Change, n.Origin)
		}
	}
}
