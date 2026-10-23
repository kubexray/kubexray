package graph_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/synthetic"
)

var thousand = synthetic.Size{Namespaces: 10, AppsPerNamespace: 10, PodsPerApp: 10, FailingEvery: 7}

func loaded(b *testing.B) (*graph.Graph, []graph.Node) {
	b.Helper()
	g := graph.New()
	st := synthetic.Cluster(thousand, time.Now())
	var pods []graph.Node
	for _, n := range st.Nodes {
		g.UpsertNode(n)
		if n.Kind == "Pod" {
			pods = append(pods, n)
		}
	}
	for _, e := range st.Edges {
		g.UpsertEdge(e)
	}
	g.Flush()
	return g, pods
}

// A busy moment: 50 pods change status in one 100 ms batch.
func BenchmarkFlush50Changes(b *testing.B) {
	g, pods := loaded(b)
	for i := 0; b.Loop(); i++ {
		for j := range 50 {
			p := pods[(i*50+j)%len(pods)]
			p.Status.Restarts = int32(i)
			g.UpsertNode(p)
		}
		if _, ok := g.Flush(); !ok {
			b.Fatal("no delta")
		}
	}
}

func BenchmarkSnapshotJSON1000Pods(b *testing.B) {
	g, _ := loaded(b)
	var size int
	for b.Loop() {
		raw, err := json.Marshal(g.Snapshot())
		if err != nil {
			b.Fatal(err)
		}
		size = len(raw)
	}
	b.ReportMetric(float64(size)/1024, "KiB/snapshot")
}
