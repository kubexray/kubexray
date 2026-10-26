package feed

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

// cluster is a graph state with the shop's api and checkout.
func cluster() *graph.State {
	st := &graph.State{Nodes: map[string]graph.Node{}, Edges: map[string]graph.Edge{}}
	add := func(n graph.Node) { st.Nodes[n.ID] = n }
	edge := func(t graph.EdgeType, from, to string) { e := graph.NewEdge(t, from, to); st.Edges[e.ID] = e }
	helm := &graph.Origin{Tool: "helm", Name: "shop"}
	add(graph.Node{
		ID: "Deployment/shop/api", Kind: "Deployment", Namespace: "shop", Name: "api", Origin: helm,
		Labels: map[string]string{"helm.sh/chart": "shop-1.4.0"},
		Status: graph.Status{Ready: true, Replicas: &graph.Replicas{Desired: 3, Ready: 3, Updated: 3}},
		Spec:   &graph.Spec{Revision: "1", Containers: []graph.Container{{Name: "api", Image: "nginx:1.27", Limits: graph.Resources{Memory: "64Mi"}}}},
	})
	add(graph.Node{ID: "ConfigMap/shop/api-config", Kind: "ConfigMap", Namespace: "shop", Name: "api-config", Origin: helm})
	add(graph.Node{
		ID: "Deployment/shop/checkout", Kind: "Deployment", Namespace: "shop", Name: "checkout",
		Status: graph.Status{Ready: true, Replicas: &graph.Replicas{Desired: 3, Ready: 3, Updated: 3}},
		Spec:   &graph.Spec{Revision: "1", Containers: []graph.Container{{Name: "checkout", Image: "busybox"}}},
	})
	add(graph.Node{ID: "ConfigMap/shop/checkout-config", Kind: "ConfigMap", Namespace: "shop", Name: "checkout-config"})
	add(graph.Node{ID: "ReplicaSet/shop/checkout-1", Kind: "ReplicaSet", Namespace: "shop", Name: "checkout-1"})
	add(graph.Node{ID: "Pod/shop/checkout-1-a", Kind: "Pod", Namespace: "shop", Name: "checkout-1-a"})
	edge(graph.EdgeOwns, "Deployment/shop/checkout", "ReplicaSet/shop/checkout-1")
	edge(graph.EdgeOwns, "ReplicaSet/shop/checkout-1", "Pod/shop/checkout-1-a")
	edge(graph.EdgeMounts, "Pod/shop/checkout-1-a", "ConfigMap/shop/checkout-config")
	add(graph.Node{ID: "Node/worker-2", Kind: "Node", Name: "worker-2", Status: graph.Status{Ready: true}})
	return st
}

func by(manager, tool, via string, when time.Time) *graph.Change {
	return &graph.Change{At: when, By: manager, Tool: tool, Via: via}
}

// apply observes a delta of upserted nodes at a time, then applies it.
func apply(t *testing.T, tr *Tracker, st *graph.State, when time.Time, nodes ...graph.Node) {
	t.Helper()
	m := graph.Message{V: 1, Type: graph.MessageDelta, Seq: st.Seq + 1, TS: when}
	for i := range nodes {
		m.Ops = append(m.Ops, graph.Op{Op: graph.OpUpsertNode, Node: &nodes[i]})
	}
	tr.Observe(st, m)
	if err := st.Apply(m); err != nil {
		t.Fatal(err)
	}
}

func only(t *testing.T, tr *Tracker) Set {
	t.Helper()
	sets := tr.Sets(time.Time{})
	if len(sets) != 1 {
		t.Fatalf("%d sets, want 1: %+v", len(sets), sets)
	}
	return sets[0]
}

