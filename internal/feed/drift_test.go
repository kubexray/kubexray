package feed

import (
	"slices"
	"strings"
	"testing"

	"github.com/kubexray/kubexray/internal/graph"
)

// A Helm-managed ConfigMap, as Helm applied it, with the release's records.
func helmCluster() (*graph.State, graph.Node) {
	st := cluster()
	cfg := st.Nodes["ConfigMap/shop/api-config"]
	cfg.Data = map[string]graph.Value{"LOG_LEVEL": {Text: "info", Hash: "fp:1"}}
	cfg.Change = by("helm", "helm", "", at(-60))
	st.Nodes[cfg.ID] = cfg
	// Revision 3 is a failed upgrade: 2 is what Helm last deployed.
	for v, status := range map[string]string{"1": "superseded", "2": "superseded", "3": "failed"} {
		id := "Secret/shop/sh.helm.release.v1.shop.v" + v
		st.Nodes[id] = graph.Node{ID: id, Kind: "Secret", Namespace: "shop", Name: "sh.helm.release.v1.shop.v" + v, Labels: map[string]string{"owner": "helm", "name": "shop", "status": status, "version": v}}
	}
	return st, cfg
}

func TestDriftAgainstWhatHelmApplied(t *testing.T) {
	t.Parallel()
	st, cfg := helmCluster()
	tr := New(MaxSets)

	// Two hand edits: the second still differs from Helm's, not from the first.
	cfg.Data = map[string]graph.Value{"LOG_LEVEL": {Text: "debug", Hash: "fp:2"}}
	cfg.Change = by("kubectl-edit", "kubectl", "edit", at(0))
	apply(t, tr, st, at(0), cfg)
	cfg.Data = map[string]graph.Value{"LOG_LEVEL": {Text: "verbose", Hash: "fp:3"}}
	cfg.Change = by("kubectl-patch", "kubectl", "patch", at(5))
	apply(t, tr, st, at(5), cfg)

	e := tr.Sets(at(-1))[0].Entries[0] // newest first
	want := []DiffLine{{Path: "LOG_LEVEL", Op: Changed, From: "info", To: "verbose"}}
	if !slices.Equal(e.DriftDiff, want) {
		t.Errorf("drift diff = %+v", e.DriftDiff)
	}
	if want := `helm get manifest shop -n shop --revision 2 | kubectl create -n shop --dry-run=client -o json -f - | jq 'select(.kind == "ConfigMap" and .metadata.name == "api-config") | .metadata.annotations += {"meta.helm.sh/release-name": "shop", "meta.helm.sh/release-namespace": "shop"}' | kubectl replace -n shop -f -`; e.Restore != want {
		t.Errorf("restore = %q", e.Restore)
	}

	// Helm applies again: that's the new baseline.
	cfg.Data = map[string]graph.Value{"LOG_LEVEL": {Text: "warn", Hash: "fp:4"}}
	cfg.Change = by("helm", "helm", "", at(120))
	apply(t, tr, st, at(120), cfg)
	cfg.Data = map[string]graph.Value{"LOG_LEVEL": {Text: "error", Hash: "fp:5"}}
	cfg.Change = by("kubectl-edit", "kubectl", "edit", at(200))
	apply(t, tr, st, at(200), cfg)
	last := tr.Sets(at(-1))[0].Entries[0]
	if want := []DiffLine{{Path: "LOG_LEVEL", Op: Changed, From: "warn", To: "error"}}; !slices.Equal(last.DriftDiff, want) {
		t.Errorf("after an upgrade, drift diff = %+v", last.DriftDiff)
	}
}

func TestDriftWithoutWhatWasApplied(t *testing.T) {
	t.Parallel()
	st, cfg := helmCluster()
	cfg.Change = by("kubectl-edit", "kubectl", "edit", at(-30)) // already drifted at start
	st.Nodes[cfg.ID] = cfg
	tr := New(MaxSets)
	cfg.Data = map[string]graph.Value{"LOG_LEVEL": {Text: "debug", Hash: "fp:2"}}
	cfg.Change = by("kubectl-edit", "kubectl", "edit", at(0))
	apply(t, tr, st, at(0), cfg)

	e := only(t, tr).Entries[0]
	if e.DriftDiff != nil || !strings.Contains(e.Drift, "can't show what differs") || e.Restore == "" {
		t.Errorf("entry = %+v", e)
	}
}

func TestDriftByAnyToolButItsOwn(t *testing.T) {
	t.Parallel()
	n := graph.Node{Origin: &graph.Origin{Tool: "argocd", Name: "shop"}}
	for _, c := range []struct {
		tool string
		want bool
	}{{"kubectl", true}, {"other", true}, {"helm", true}, {"argocd", false}, {"controller", false}} {
		if got := drifts(n, &graph.Change{Tool: c.tool}); got != c.want {
			t.Errorf("%s: drifts = %v", c.tool, got)
		}
	}
}

func TestRestoreCommands(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		origin graph.Origin
		want   string
	}{
		{graph.Origin{Tool: "argocd", Name: "shop"}, "argocd app sync shop"},
		{graph.Origin{Tool: "flux", Name: "apps", Kind: "Kustomization", Namespace: "flux-system"}, "flux reconcile kustomization apps -n flux-system --with-source"},
		{graph.Origin{Tool: "flux", Name: "shop", Kind: "HelmRelease"}, "flux reconcile helmrelease shop --with-source"},
		{graph.Origin{Tool: "helm", Name: "shop", Namespace: "apps"}, `helm get manifest shop -n apps | kubectl create -n shop --dry-run=client -o json -f - | jq 'select(.kind == "" and .metadata.name == "") | .metadata.annotations += {"meta.helm.sh/release-name": "shop", "meta.helm.sh/release-namespace": "apps"}' | kubectl replace -n shop -f -`},
	} {
		if got := restore(cluster(), graph.Node{Namespace: "shop", Origin: &c.origin}); got != c.want {
			t.Errorf("%+v: restore = %q, want %q", c.origin, got, c.want)
		}
	}
}
