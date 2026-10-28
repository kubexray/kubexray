package cause

import (
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

func signalCluster(pod graph.Node) *graph.State {
	st := &graph.State{Nodes: map[string]graph.Node{}, Edges: map[string]graph.Edge{}}
	app := graph.Node{
		ID: "Deployment/shop/reports", Kind: "Deployment", Namespace: "shop", Name: "reports",
		Status: graph.Status{Ready: true, Replicas: &graph.Replicas{Desired: 1, Ready: 1, Updated: 1}},
		Spec:   &graph.Spec{Containers: []graph.Container{{Name: "reports", Image: "reports:1", Limits: graph.Resources{Memory: "256Mi"}}}},
	}
	st.Nodes[app.ID] = app
	pod.ID, pod.Kind, pod.Namespace, pod.Name = "Pod/shop/reports-1", "Pod", "shop", "reports-1"
	pod.Spec = app.Spec
	pod.Status.Ready, pod.Status.Phase = true, "Running"
	st.Nodes[pod.ID] = pod
	e := graph.NewEdge(graph.EdgeOwns, app.ID, pod.ID)
	st.Edges[e.ID] = e
	return st
}

func TestRestartsAreAnIncident(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	pod := graph.Node{Status: graph.Status{Restarts: 6, Reason: "OOMKilled", Restarted: []time.Time{ago(200), ago(50), ago(30), ago(10)}}}
	incs := Analyze(signalCluster(pod), DefaultOptions(now))
	if len(incs) != 1 {
		t.Fatalf("incidents = %+v", incs)
	}
	inc := incs[0]
	if inc.State() != "keeps restarting" || inc.Signal.Source != "Kubernetes" || !inc.Since.Equal(ago(50)) ||
		!strings.Contains(inc.Symptom.Summary, "3 restarts in the last hour") || !strings.Contains(inc.Symptom.Summary, "OOMKilled") {
		t.Errorf("incident = %+v, signal %+v", inc, inc.Signal)
	}
	if len(inc.Next) == 0 || !strings.Contains(inc.Next[0].Command, "logs reports-1 --previous") {
		t.Errorf("next = %+v", inc.Next)
	}

	// Two in the hour is not a pattern.
	pod.Status.Restarted = []time.Time{ago(200), ago(30), ago(10)}
	if incs := Analyze(signalCluster(pod), DefaultOptions(now)); len(incs) != 0 {
		t.Errorf("two restarts: %+v", incs)
	}
}

func TestMemoryNearTheLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	pod := graph.Node{Status: graph.Status{MemoryPct: 95, MemoryHigh: now.Add(-6 * time.Minute)}}
	incs := Analyze(signalCluster(pod), DefaultOptions(now))
	if len(incs) != 1 || incs[0].State() != "is close to its memory limit" || !strings.Contains(incs[0].Symptom.Summary, "95% of 256Mi") {
		t.Fatalf("incidents = %+v", incs)
	}
	// Only just crossed: not yet.
	pod.Status.MemoryHigh = now.Add(-time.Minute)
	if incs := Analyze(signalCluster(pod), DefaultOptions(now)); len(incs) != 0 {
		t.Errorf("one minute at 95%%: %+v", incs)
	}
}