func TestHelmUpgradeIsOneSet(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	api, cfg := st.Nodes["Deployment/shop/api"], st.Nodes["ConfigMap/shop/api-config"]
	helm := by("helm", "helm", "", at(0))
	api.Labels = map[string]string{"helm.sh/chart": "shop-2.0.0"}
	api.Spec = &graph.Spec{Revision: "2", Containers: []graph.Container{{Name: "api", Image: "nginx:1.29", Limits: graph.Resources{Memory: "64Mi"}}}}
	api.Change, cfg.Change = helm, helm
	apply(t, tr, st, at(0), api)
	apply(t, tr, st, at(2), cfg)

	s := only(t, tr)
	if s.Title != "Helm upgrade shop 1.4.0 → 2.0.0" || len(s.Entries) != 2 {
		t.Errorf("set = %q with %d entries", s.Title, len(s.Entries))
	}
	if e := s.Entries[0]; e.Summary != "image nginx:1.27 → nginx:1.29" || e.Drift != "" {
		t.Errorf("first entry = %+v", e)
	}
	if s.Entries[1].Summary != "edited (contents not read)" || len(s.Apps) != 1 || s.Apps[0] != "Deployment/shop/api" {
		t.Errorf("entries %+v, apps %v", s.Entries, s.Apps)
	}
}

// What the demo story showed: Helm's release Secrets between the objects
// of an install or upgrade, an upgrade that only scales, and a fresh install.
func TestHelmInstallsAndUpgradesReadAsSuch(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	release := &graph.Origin{Tool: "helm", Name: "storefront"}
	labels := func(v string) map[string]string { return map[string]string{"helm.sh/chart": "storefront-" + v} }
	helm := func(s float64) *graph.Change { return by("helm", "helm", "", at(s)) }
	secret := func(rev string, s float64) graph.Node {
		return graph.Node{ID: "Secret/shop/sh.helm.release.v1.storefront." + rev, Kind: "Secret", Namespace: "shop", Name: "sh.helm.release.v1.storefront." + rev, Change: by("Helm", "helm", "", at(s))}
	}
	front := graph.Node{
		ID: "Deployment/shop/storefront", Kind: "Deployment", Namespace: "shop", Name: "storefront", Origin: release, Labels: labels("1.0.0"), Change: helm(0),
		Status: graph.Status{Ready: true, Replicas: &graph.Replicas{Desired: 2, Ready: 2, Updated: 2}},
		Spec:   &graph.Spec{Revision: "1", Containers: []graph.Container{{Name: "storefront", Image: "nginx:1.28"}}},
	}
	svc := graph.Node{ID: "Service/shop/storefront", Kind: "Service", Namespace: "shop", Name: "storefront", Origin: release, Labels: labels("1.0.0"), Change: helm(0)}
	apply(t, tr, st, at(0), front, secret("v1", 0), svc)

	front.Labels, front.Change = labels("1.0.1"), helm(60)
	front.Status.Replicas = &graph.Replicas{Desired: 3, Ready: 2, Updated: 3}
	svc.Labels, svc.Change = labels("1.0.1"), helm(60)
	apply(t, tr, st, at(60), secret("v2", 60), front, svc)

	sets := tr.Sets(time.Time{})
	if len(sets) != 2 {
		t.Fatalf("%d sets, want the install and the upgrade: %+v", len(sets), sets)
	}
	if sets[1].Title != "Helm install storefront 1.0.0" || len(sets[1].Entries) != 2 {
		t.Errorf("install = %q, %d entries", sets[1].Title, len(sets[1].Entries))
	}
	if sets[0].Title != "Helm upgrade storefront 1.0.0 → 1.0.1" {
		t.Errorf("upgrade = %q, entries %+v", sets[0].Title, sets[0].Entries)
	}
}

func TestAnotherAuthorDoesntSplitASet(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	api, cfg, checkout := st.Nodes["Deployment/shop/api"], st.Nodes["ConfigMap/shop/api-config"], st.Nodes["Deployment/shop/checkout"]
	helm := by("helm", "helm", "", at(0))
	api.Labels, api.Change = map[string]string{"helm.sh/chart": "shop-2.0.0"}, helm
	api.Spec = &graph.Spec{Revision: "2", Containers: []graph.Container{{Name: "api", Image: "nginx:1.29", Limits: graph.Resources{Memory: "64Mi"}}}}
	apply(t, tr, st, at(0), api)
	checkout.Status.Replicas = &graph.Replicas{Desired: 4, Ready: 3, Updated: 4} // an autoscaler, in between
	apply(t, tr, st, at(1), checkout)
	cfg.Change = by("helm", "helm", "", at(2))
	apply(t, tr, st, at(2), cfg)

	sets := tr.Sets(time.Time{})
	if len(sets) != 2 || sets[1].Title != "Helm upgrade shop 1.4.0 → 2.0.0" || len(sets[1].Entries) != 2 {
		t.Fatalf("sets = %+v", sets)
	}
}

