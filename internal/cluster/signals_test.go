package cluster

import (
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"

	"github.com/kubexray/kubexray/internal/graph"
)

func restartingPod(count int32, finished time.Time, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "reports-1"},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "reports", RestartCount: count,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: metav1.NewTime(finished)}},
			}},
		},
	}
}

func newSignalWatcher() *Watcher {
	return New(kubefake.NewClientset(), metadatafake.NewSimpleMetadataClient(metadatafake.NewTestScheme()), graph.New(), slog.New(slog.DiscardHandler))
}

func TestRestartTimes(t *testing.T) {
	t.Parallel()
	w := newSignalWatcher()
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	const id = "Pod/shop/reports-1"

	// First seen with 2 restarts: only the latest's time is known.
	p1 := restartingPod(2, t0, true)
	if got := w.noteRestarts(id, nil, p1); len(got) != 1 || !got[0].Equal(t0) {
		t.Errorf("first seen: %v", got)
	}
	// A resync of the same pod adds nothing.
	if got := w.noteRestarts(id, nil, p1); len(got) != 1 {
		t.Errorf("resync: %v", got)
	}
	// It was ready, then restarted: that counts.
	p2 := restartingPod(3, t0.Add(time.Minute), false)
	if got := w.noteRestarts(id, p1, p2); len(got) != 2 || !got[1].Equal(t0.Add(time.Minute)) {
		t.Errorf("a restart after running fine: %v", got)
	}
	// Status updates without a restart change nothing.
	if got := w.noteRestarts(id, p2, p2); len(got) != 2 {
		t.Errorf("no restart: %v", got)
	}
	// Flaky: ready between every restart. Only the last ten are kept.
	prev := p2
	for i := int32(4); i < 20; i++ {
		up := restartingPod(i-1, t0.Add(time.Duration(i)*time.Minute), true)
		w.noteRestarts(id, prev, up)
		down := restartingPod(i, t0.Add(time.Duration(i)*time.Minute), false)
		w.noteRestarts(id, up, down)
		prev = down
	}
	if got := w.restarts[id]; len(got) != graph.MaxRestartTimes {
		t.Errorf("kept %d times", len(got))
	}
}

// A crash loop never gets ready between restarts: they're its own
// incident, not a pattern of restarts once it's fixed.
func TestCrashLoopRestartsDontCount(t *testing.T) {
	t.Parallel()
	w := newSignalWatcher()
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	const id = "Pod/shop/checkout-1"
	prev := restartingPod(0, time.Time{}, false)
	w.noteRestarts(id, nil, prev)
	for i := int32(1); i <= 5; i++ {
		next := restartingPod(i, t0.Add(time.Duration(i)*time.Minute), false)
		w.noteRestarts(id, prev, next)
		prev = next
	}
	fixed := restartingPod(5, t0.Add(5*time.Minute), true)
	if got := w.noteRestarts(id, prev, fixed); len(got) != 0 {
		t.Errorf("crash-loop restarts kept: %v", got)
	}
}

// Docker or the node restarting stops every container with reason Unknown:
// the apps didn't restart themselves, so it's no pattern of restarts.
func TestRestartsWhenTheNodeWentAwayDontCount(t *testing.T) {
	t.Parallel()
	w := newSignalWatcher()
	t0 := time.Date(2026, 10, 30, 21, 11, 0, 0, time.UTC)
	const id = "Pod/kube-system/kindnet-1"
	unknown := func(count int32, finished time.Time, ready bool) *corev1.Pod {
		p := restartingPod(count, finished, ready)
		p.Status.ContainerStatuses[0].LastTerminationState.Terminated.Reason = "Unknown"
		return p
	}
	// First seen after the node came back.
	if got := w.noteRestarts(id, nil, unknown(2, t0, true)); len(got) != 0 {
		t.Errorf("first seen: %v", got)
	}
	// Ready, then the node went away again.
	if got := w.noteRestarts(id, unknown(2, t0, true), unknown(3, t0.Add(time.Minute), false)); len(got) != 0 {
		t.Errorf("node restart counted: %v", got)
	}
	// A real restart afterwards still counts.
	up := unknown(3, t0.Add(time.Minute), true)
	w.noteRestarts(id, unknown(3, t0.Add(time.Minute), false), up)
	if got := w.noteRestarts(id, up, restartingPod(4, t0.Add(2*time.Minute), false)); len(got) != 1 {
		t.Errorf("an OOM kill after it: %v", got)
	}
}

func TestMemoryPct(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "app", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}}},
		{Name: "sidecar"}, // no limit: doesn't count
	}}}
	usage := func(app, sidecar string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"containers": []any{
			map[string]any{"name": "app", "usage": map[string]any{"memory": app}},
			map[string]any{"name": "sidecar", "usage": map[string]any{"memory": sidecar}},
		}}}
	}
	if pct, ok := memoryPct(usage("243Mi", "2Gi"), pod); !ok || pct != 90 {
		t.Errorf("243Mi of 256Mi = %d%%, %v", pct, ok)
	}
	if _, ok := memoryPct(usage("bad", "1Mi"), &corev1.Pod{}); ok {
		t.Error("a pod without limits has a percentage")
	}
}
