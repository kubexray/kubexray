// Package synthetic builds large, realistic clusters as graph states, for
// benchmarks and the load generator: namespaces of apps, each with a
// Service, a ConfigMap, a ReplicaSet and pods, some of them failing.
package synthetic

import (
	"fmt"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// Size describes a synthetic cluster.
type Size struct {
	Namespaces, AppsPerNamespace, PodsPerApp int
	// FailingEvery makes every Nth app crash-loop after a recent ConfigMap
	// edit, so the cause engine has incidents to explain (0: none).
	FailingEvery int
}

// Pods is the total pod count.
func (s Size) Pods() int { return s.Namespaces * s.AppsPerNamespace * s.PodsPerApp }

// Cluster returns a graph state of the given size at time now.
func Cluster(s Size, now time.Time) *graph.State {
	st := &graph.State{Seq: 1, TS: now, Nodes: map[string]graph.Node{}, Edges: map[string]graph.Edge{}}
	add := func(n graph.Node) { st.Nodes[n.ID] = n }
	edge := func(t graph.EdgeType, from, to string) { e := graph.NewEdge(t, from, to); st.Edges[e.ID] = e }
	long := now.Add(-48 * time.Hour)

	add(graph.Node{ID: "Node/worker-1", Kind: "Node", Name: "worker-1", Status: graph.Status{Ready: true}})
	app := 0
	for ns := range s.Namespaces {
		nsName := fmt.Sprintf("team-%02d", ns)
		add(graph.Node{ID: graph.ID("Namespace", "", nsName), Kind: "Namespace", Name: nsName, Status: graph.Status{Ready: true}})
		for a := range s.AppsPerNamespace {
			app++
			name := fmt.Sprintf("svc-%02d", a)
			failing := s.FailingEvery > 0 && app%s.FailingEvery == 0
			ready := int32(s.PodsPerApp) //nolint:gosec // small test sizes
			if failing {
				ready--
			}
			dep := graph.Node{
				ID: graph.ID("Deployment", nsName, name), Kind: "Deployment", Namespace: nsName, Name: name, Created: long,
				Status: graph.Status{Ready: !failing, Replicas: &graph.Replicas{Desired: int32(s.PodsPerApp), Ready: ready, Updated: int32(s.PodsPerApp)}}, //nolint:gosec // small test sizes
				Spec:   &graph.Spec{Revision: "1", Containers: []graph.Container{{Name: name, Image: "registry.example/" + name + ":1.4.2"}}},
				Change: &graph.Change{At: long, By: "kubectl-client-side-apply", Tool: "kubectl", Via: "apply"},
			}
			add(dep)
			rs := graph.Node{ID: graph.ID("ReplicaSet", nsName, name+"-7d9f"), Kind: "ReplicaSet", Namespace: nsName, Name: name + "-7d9f", Created: long, Spec: dep.Spec}
			add(rs)
			edge(graph.EdgeOwns, dep.ID, rs.ID)
			svc := graph.ID("Service", nsName, name)
			add(graph.Node{ID: svc, Kind: "Service", Namespace: nsName, Name: name, Status: graph.Status{Ready: true}, Created: long})
			cm := graph.ID("ConfigMap", nsName, name+"-config")
			cmNode := graph.Node{ID: cm, Kind: "ConfigMap", Namespace: nsName, Name: name + "-config", Created: long, Status: graph.Status{Ready: true}}
			if failing {
				cmNode.Change = &graph.Change{At: now.Add(-5 * time.Minute), By: "kubectl-patch", Tool: "kubectl", Via: "patch"}
			}
			add(cmNode)
			for p := range s.PodsPerApp {
				pod := graph.Node{
					ID: graph.ID("Pod", nsName, fmt.Sprintf("%s-7d9f-%03d", name, p)), Kind: "Pod", Namespace: nsName,
					Name: fmt.Sprintf("%s-7d9f-%03d", name, p), Parent: "Node/worker-1", Created: long,
					Status: graph.Status{Phase: "Running", Ready: true, Since: long},
					Spec:   &graph.Spec{Containers: dep.Spec.Containers},
				}
				if failing && p == 0 {
					pod.Created = now.Add(-2 * time.Minute)
					pod.Status = graph.Status{Phase: "Running", Reason: "CrashLoopBackOff", Restarts: 3, Since: now.Add(-2 * time.Minute)}
				}
				add(pod)
				edge(graph.EdgeOwns, rs.ID, pod.ID)
				edge(graph.EdgeSelects, svc, pod.ID)
				edge(graph.EdgeMounts, pod.ID, cm)
			}
		}
	}
	return st
}