func TestScaleAndDrift(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	api := st.Nodes["Deployment/shop/api"]
	api.Spec = &graph.Spec{Revision: "2", Containers: []graph.Container{{Name: "api", Image: "nginx:1.27", Limits: graph.Resources{Memory: "128Mi"}}}}
	api.Change = by("kubectl-edit", "kubectl", "edit", at(0))
	apply(t, tr, st, at(0), api)
	s := only(t, tr)
	if e := s.Entries[0]; e.Summary != "memory limit 64Mi → 128Mi" || !strings.Contains(e.Drift, "managed by Helm release shop; the next helm upgrade puts it back") {
		t.Errorf("hand edit = %+v", e)
	}

	// A scale an hour later: Kubernetes records no author for it.
	co := st.Nodes["Deployment/shop/checkout"]
	co.Status.Replicas = &graph.Replicas{Desired: 5, Ready: 3, Updated: 5}
	apply(t, tr, st, at(3600), co)
	sets := tr.Sets(time.Time{})
	if len(sets) != 2 || sets[0].Title != "checkout scaled 3 → 5 replicas" || sets[0].By != nil {
		t.Errorf("sets = %+v", sets)
	}
}

func TestNodesAndNoise(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	w := st.Nodes["Node/worker-2"]
	w.Status = graph.Status{Ready: true, Reason: "SchedulingDisabled"}
	w.Change = by("kubectl-cordon", "kubectl", "cordon", at(0))
	apply(t, tr, st, at(0), w)
	// Pods and ReplicaSets churn: no entries.
	p := st.Nodes["Pod/shop/checkout-1-a"]
	p.Status.Restarts = 4
	apply(t, tr, st, at(10), p)
	w.Status = graph.Status{Ready: false, Reason: "NodeStatusUnknown"}
	apply(t, tr, st, at(100), w)

	sets := tr.Sets(time.Time{})
	if len(sets) != 2 || sets[1].Title != "Node worker-2 cordoned" || sets[0].Title != "Node worker-2 not ready (NodeStatusUnknown)" {
		t.Errorf("sets = %+v", sets)
	}
	tr.Assess(st, nil, at(200))
	if s := tr.Sets(time.Time{})[1]; s.Verdict != NoEffect {
		t.Errorf("cordon verdict = %q, want none: it touches no app", s.Verdict)
	}
}

// incident is the cause engine naming a cause for checkout.
func incident(object string, causeAt, since time.Time) cause.Incident {
	return cause.Incident{
		App: "Deployment/shop/checkout", Kind: "Deployment", Namespace: "shop", Name: "checkout", Since: since,
		Symptom: cause.Symptom{Ready: 2, Desired: 3, BadPods: []string{"x"}},
		Causes:  []cause.Cause{{Kind: cause.KindConfig, Object: object, At: causeAt, Confidence: cause.Likely}},
	}
}

