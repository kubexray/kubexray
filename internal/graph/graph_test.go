package graph

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func newTestGraph() *Graph {
	g := New()
	g.now = func() time.Time { return t0 }
	return g
}

func pod(id string) Node {
	return Node{ID: id, Kind: "Pod", Namespace: "default", Name: "pod-" + id, Status: Status{Phase: "Running", Ready: true}}
}

// ops renders a delta's operations compactly, e.g. "+n:a +e:owns:a:b -n:c".
func ops(t *testing.T, g *Graph) []string {
	t.Helper()
	msg, ok := g.Flush()
	if !ok {
		return nil
	}
	out := make([]string, 0, len(msg.Ops))
	for _, o := range msg.Ops {
		switch o.Op {
		case OpUpsertNode:
			out = append(out, "+n:"+o.Node.ID)
		case OpRemoveNode:
			out = append(out, "-n:"+o.ID)
		case OpUpsertEdge:
			out = append(out, "+e:"+o.Edge.ID)
		case OpRemoveEdge:
			out = append(out, "-e:"+o.ID)
		}
	}
	return out
}

func assertOps(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

func TestFlushEmpty(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	if _, ok := g.Flush(); ok {
		t.Fatal("Flush on empty graph reported changes")
	}
}

func TestUpsertAndRemoveNode(t *testing.T) {
	t.Parallel()
	g := newTestGraph()

	g.UpsertNode(pod("b"))
	g.UpsertNode(pod("a"))
	assertOps(t, ops(t, g), "+n:a", "+n:b")

	g.RemoveNode("a")
	assertOps(t, ops(t, g), "-n:a")
}

func TestIdenticalUpsertIsNoop(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	g.UpsertNode(pod("a"))
	ops(t, g)

	g.UpsertNode(pod("a"))
	assertOps(t, ops(t, g))
}

func TestChangedNodeIsUpserted(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	g.UpsertNode(pod("a"))
	ops(t, g)

	p := pod("a")
	p.Status.Restarts = 1
	g.UpsertNode(p)
	assertOps(t, ops(t, g), "+n:a")
}

func TestChangeRevertedWithinBatchIsNoop(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	g.UpsertNode(pod("a"))
	ops(t, g)

	p := pod("a")
	p.Status.Ready = false
	g.UpsertNode(p)
	g.UpsertNode(pod("a"))
	assertOps(t, ops(t, g))
}

func TestAddThenRemoveWithinBatchIsNoop(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	g.UpsertNode(pod("a"))
	g.RemoveNode("a")
	assertOps(t, ops(t, g))
}

func TestEdgeHeldBackUntilBothEndpointsExist(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	e := NewEdge(EdgeSelects, "svc", "a")

	g.UpsertEdge(e)
	g.UpsertNode(pod("svc"))
	assertOps(t, ops(t, g), "+n:svc")

	g.UpsertNode(pod("a"))
	assertOps(t, ops(t, g), "+n:a", "+e:"+e.ID)
}

func TestRemoveNodeRemovesItsEdges(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	e := NewEdge(EdgeOwns, "rs", "a")
	g.UpsertNode(pod("rs"))
	g.UpsertNode(pod("a"))
	g.UpsertEdge(e)
	assertOps(t, ops(t, g), "+n:a", "+n:rs", "+e:"+e.ID)

	g.RemoveNode("a")
	assertOps(t, ops(t, g), "-e:"+e.ID, "-n:a")

	// The edge is gone for good, not just hidden: re-adding the node doesn't bring it back.
	g.UpsertNode(pod("a"))
	assertOps(t, ops(t, g), "+n:a")
}

func TestRemoveEdge(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	e := NewEdge(EdgeMounts, "a", "cm")
	g.UpsertNode(pod("a"))
	g.UpsertNode(pod("cm"))
	g.UpsertEdge(e)
	ops(t, g)

	g.RemoveEdge(e.ID)
	assertOps(t, ops(t, g), "-e:"+e.ID)
}

func TestHiddenEdgeRemovedSilently(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	g.UpsertNode(pod("svc"))
	e := NewEdge(EdgeSelects, "svc", "missing")
	g.UpsertEdge(e)
	ops(t, g)

	g.RemoveEdge(e.ID)
	assertOps(t, ops(t, g))
}

func TestSeqIncrementsOnlyWhenEmitting(t *testing.T) {
	t.Parallel()
	g := newTestGraph()

	g.UpsertNode(pod("a"))
	m1, _ := g.Flush()
	g.Flush() // nothing changed
	g.UpsertNode(pod("b"))
	m2, _ := g.Flush()

	if m1.Seq != 1 || m2.Seq != 2 {
		t.Errorf("seqs = %d, %d; want 1, 2", m1.Seq, m2.Seq)
	}
}

func TestSnapshotReflectsFlushedStateOnly(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	g.UpsertNode(pod("a"))
	g.UpsertNode(pod("b"))
	g.UpsertEdge(NewEdge(EdgeOwns, "a", "b"))
	g.Flush()

	g.UpsertNode(pod("c")) // pending, not yet flushed

	s := g.Snapshot()
	if s.Type != MessageSnapshot || s.Seq != 1 || s.V != SchemaVersion {
		t.Errorf("snapshot header = %+v", s)
	}
	if len(s.Nodes) != 2 || s.Nodes[0].ID != "a" || s.Nodes[1].ID != "b" {
		t.Errorf("snapshot nodes = %v, want [a b]", s.Nodes)
	}
	if len(s.Edges) != 1 {
		t.Errorf("snapshot edges = %v, want 1 edge", s.Edges)
	}
}

func TestSnapshotPlusDeltasConverges(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	g.UpsertNode(pod("a"))
	g.UpsertNode(pod("b"))
	g.Flush()

	client := map[string]bool{}
	for _, n := range g.Snapshot().Nodes {
		client[n.ID] = true
	}

	g.RemoveNode("a")
	g.UpsertNode(pod("c"))
	msg, _ := g.Flush()
	for _, o := range msg.Ops {
		switch o.Op {
		case OpUpsertNode:
			client[o.Node.ID] = true
		case OpRemoveNode:
			delete(client, o.ID)
		case OpUpsertEdge, OpRemoveEdge:
		}
	}

	want := map[string]bool{}
	for _, n := range g.Snapshot().Nodes {
		want[n.ID] = true
	}
	if fmt.Sprint(client) != fmt.Sprint(want) {
		t.Errorf("client state %v, want %v", client, want)
	}
}

func TestCallerMutationDoesNotLeakIntoGraph(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	n := pod("a")
	n.Labels = map[string]string{"app": "api"}
	n.Status.Replicas = &Replicas{Desired: 1}
	g.UpsertNode(n)
	g.Flush()

	n.Labels["app"] = "changed"
	n.Status.Replicas.Desired = 5

	got := g.Snapshot().Nodes[0]
	if got.Labels["app"] != "api" || got.Status.Replicas.Desired != 1 {
		t.Errorf("stored node was mutated through caller's references: %+v", got)
	}
}

func TestNodeEqual(t *testing.T) {
	t.Parallel()
	base := Node{
		ID: "a", Kind: "Deployment", Name: "api",
		Labels:  map[string]string{"app": "api"},
		Status:  Status{Replicas: &Replicas{Desired: 3, Ready: 3}},
		Created: t0,
	}
	tests := []struct {
		name   string
		mutate func(*Node)
		equal  bool
	}{
		{"identical", func(*Node) {}, true},
		{"same replicas, different pointer", func(n *Node) { n.Status.Replicas = &Replicas{Desired: 3, Ready: 3} }, true},
		{"same instant, different location", func(n *Node) { n.Created = t0.In(time.FixedZone("x", 3600)) }, true},
		{"label changed", func(n *Node) { n.Labels = map[string]string{"app": "web"} }, false},
		{"replicas changed", func(n *Node) { n.Status.Replicas = &Replicas{Desired: 3, Ready: 2} }, false},
		{"replicas removed", func(n *Node) { n.Status.Replicas = nil }, false},
		{"parent changed", func(n *Node) { n.Parent = "ns" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			other := base.clone()
			tt.mutate(&other)
			if got := base.Equal(other); got != tt.equal {
				t.Errorf("Equal = %v, want %v", got, tt.equal)
			}
		})
	}
}

func TestDeltaJSONShape(t *testing.T) {
	t.Parallel()
	g := newTestGraph()
	g.UpsertNode(Node{ID: "a", Kind: "Pod", Name: "p", Created: t0})
	g.Flush()
	g.RemoveNode("a")
	msg, _ := g.Flush()

	got, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"type":"delta","seq":2,"ts":"2026-10-20T12:00:00Z","ops":[{"op":"removeNode","id":"a"}]}`
	if string(got) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestConcurrentUse(t *testing.T) {
	t.Parallel()
	g := New()
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 200 {
				id := fmt.Sprintf("%d-%d", w, i%20)
				g.UpsertNode(pod(id))
				g.UpsertEdge(NewEdge(EdgeOwns, id, fmt.Sprintf("%d-%d", w, (i+1)%20)))
				if i%7 == 0 {
					g.RemoveNode(id)
				}
				if i%10 == 0 {
					g.Flush()
					g.Snapshot()
				}
			}
		})
	}
	wg.Wait()
	g.Flush()

	// After a final flush, the snapshot must not contain dangling edges.
	s := g.Snapshot()
	nodes := map[string]bool{}
	for _, n := range s.Nodes {
		nodes[n.ID] = true
	}
	for _, e := range s.Edges {
		if !nodes[e.From] || !nodes[e.To] {
			t.Fatalf("dangling edge in snapshot: %+v", e)
		}
	}
}
