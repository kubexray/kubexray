// Package cluster keeps a graph.Graph in sync with a Kubernetes cluster.
//
// It only reads: it lists and watches objects and never writes to the API.
// ConfigMaps and Secrets are watched as metadata only, and objects are cut
// down as they arrive (see stripObject), so neither Secret contents nor
// last-applied-configuration ever stay in memory.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/tools/cache"

	"github.com/kubexray/kubexray/internal/graph"
)

// Watcher mirrors cluster objects into a graph.
type Watcher struct {
	graph *graph.Graph
	log   *slog.Logger
	typed informers.SharedInformerFactory
	meta  metadatainformer.SharedInformerFactory

	pods     corelisters.PodLister
	services corelisters.ServiceLister

	// Informer handlers run concurrently; mu serialises graph updates so the
	// edge bookkeeping below stays consistent.
	mu sync.Mutex
	// owned records the edges each owner added, so they can be replaced or
	// removed later (ADR-0003: edges belong to whoever added them). Keys are
	// node IDs for edges derived from an object itself, and selectsKey(id)
	// for a Service's selector edges.
	owned map[string]map[string]graph.Edge
}

// New returns a Watcher that writes into g. Call Start to begin watching.
func New(client kubernetes.Interface, metaClient metadata.Interface, g *graph.Graph, log *slog.Logger) *Watcher {
	return &Watcher{
		graph: g,
		log:   log,
		typed: informers.NewSharedInformerFactory(client, 0),
		meta:  metadatainformer.NewSharedInformerFactory(metaClient, 0),
		owned: map[string]map[string]graph.Edge{},
	}
}

// Start registers informers, starts them, and blocks until their caches have
// synced. Informers stop when ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) error {
	core := w.typed.Core().V1()
	apps := w.typed.Apps().V1()
	podInformer := core.Pods()
	serviceInformer := core.Services()
	w.pods = podInformer.Lister()
	w.services = serviceInformer.Lister()

	var syncs []cache.InformerSynced
	var errs []error
	track := func(reg cache.ResourceEventHandlerRegistration, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		syncs = append(syncs, reg.HasSynced)
	}
	track(register(w, KindNamespace, core.Namespaces().Informer(), mapNamespace))
	track(register(w, KindNode, core.Nodes().Informer(), mapNode))
	track(register(w, KindPVC, core.PersistentVolumeClaims().Informer(), mapPVC))
	track(register(w, KindDeployment, apps.Deployments().Informer(), mapDeployment))
	track(register(w, KindReplicaSet, apps.ReplicaSets().Informer(), mapReplicaSet))
	track(register(w, KindStatefulSet, apps.StatefulSets().Informer(), mapStatefulSet))
	track(register(w, KindDaemonSet, apps.DaemonSets().Informer(), mapDaemonSet))
	track(register(w, KindJob, w.typed.Batch().V1().Jobs().Informer(), mapJob))
	track(register(w, KindIngress, w.typed.Networking().V1().Ingresses().Informer(), mapIngress))
	track(register(w, KindConfigMap, w.meta.ForResource(corev1.SchemeGroupVersion.WithResource("configmaps")).Informer(), mapMetadata(KindConfigMap)))
	track(register(w, KindSecret, w.meta.ForResource(corev1.SchemeGroupVersion.WithResource("secrets")).Informer(), mapMetadata(KindSecret)))
	track(handle(podInformer.Informer(), w.upsertPod, w.removePod))
	track(handle(serviceInformer.Informer(), w.upsertService, w.removeService))
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("registering informers: %w", err)
	}

	w.typed.Start(ctx.Done())
	w.meta.Start(ctx.Done())
	// Wait for every handler to process the initial list, not just for the
	// caches to fill, so the first snapshot is complete.
	if !cache.WaitForCacheSync(ctx.Done(), syncs...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errors.New("informer caches did not sync")
	}
	w.log.Info("cluster caches synced")
	return nil
}

// register wires an informer to a mapping function for objects whose node
// and edges depend only on the object itself.
func register[T metav1.Object](w *Watcher, kind string, inf cache.SharedIndexInformer, mapFn func(T) (graph.Node, []graph.Edge)) (cache.ResourceEventHandlerRegistration, error) {
	return handle(inf,
		func(_, obj T) {
			n, edges := mapFn(obj)
			w.mu.Lock()
			defer w.mu.Unlock()
			w.graph.UpsertNode(n)
			w.setEdges(n.ID, edges)
		},
		func(obj T) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.removeNode(graph.ID(kind, obj.GetNamespace(), obj.GetName()))
		},
	)
}

// handle adds event handlers that call upsert on add/update (old is the zero
// value on add) and remove on delete, and strips fields kubexray must not keep
// in memory.
func handle[T metav1.Object](inf cache.SharedIndexInformer, upsert func(old, obj T), remove func(T)) (cache.ResourceEventHandlerRegistration, error) {
	if err := inf.SetTransform(stripObject); err != nil {
		return nil, err
	}
	return inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if o, ok := obj.(T); ok {
				var zero T
				upsert(zero, o)
			}
		},
		UpdateFunc: func(oldObj, obj any) {
			old, _ := oldObj.(T)
			if o, ok := obj.(T); ok {
				upsert(old, o)
			}
		},
		DeleteFunc: func(obj any) {
			if tomb, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tomb.Obj
			}
			if o, ok := obj.(T); ok {
				remove(o)
			}
		},
	})
}