func TestTheConfigMapStory(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	cfg := st.Nodes["ConfigMap/shop/checkout-config"]
	cfg.Change = by("kubectl-patch", "kubectl", "patch", at(0))
	apply(t, tr, st, at(0), cfg)

	// Nothing restarts yet: its apps stay healthy.
	tr.Assess(st, nil, at(5))
	if s := only(t, tr); s.Verdict != Healthy || s.Note != "its apps stayed healthy" || s.Apps[0] != "Deployment/shop/checkout" {
		t.Fatalf("right after = %q %q %v", s.Verdict, s.Note, s.Apps)
	}
	// A pod restarts, picks up the edit and crash-loops: the cause engine
	// names the edit.
	inc := incident(cfg.ID, at(0), at(61))
	tr.Assess(st, []cause.Incident{inc}, at(100))
	if s := only(t, tr); s.Verdict != Broke || s.Incident != inc.App || s.Note != "checkout is degraded 1m 1s later" {
		t.Fatalf("after the incident = %q %q", s.Verdict, s.Note)
	}

	// The same author edits it back, during the incident.
	cfg.Change = by("kubectl-patch", "kubectl", "patch", at(420))
	apply(t, tr, st, at(420), cfg)
	tr.Assess(st, []cause.Incident{inc}, at(425))
	tr.Assess(st, nil, at(455)) // checkout recovered
	sets := tr.Sets(time.Time{})
	if len(sets) != 2 {
		t.Fatalf("%d sets", len(sets))
	}
	if fix := sets[0]; fix.Verdict != Fixed || fix.Note != "checkout healthy 35s later" {
		t.Errorf("the edit back = %q %q", fix.Verdict, fix.Note)
	}
	if s := sets[1]; s.Verdict != Broke || !s.Ended.Equal(at(455)) {
		t.Errorf("the breaking edit after recovery = %q, ended %v", s.Verdict, s.Ended)
	}
}

func TestRolloutSettlesOrSticks(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	co := st.Nodes["Deployment/shop/checkout"]
	co.Change = by("kubectl-set", "kubectl", "set", at(0))
	co.Spec = &graph.Spec{Revision: "2", Containers: []graph.Container{{Name: "checkout", Image: "busybox:1.37"}}}
	co.Status = graph.Status{Replicas: &graph.Replicas{Desired: 3, Ready: 2, Updated: 1}}
	apply(t, tr, st, at(0), co)

	tr.Assess(st, nil, at(10))
	if s := only(t, tr); s.Verdict != Settling || s.Note != "checkout: 2 of 3 ready" {
		t.Errorf("mid-rollout = %q %q", s.Verdict, s.Note)
	}
	co.Status = graph.Status{Ready: true, Replicas: &graph.Replicas{Desired: 3, Ready: 3, Updated: 3}}
	apply(t, tr, st, at(48), co)
	tr.Assess(st, nil, at(48))
	if s := only(t, tr); s.Verdict != Healthy || s.Note != "settled in 48s" {
		t.Errorf("rolled out = %q %q", s.Verdict, s.Note)
	}

	// Another rollout that never completes.
	co.Spec = &graph.Spec{Revision: "3", Containers: []graph.Container{{Name: "checkout", Image: "busybox:9.9"}}}
	co.Status = graph.Status{Replicas: &graph.Replicas{Desired: 3, Ready: 3, Updated: 1}}
	co.Change = by("kubectl-set", "kubectl", "set", at(3600))
	apply(t, tr, st, at(3600), co)
	tr.Assess(st, nil, at(3600+11*60))
	if s := tr.Sets(time.Time{})[0]; s.Verdict != Stuck {
		t.Errorf("never rolled out = %q %q", s.Verdict, s.Note)
	}
}

func TestOldestSetsAreDropped(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(3)
	cfg := st.Nodes["ConfigMap/shop/checkout-config"]
	for i := range 5 {
		cfg.Change = by("kubectl-patch", "kubectl", "patch", at(float64(i*60)))
		apply(t, tr, st, at(float64(i*60)), cfg)
	}
	sets := tr.Sets(time.Time{})
	if len(sets) != 3 || !sets[0].At.Equal(at(240)) || !sets[2].At.Equal(at(120)) {
		t.Errorf("kept %d sets, newest %v", len(sets), sets[0].At)
	}
	if got := tr.Sets(at(200)); len(got) != 1 {
		t.Errorf("Sets(since) = %d, want the 1 after it", len(got))
	}
}

