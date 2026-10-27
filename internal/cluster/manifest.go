package cluster

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/redact"
)

// DefaultResources are the kinds kubexray keeps a manifest history of
// besides the ones it maps (ADR-0018). Each is watched only if kubexray may
// list it.
var DefaultResources = []schema.GroupVersionResource{
	{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
	{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"},
	{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
	{Group: "batch", Version: "v1", Resource: "cronjobs"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
	{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
	{Group: "", Version: "v1", Resource: "limitranges"},
	{Group: "", Version: "v1", Resource: "serviceaccounts"},
	{Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"},
	{Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses"},
	// Admission: a webhook or policy that starts refusing pods.
	{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"},
	{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"},
	{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies"},
	{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings"},
	// Gateway API, where it's installed.
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "grpcroutes"},
}

// ParseResource reads a resource as "group/version/resource", or
// "version/resource" for the core group: "argoproj.io/v1alpha1/applications".
// Secrets are refused: kubexray never reads them.
func ParseResource(s string) (schema.GroupVersionResource, error) {
	parts := strings.Split(s, "/")
	var gvr schema.GroupVersionResource
	switch len(parts) {
	case 2:
		gvr = schema.GroupVersionResource{Version: parts[0], Resource: parts[1]}
	case 3:
		gvr = schema.GroupVersionResource{Group: parts[0], Version: parts[1], Resource: parts[2]}
	default:
		return gvr, fmt.Errorf("resource %q: want group/version/resource, e.g. argoproj.io/v1alpha1/applications", s)
	}
	if slices.Contains(parts, "") {
		return gvr, fmt.Errorf("resource %q: want group/version/resource, e.g. argoproj.io/v1alpha1/applications", s)
	}
	if gvr.Group == "" && gvr.Resource == "secrets" {
		return gvr, fmt.Errorf("resource %q: kubexray never reads Secrets", s)
	}
	return gvr, nil
}

// WithManifests keeps a manifest history of resources through dyn: the
// kinds kubexray doesn't otherwise map, such as HorizontalPodAutoscalers,
// RBAC and custom resources (ADR-0018).
func (w *Watcher) WithManifests(dyn dynamic.Interface, resources []schema.GroupVersionResource) *Watcher {
	w.dyn, w.resources = dyn, resources
	return w
}

// watchManifests registers an informer for each resource kubexray may
// list. One it can't list, or that the cluster doesn't have, is skipped and
// logged: a missing CRD or permission must not stop kubexray.
func (w *Watcher) watchManifests(ctx context.Context, track func(cache.ResourceEventHandlerRegistration, error)) {
	if w.dyn == nil {
		return
	}
	w.dynamic = dynamicinformer.NewDynamicSharedInformerFactory(w.dyn, 0)
	for _, gvr := range w.resources {
		list, err := w.dyn.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
		switch {
		case apierrors.IsForbidden(err):
			w.log.Info("not keeping a history of a resource kubexray may not list", "resource", gvr.String())
			continue
		case apierrors.IsNotFound(err):
			w.log.Debug("resource not in this cluster", "resource", gvr.String())
			continue
		case err != nil:
			w.log.Warn("listing a resource", "resource", gvr.String(), "err", err)
			continue
		}
		kind := strings.TrimSuffix(list.GetKind(), "List")
		if kind == "" || kind == KindSecret {
			continue
		}
		track(register(w, kind, w.dynamic.ForResource(gvr).Informer(), mapManifest(kind)))
	}
}

// mapManifest maps any object to a node with its manifest. Objects in
// system namespaces and the cluster's own RBAC ("system:…") are mapped
// without one: they change with every upgrade.
func mapManifest(kind string) func(*unstructured.Unstructured) (graph.Node, []graph.Edge) {
	return func(u *unstructured.Unstructured) (graph.Node, []graph.Edge) {
		n := baseNode(kind, u)
		n.Status.Ready = true
		if !systemNamespaces[u.GetNamespace()] && !strings.HasPrefix(u.GetName(), "system:") {
			n.Manifest = manifest(u.Object)
		}
		return n, ownerEdges(n.ID, u)
	}
}

// manifestOf is the manifest of a typed object, for the kinds kubexray maps
// itself, such as Ingresses and Services.
func manifestOf(obj runtime.Object) map[string]graph.Value {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil
	}
	return manifest(m)
}

// covered are the parts of a workload its Spec already records (ADR-0015),
// with their own diff lines, or that have their own entry (replicas).
var covered = []string{
	"spec.replicas",
	"spec.template.spec.containers",
	"spec.template.spec.initContainers",
	"spec.template.spec.volumes",
	"spec.template.spec.serviceAccount",
	"spec.template.spec.serviceAccountName",
}

// workloadManifest is a workload's manifest without what its Spec covers:
// the rollout strategy, scheduling, security context, template annotations
// and the rest.
func workloadManifest(obj runtime.Object) map[string]graph.Value {
	m := manifestOf(obj)
	for path := range m {
		for _, c := range covered {
			if rest, ok := strings.CutPrefix(path, c); ok && (rest == "" || rest[0] == '.' || rest[0] == '[') {
				delete(m, path)
				break
			}
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// manifest flattens an object's desired state into fields by path, each
// masked and capped like any value (ADR-0017). Status, and metadata other
// than labels, are left out: they change without anyone changing the
// object.
func manifest(obj map[string]any) map[string]graph.Value {
	f := flattener{out: map[string]graph.Value{}, budget: maxData}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		switch k {
		case "apiVersion", "kind", "status":
		case "metadata":
			if md, ok := obj[k].(map[string]any); ok {
				if l, ok := md["labels"]; ok {
					f.walk("metadata.labels", l, false)
				}
			}
		default:
			f.walk(k, obj[k], redact.SensitiveName(k))
		}
	}
	if len(f.out) == 0 {
		return nil
	}
	return f.out
}

// maxFields caps how many fields of one object are kept.
const maxFields = 512

type flattener struct {
	out    map[string]graph.Value
	budget int
}

var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// walk adds the fields under v at path. sensitive is set once any key or
// item name on the path looks like a credential: env[DB_PASSWORD].value
// is hidden like DB_PASSWORD.
func (f *flattener) walk(path string, v any, sensitive bool) {
	if len(f.out) >= maxFields {
		return
	}
	switch v := v.(type) {
	case map[string]any:
		if len(v) == 0 {
			f.leaf(path, "{}", false)
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			p := path + "." + k
			if !plainKey.MatchString(k) || len(k) > maxValueLen {
				p = path + "[" + strconv.Quote(capped(k)) + "]"
			}
			f.walk(p, v[k], sensitive || redact.SensitiveName(k))
		}
	case []any:
		if len(v) == 0 {
			f.leaf(path, "[]", false)
		}
		names := itemNames(v)
		for i, x := range v {
			if names != nil {
				f.walk(path+"["+names[i]+"]", x, sensitive || redact.SensitiveName(names[i]))
			} else {
				f.walk(path+"["+strconv.Itoa(i)+"]", x, sensitive)
			}
		}
	case nil:
		f.leaf(path, "null", false)
	case string:
		f.leaf(path, v, sensitive)
	default:
		// Numbers and booleans are never credentials, whatever their name:
		// automountServiceAccountToken: false.
		f.leaf(path, fmt.Sprint(v), false)
	}
}

func (f *flattener) leaf(path, s string, sensitive bool) {
	val := graph.Value{Hash: fingerprint(s)}
	switch {
	case sensitive:
		val.Masked = "name"
	case f.budget-len(s) < 0:
		val.Masked = "size"
	default:
		f.budget -= len(s)
		val.Text, val.Masked = literal("", s)
	}
	f.out[path] = val
}

// itemNames keys a list by its items' names when every item has a distinct
// one, as containers, env and ports do, so inserting an item changes one
// line rather than every line after it.
func itemNames(items []any) []string {
	names := make([]string, len(items))
	seen := map[string]bool{}
	for i, x := range items {
		m, ok := x.(map[string]any)
		if !ok {
			return nil
		}
		name, ok := m["name"].(string)
		if !ok || name == "" || len(name) > maxValueLen || seen[name] || !plainKey.MatchString(name) {
			return nil
		}
		seen[name], names[i] = true, name
	}
	return names
}
