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

func restartingPod(count int32, finished time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "reports-1"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "reports", RestartCount: count,
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: metav1.NewTime(finished)}},
		}}},
	}
}

func TestRestartTimes(t *testing.T) {
	t.Parallel()
	w := New(kubefake.NewClientset(), metadatafake.NewSimpleMetadataClient(metadatafake.NewTestScheme()), graph.New(), slog.New(slog.DiscardHandler))
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	const id = "Pod/shop/reports-1"

	// First seen with 2 restarts: only the latest's time is known.
	p1 := restartingPod(2, t0)
	if got := w.noteRestarts(id, nil, p1); len(got) != 1 || !got[0].Equal(t0) {
		t.Errorf("first seen: %v", got)
	}
	// A resync of the same pod adds nothing.
	if got := w.noteRestarts(id, nil, p1); len(got) != 1 {
		t.Errorf("resync: %v", got)
	}
	p2 := restartingPod(3, t0.Add(time.Minute))
	if got := w.noteRestarts(id, p1, p2); len(got) != 2 || !got[1].Equal(t0.Add(time.Minute)) {
		t.Errorf("a restart: %v", got)
	}
	// Status updates without a restart change nothing.
	if got := w.noteRestarts(id, p2, p2); len(got) != 2 {
		t.Errorf("no restart: %v", got)
	}
	prev := p2
	for i := int32(4); i < 20; i++ {
		next := restartingPod(i, t0.Add(time.Duration(i)*time.Minute))
		w.noteRestarts(id, prev, next)
		prev = next
	}
	if got := w.restarts[id]; len(got) != graph.MaxRestartTimes {
		t.Errorf("kept %d times", len(got))
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