func TestRebuildFromHistory(t *testing.T) {
	t.Parallel()
	st := cluster()
	snap := st.Snapshot()
	snap.TS = at(0)
	cfg := st.Nodes["ConfigMap/shop/checkout-config"]
	cfg.Change = by("kubectl-patch", "kubectl", "patch", at(60))
	edit := graph.Message{V: 1, Type: graph.MessageDelta, Seq: snap.Seq + 1, TS: at(60), Ops: []graph.Op{{Op: graph.OpUpsertNode, Node: &cfg}}}
	restart := snap // a new epoch starts from its own snapshot: no entries
	restart.TS = at(120)
	history := make([][]byte, 0, 3)
	for _, m := range []graph.Message{snap, edit, restart} {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, raw)
	}
	tr := New(MaxSets)
	err := Rebuild(tr, func(fn func([]byte) error) error {
		for _, raw := range history {
			if err := fn(raw); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := only(t, tr); s.Title != "ConfigMap checkout-config edited" || s.Verdict == "" {
		t.Errorf("rebuilt = %q, verdict %q", s.Title, s.Verdict)
	}
}

func TestRolloutBeforeStatusCatchesUp(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	co := st.Nodes["Deployment/shop/checkout"]
	co.Change = by("kubectl-set", "kubectl", "set", at(0))
	co.Spec = &graph.Spec{Revision: "2", Containers: []graph.Container{{Name: "checkout", Image: "busybox:9.9"}}}
	apply(t, tr, st, at(0), co) // status still says ready: the controller hasn't started

	tr.Assess(st, nil, at(1))
	if s := only(t, tr); s.Verdict != Settling {
		t.Fatalf("right after = %q %q, want settling", s.Verdict, s.Note)
	}
	// Seen rolling out, then hung: back to settling, then stuck.
	co.Status = graph.Status{Replicas: &graph.Replicas{Desired: 3, Ready: 3, Updated: 1}}
	apply(t, tr, st, at(5), co)
	tr.Assess(st, nil, at(5))
	tr.Assess(st, nil, at(11*60))
	if s := only(t, tr); s.Verdict != Stuck {
		t.Errorf("hung rollout = %q %q", s.Verdict, s.Note)
	}
}

func TestNewerSetsWaitForTheirState(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	stale := st.Clone()
	co := st.Nodes["Deployment/shop/checkout"]
	co.Change = by("kubectl-set", "kubectl", "set", at(10))
	co.Spec = &graph.Spec{Revision: "2", Containers: []graph.Container{{Name: "checkout", Image: "busybox:1.37"}}}
	apply(t, tr, st, at(10), co)
	tr.Assess(stale, nil, at(11)) // analyzed before the change
	if s := only(t, tr); s.Verdict != "" {
		t.Errorf("judged on a state without it: %q", s.Verdict)
	}
}

func TestDeletionIsNeutral(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	m := graph.Message{V: 1, Type: graph.MessageDelta, Seq: st.Seq + 1, TS: at(0), Ops: []graph.Op{{Op: graph.OpRemoveNode, ID: "Deployment/shop/checkout"}}}
	tr.Observe(st, m)
	if err := st.Apply(m); err != nil {
		t.Fatal(err)
	}
	tr.Assess(st, nil, at(5))
	if s := only(t, tr); s.Title != "Deployment checkout deleted" || s.Verdict != NoEffect {
		t.Errorf("deletion = %q %q %q", s.Title, s.Verdict, s.Note)
	}
}

func TestSeedPutsHistoryFirst(t *testing.T) {
	t.Parallel()
	st, live, history := cluster(), New(MaxSets), New(MaxSets)
	cfg := st.Nodes["ConfigMap/shop/checkout-config"]
	cfg.Change = by("kubectl-patch", "kubectl", "patch", at(0))
	apply(t, history, st.Clone(), at(0), cfg)
	cfg.Change = by("kubectl-patch", "kubectl", "patch", at(600))
	apply(t, live, st, at(600), cfg)
	live.Seed(history)
	sets := live.Sets(time.Time{})
	if len(sets) != 2 || !sets[0].At.Equal(at(600)) || !sets[1].At.Equal(at(0)) {
		t.Errorf("seeded = %+v", sets)
	}
}
