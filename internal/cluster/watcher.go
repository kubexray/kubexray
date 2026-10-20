// Package cluster keeps a graph.Graph in sync with a Kubernetes cluster.
//
// It only reads: it lists and watches objects and never writes to the API.
// ConfigMaps and Secrets are watched as metadata only, and annotations and
// managed fields are stripped as objects arrive, so neither Secret contents
// nor last-applied-configuration ever stay in memory.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	err := errors.Join(
		register(w, core.Namespaces().Informer(), mapNamespace),
		register(w, core.Nodes().Informer(), mapNode),
		register(w, core.PersistentVolumeClaims().Informer(), mapPVC),
		register(w, apps.Deployments().Informer(), mapDeployment),
		register(w, apps.ReplicaSets().Informer(), mapReplicaSet),
		register(w, apps.StatefulSets().Informer(), mapStatefulSet),
		register(w, apps.DaemonSets().Informer(), mapDaemonSet),
		register(w, w.typed.Batch().V1().Jobs().Informer(), mapJob),
		register(w, w.typed.Networking().V1().Ingresses().Informer(), mapIngress),
		register(w, w.meta.ForResource(corev1.SchemeGroupVersion.WithResource("configmaps")).Informer(), mapMetadata(KindConfigMap)),
		register(w, w.meta.ForResource(corev1.SchemeGroupVersion.WithResource("secrets")).Informer(), mapMetadata(KindSecret)),
		handle(podInformer.Informer(), w.upsertPod, w.removePod),
		handle(serviceInformer.Informer(), w.upsertService, w.removeService),
	)
	if err != nil {
		return fmt.Errorf("registering informers: %w", err)
	}

	w.typed.Start(ctx.Done())
	w.meta.Start(ctx.Done())
	for typ, ok := range w.typed.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return fmt.Errorf("cache for %v did not sync", typ)
		}
	}
	for gvr, ok := range w.meta.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return fmt.Errorf("cache for %v did not sync", gvr)
		}
	}
	w.log.Info("cluster caches synced")
	return nil
}

// register wires an informer to a mapping function for objects whose node
// and edges depend only on the object itself.
func register[T metav1.Object](w *Watcher, inf cache.SharedIndexInformer, mapFn func(T) (graph.Node, []graph.Edge)) error {
	return handle(inf,
		func(obj T) {
			n, edges := mapFn(obj)
			w.mu.Lock()
			defer w.mu.Unlock()
			w.graph.UpsertNode(n)
			w.setEdges(n.ID, edges)
		},
		func(obj T) {
			n, _ := mapFn(obj)
			w.mu.Lock()
			defer w.mu.Unlock()
			w.removeNode(n.ID)
		},
	)
}

// handle adds event handlers that call upsert on add/update and remove on
// delete, and strips fields kubexray must not keep in memory.
func handle[T metav1.Object](inf cache.SharedIndexInformer, upsert, remove func(T)) error {
	if err := inf.SetTransform(stripObject); err != nil {
		return err
	}
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if o, ok := obj.(T); ok {
				upsert(o)
			}
		},
		UpdateFunc: func(_, obj any) {
			if o, ok := obj.(T); ok {
				upsert(o)
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
	return err
}

// stripObject drops annotations (which may hold last-applied-configuration
// with embedded Secrets) and managed fields (large, unused) before objects
// are cached.
func stripObject(obj any) (any, error) {
	if m, ok := obj.(metav1.Object); ok {
		m.SetAnnotations(nil)
		m.SetManagedFields(nil)
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

func (w *Watcher) upsertPod(pod *corev1.Pod) {
	n, edges := mapPod(pod)
	services, err := w.services.Services(pod.Namespace).List(labels.Everything())
	if err != nil {
		w.log.Error("listing services", "namespace", pod.Namespace, "err", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.graph.UpsertNode(n)
	w.setEdges(n.ID, edges)
	podLabels := labels.Set(pod.Labels)
	for _, svc := range services {
		svcID := graph.ID(KindService, svc.Namespace, svc.Name)
		sel := selector(svc)
		w.setEdge(selectsKey(svcID), graph.NewEdge(graph.EdgeSelects, svcID, n.ID), sel != nil && sel.Matches(podLabels))
	}
}

func (w *Watcher) removePod(pod *corev1.Pod) {
	id := graph.ID(KindPod, pod.Namespace, pod.Name)
	services, err := w.services.Services(pod.Namespace).List(labels.Everything())
	if err != nil {
		w.log.Error("listing services", "namespace", pod.Namespace, "err", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.removeNode(id)
	for _, svc := range services {
		svcID := graph.ID(KindService, svc.Namespace, svc.Name)
		w.setEdge(selectsKey(svcID), graph.NewEdge(graph.EdgeSelects, svcID, id), false)
	}
}

func (w *Watcher) upsertService(svc *corev1.Service) {
	n, edges := mapService(svc)
	var selects []graph.Edge
	if sel := selector(svc); sel != nil {
		pods, err := w.pods.Pods(svc.Namespace).List(sel)
		if err != nil {
			w.log.Error("listing pods", "namespace", svc.Namespace, "err", err)
		}
		for _, pod := range pods {
			selects = append(selects, graph.NewEdge(graph.EdgeSelects, n.ID, graph.ID(KindPod, pod.Namespace, pod.Name)))
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.graph.UpsertNode(n)
	w.setEdges(n.ID, edges)
	w.setEdges(selectsKey(n.ID), selects)
}

func (w *Watcher) removeService(svc *corev1.Service) {
	id := graph.ID(KindService, svc.Namespace, svc.Name)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.setEdges(selectsKey(id), nil)
	w.removeNode(id)
}
