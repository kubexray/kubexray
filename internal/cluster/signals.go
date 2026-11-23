package cluster

import (
	"context"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kubexray/kubexray/internal/graph"
)

// Signals from Kubernetes itself (ADR-0016): when pods restart, and how
// close their memory is to its limit, from metrics-server.

// podMetrics is metrics-server's pod usage.
var podMetrics = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}

// metricsEvery is how often memory is read.
const metricsEvery = 30 * time.Second

// memUse is a pod's memory against its limit.
type memUse struct {
	pct  int32     // in steps of 5
	high time.Time // since when it's been at graph.MemoryHighPct or more
}

// noteRestarts records when a pod's containers restart, and returns its
// recent restart times. Kubernetes keeps only a count, and the latest
// finish time, so a pod seen for the first time has at most one.
//
// Only restarts of a pod that had been ready since its previous one count:
// the signal is for apps that look fine between restarts. A crash loop
// never gets ready, and is an incident of its own while it lasts; its
// restarts mustn't keep the app broken for an hour after it's fixed. Nor
// do restarts that ended for reason Unknown: the node or its container
// runtime went away under the pod (a reboot, Docker restarting), which
// isn't the app restarting.
// Called with w.mu held.
func (w *Watcher) noteRestarts(id string, old, pod *corev1.Pod) []time.Time {
	defer func() {
		if podReady(pod) {
			w.wasReady[id] = true
		}
	}()
	before := int32(0)
	if old != nil {
		before = restartCount(old)
	} else if t, ok := w.restarts[id]; ok {
		return t // a resync: nothing new
	}
	now := restartCount(pod)
	times := w.restarts[id]
	if old == nil && now > 0 && !nodeWentAway(pod) {
		if at := lastRestart(pod); !at.IsZero() {
			times = []time.Time{at}
		}
	}
	if before < now && old != nil {
		if w.wasReady[id] && !nodeWentAway(pod) {
			at := lastRestart(pod)
			if at.IsZero() {
				at = w.now()
			}
			times = append(times, at)
		}
		w.wasReady[id] = false
	}
	if len(times) > graph.MaxRestartTimes {
		times = times[len(times)-graph.MaxRestartTimes:]
	}
	w.restarts[id] = times
	return slices.Clone(times)
}

// nodeWentAway reports whether the container that stopped last ended for
// reason Unknown, which the kubelet gives when the runtime lost it rather
// than the app exiting.
func nodeWentAway(pod *corev1.Pod) bool {
	var last *corev1.ContainerStateTerminated
	for _, cs := range pod.Status.ContainerStatuses {
		if t := cs.LastTerminationState.Terminated; t != nil && (last == nil || t.FinishedAt.After(last.FinishedAt.Time)) {
			last = t
		}
	}
	return last != nil && last.Reason == "Unknown"
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func restartCount(pod *corev1.Pod) int32 {
	var n int32
	for _, cs := range pod.Status.ContainerStatuses {
		n += cs.RestartCount
	}
	return n
}

// lastRestart is when a container last stopped before restarting.
func lastRestart(pod *corev1.Pod) time.Time {
	var at time.Time
	for _, cs := range pod.Status.ContainerStatuses {
		if t := cs.LastTerminationState.Terminated; t != nil && t.FinishedAt.After(at) {
			at = t.FinishedAt.Time
		}
	}
	return at
}

// withSignals adds what the watcher knows beyond the pod object. Called
// with w.mu held.
func (w *Watcher) withSignals(n graph.Node, old, pod *corev1.Pod) graph.Node {
	n.Status.Restarted = w.noteRestarts(n.ID, old, pod)
	if m, ok := w.memory[n.ID]; ok {
		n.Status.MemoryPct, n.Status.MemoryHigh = m.pct, m.high
	}
	return n
}

// watchMemory reads pod memory from metrics-server until ctx ends, if the
// cluster has it.
func (w *Watcher) watchMemory(ctx context.Context) {
	if w.dyn == nil {
		return
	}
	if _, err := w.dyn.Resource(podMetrics).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		if apierrors.IsNotFound(err) {
			w.log.Info("no metrics-server: memory signals are off")
		} else {
			w.log.Info("can't read pod metrics: memory signals are off", "err", err)
		}
		return
	}
	w.mu.Lock()
	w.sources.MetricsServer = true
	w.mu.Unlock()
	go func() {
		t := time.NewTicker(metricsEvery)
		defer t.Stop()
		for {
			w.readMemory(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (w *Watcher) readMemory(ctx context.Context) {
	list, err := w.dyn.Resource(podMetrics).List(ctx, metav1.ListOptions{})
	if err != nil {
		w.log.Debug("reading pod metrics", "err", err)
		return
	}
	seen := map[string]bool{}
	for i := range list.Items {
		item := &list.Items[i]
		pod, err := w.pods.Pods(item.GetNamespace()).Get(item.GetName())
		if err != nil {
			continue
		}
		pct, ok := memoryPct(item, pod)
		if !ok {
			continue
		}
		id := graph.ID(KindPod, pod.Namespace, pod.Name)
		seen[id] = true
		w.mu.Lock()
		prev := w.memory[id]
		next := memUse{pct: pct}
		if pct >= graph.MemoryHighPct {
			next.high = prev.high
			if next.high.IsZero() {
				next.high = w.now()
			}
		}
		if next != prev {
			w.memory[id] = next
			n, _ := mapPod(pod)
			n = w.withSignals(n, pod, pod) // same object: no new restarts
			w.graph.UpsertNode(w.kept(n))
		}
		w.mu.Unlock()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range w.memory {
		if !seen[id] {
			delete(w.memory, id)
		}
	}
}

// memoryPct is the highest share of its memory limit any container uses,
// rounded down to a step of 5. Containers without a limit don't count.
func memoryPct(item *unstructured.Unstructured, pod *corev1.Pod) (int32, bool) {
	limits := map[string]resource.Quantity{}
	for _, c := range pod.Spec.Containers {
		if l, ok := c.Resources.Limits[corev1.ResourceMemory]; ok && !l.IsZero() {
			limits[c.Name] = l
		}
	}
	containers, _, _ := unstructured.NestedSlice(item.Object, "containers")
	best, found := int64(-1), false
	for _, c := range containers {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(m, "name")
		used, _, _ := unstructured.NestedString(m, "usage", "memory")
		limit, ok := limits[name]
		q, err := resource.ParseQuantity(used)
		if !ok || err != nil {
			continue
		}
		pct := q.Value() * 100 / limit.Value()
		if pct > best {
			best, found = pct, true
		}
	}
	if !found {
		return 0, false
	}
	return int32(min(best, 999) / 5 * 5), true
}

// forgetPod drops what the watcher knows about a deleted pod. Called with
// w.mu held.
func (w *Watcher) forgetPod(id string) {
	delete(w.restarts, id)
	delete(w.memory, id)
	delete(w.wasReady, id)
}
