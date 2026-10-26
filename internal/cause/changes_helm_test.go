package cause

import (
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

func TestChangesLeaveOutHelmRecords(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 20, 43, 50, 0, time.UTC)
	helm := &graph.Change{At: at, By: "Helm", Tool: "helm"}
	st := &graph.State{Nodes: map[string]graph.Node{
		"Secret/shop/sh.helm.release.v1.storefront.v2": {ID: "Secret/shop/sh.helm.release.v1.storefront.v2", Kind: "Secret", Namespace: "shop", Name: "sh.helm.release.v1.storefront.v2", Change: helm},
		"ConfigMap/shop/storefront-config":             {ID: "ConfigMap/shop/storefront-config", Kind: "ConfigMap", Namespace: "shop", Name: "storefront-config", Change: helm},
	}, Edges: map[string]graph.Edge{}}
	got := Changes(st, at.Add(-time.Minute), false)
	if len(got) != 1 || got[0].Object != "ConfigMap/shop/storefront-config" {
		t.Fatalf("changes = %+v", got)
	}
}