// stripObject reduces objects to what kubexray may keep in memory before they
// are cached (ADR-0005): annotations are cut down to a small allow-list, which
// drops last-applied-configuration with any Secrets embedded in it, and
// managed fields keep who wrote the object and when, without the field sets.
func stripObject(obj any) (any, error) {
	m, ok := obj.(metav1.Object)
	if !ok {
		return obj, nil
	}
	var kept map[string]string
	for _, k := range keptAnnotations {
		if v, ok := m.GetAnnotations()[k]; ok {
			if kept == nil {
				kept = map[string]string{}
			}
			kept[k] = capped(v)
		}
	}
	m.SetAnnotations(kept)

	// The object belongs to the informer, so entries are slimmed in place.
	fields := m.GetManagedFields()
	for i, e := range fields {
		fields[i] = metav1.ManagedFieldsEntry{Manager: capped(e.Manager), Operation: e.Operation, Subresource: e.Subresource, Time: e.Time}
	}
	return obj, nil
}

func (w *Watcher) removeNode(id string) {
	w.setEdges(id, nil)
	w.graph.RemoveNode(id)
}

// setEdges replaces the edges added by owner with edges.
func (w *Watcher) setEdges(owner string, edges []graph.Edge) {
	old := w.owned[owner]
	next := make(map[string]graph.Edge, len(edges))
	for _, e := range edges {
		next[e.ID] = e
	}
	for id := range old {
		if _, keep := next[id]; !keep {
			w.graph.RemoveEdge(id)
		}
	}
	for id, e := range next {
		if _, had := old[id]; !had {
			w.graph.UpsertEdge(e)
		}
	}
	if len(next) == 0 {
		delete(w.owned, owner)
	} else {
		w.owned[owner] = next
	}
}

// setEdge adds (or, if present is false, removes) a single edge owned by owner.
func (w *Watcher) setEdge(owner string, e graph.Edge, present bool) {
	edges := w.owned[owner]
	_, had := edges[e.ID]
	switch {
	case present && !had:
		if edges == nil {
			edges = map[string]graph.Edge{}
			w.owned[owner] = edges
		}
		edges[e.ID] = e
		w.graph.UpsertEdge(e)
	case !present && had:
		delete(edges, e.ID)
		if len(edges) == 0 {
			delete(w.owned, owner)
		}
		w.graph.RemoveEdge(e.ID)
	}
}

func selectsKey(serviceID string) string { return "selects:" + serviceID }

// selector returns a Service's pod selector, or nil if it selects nothing
// (services without a selector are backed by manually managed endpoints).
func selector(svc *corev1.Service) labels.Selector {
	if len(svc.Spec.Selector) == 0 {
		return nil
	}
	return labels.SelectorFromSet(svc.Spec.Selector)
}

// The pod and service handlers read the other kind's cache while holding
// w.mu. Caches are updated before their handlers run, so whichever handler
// runs last sees the latest state of both and its edges win.

func (w *Watcher) upsertPod(old, pod *corev1.Pod) {
	n, edges := mapPod(pod)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.graph.UpsertNode(n)
	w.setEdges(n.ID, edges)

	// Status updates are by far the most common pod event; selection only
	// depends on labels.
	if old != nil && maps.Equal(old.Labels, pod.Labels) {
		return
	}
	services, err := w.services.Services(pod.Namespace).List(labels.Everything())
	if err != nil {
		w.log.Error("listing services", "namespace", pod.Namespace, "err", err)
		return
	}
	podLabels := labels.Set(pod.Labels)
	for _, svc := range services {
		svcID := graph.ID(KindService, svc.Namespace, svc.Name)
		sel := selector(svc)
		w.setEdge(selectsKey(svcID), graph.NewEdge(graph.EdgeSelects, svcID, n.ID), sel != nil && sel.Matches(podLabels))
	}
}

func (w *Watcher) removePod(pod *corev1.Pod) {
	id := graph.ID(KindPod, pod.Namespace, pod.Name)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.removeNode(id)
	services, err := w.services.Services(pod.Namespace).List(labels.Everything())
	if err != nil {
		w.log.Error("listing services", "namespace", pod.Namespace, "err", err)
		return
	}
	for _, svc := range services {
		svcID := graph.ID(KindService, svc.Namespace, svc.Name)
		w.setEdge(selectsKey(svcID), graph.NewEdge(graph.EdgeSelects, svcID, id), false)
	}
}

func (w *Watcher) upsertService(_, svc *corev1.Service) {
	n, edges := mapService(svc)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.graph.UpsertNode(n)
	w.setEdges(n.ID, edges)

	var selects []graph.Edge
	if sel := selector(svc); sel != nil {
		pods, err := w.pods.Pods(svc.Namespace).List(sel)
		if err != nil {
			w.log.Error("listing pods", "namespace", svc.Namespace, "err", err)
			return // keep the current edges rather than wiping them
		}
		for _, pod := range pods {
			selects = append(selects, graph.NewEdge(graph.EdgeSelects, n.ID, graph.ID(KindPod, pod.Namespace, pod.Name)))
		}
	}
	w.setEdges(selectsKey(n.ID), selects)
}

func (w *Watcher) removeService(svc *corev1.Service) {
	id := graph.ID(KindService, svc.Namespace, svc.Name)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.setEdges(selectsKey(id), nil)
	w.removeNode(id)
}
