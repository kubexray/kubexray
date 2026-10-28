package feed

import (
	"slices"
	"strings"
	"testing"

	"github.com/kubexray/kubexray/internal/graph"
)

func TestDiffOfAHandEdit(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	api := st.Nodes["Deployment/shop/api"]
	api.Spec = &graph.Spec{Revision: "2", Containers: []graph.Container{{
		Name: "api", Image: "nginx:1.27", Limits: graph.Resources{Memory: "64Mi"},
		Env: []graph.EnvVar{{Name: "PROMO_BANNER", Value: "fp:aaaaaaaaaaaa"}},
	}}}
	api.Change = by("kubectl-set", "kubectl", "set", at(0))
	apply(t, tr, st, at(0), api)

	e := only(t, tr).Entries[0]
	if e.Summary != "env PROMO_BANNER added (revision 1 → 2)" {
		t.Errorf("summary = %q", e.Summary)
	}
	want := []DiffLine{{Path: "env PROMO_BANNER", Op: Added, To: "a value", Note: "values aren't recorded"}}
	if !slices.Equal(e.Diff, want) {
		t.Errorf("diff = %+v", e.Diff)
	}
}

func TestDiffOfATemplate(t *testing.T) {
	t.Parallel()
	c := graph.Container{
		Name: "api", Image: "nginx:1.27", Limits: graph.Resources{Memory: "64Mi"}, Command: "fp:111111111111", Ports: []string{"80/TCP"},
		Env: []graph.EnvVar{
			{Name: "LOG_LEVEL", Value: "fp:aaaaaaaaaaaa"},
			{Name: "DB_PASSWORD", Value: "fp:bbbbbbbbbbbb"},
			{Name: "OLD", Value: "fp:cccccccccccc"},
		},
		Probes: graph.Probes{Readiness: true, ReadinessCheck: "http GET :80/"},
	}
	n := c
	n.Image, n.Limits.Memory, n.Command, n.Ports = "nginx:1.29", "128Mi", "fp:222222222222", []string{"8080/TCP"}
	n.Env = []graph.EnvVar{
		{Name: "LOG_LEVEL", Value: "fp:dddddddddddd"},
		{Name: "DB_PASSWORD", From: "Secret/db:password"},
	}
	n.Probes.ReadinessCheck = "http GET :8080/healthz"
	old := graph.Node{Spec: &graph.Spec{Containers: []graph.Container{c}, Volumes: []string{"ConfigMap/api-config"}}, Status: graph.Status{Replicas: &graph.Replicas{Desired: 3}}}
	now := graph.Node{Spec: &graph.Spec{Containers: []graph.Container{n}, Volumes: []string{"ConfigMap/api-config", "Secret/tls"}}, Status: graph.Status{Replicas: &graph.Replicas{Desired: 4}}}

	got := map[string]DiffLine{}
	for _, l := range objectDiff(old, now) {
		got[l.Path+" "+l.Op] = l
	}
	for k, want := range map[string]DiffLine{
		"image changed":           {Path: "image", Op: Changed, From: "nginx:1.27", To: "nginx:1.29"},
		"memory limit changed":    {Path: "memory limit", Op: Changed, From: "64Mi", To: "128Mi"},
		"readiness probe changed": {Path: "readiness probe", Op: Changed, From: "http GET :80/", To: "http GET :8080/healthz"},
		"env LOG_LEVEL changed":   {Path: "env LOG_LEVEL", Op: Changed, Note: "its value changed; values aren't recorded"},
		"env DB_PASSWORD changed": {Path: "env DB_PASSWORD", Op: Changed, From: "a value", To: "Secret/db:password"},
		"env OLD removed":         {Path: "env OLD", Op: Removed, From: "a value"},
		"command changed":         {Path: "command", Op: Changed, Note: "contents aren't recorded"},
		"port added":              {Path: "port", Op: Added, To: "8080/TCP"},
		"port removed":            {Path: "port", Op: Removed, From: "80/TCP"},
		"volume added":            {Path: "volume", Op: Added, To: "Secret/tls"},
		"replicas changed":        {Path: "replicas", Op: Changed, From: "3", To: "4"},
	} {
		if got[k] != want {
			t.Errorf("%s: got %+v, want %+v", k, got[k], want)
		}
	}
	if len(got) != 11 {
		t.Errorf("%d lines: %+v", len(got), got)
	}
	for _, l := range got {
		if strings.Contains(l.From+l.To, "fp:") {
			t.Errorf("a fingerprint shows in %+v", l)
		}
	}
}

func TestDiffWithValues(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	cfg := st.Nodes["ConfigMap/shop/checkout-config"]
	cfg.Data = map[string]graph.Value{
		"LOG_LEVEL": {Text: "info", Hash: "fp:1"},
		"API_TOKEN": {Hash: "fp:2", Masked: "name"},
		"OLD":       {Text: "x", Hash: "fp:3"},
	}
	st.Nodes[cfg.ID] = cfg
	cfg.Data = map[string]graph.Value{
		"LOG_LEVEL": {Text: "verbose", Hash: "fp:4"},
		"API_TOKEN": {Hash: "fp:5", Masked: "name"},
		"NEW":       {Text: "on", Hash: "fp:6"},
	}
	cfg.Change = by("kubectl-edit", "kubectl", "edit", at(0))
	apply(t, tr, st, at(0), cfg)

	e := only(t, tr).Entries[0]
	if e.Summary != "edited (API_TOKEN changed, LOG_LEVEL info → verbose and 2 more)" {
		t.Errorf("summary = %q", e.Summary)
	}
	want := []DiffLine{
		{Path: "API_TOKEN", Op: Changed, Note: "hidden: the name looks like a credential"},
		{Path: "LOG_LEVEL", Op: Changed, From: "info", To: "verbose", Value: true},
		{Path: "NEW", Op: Added, To: "on", Value: true},
		{Path: "OLD", Op: Removed, From: "x", Value: true},
	}
	if !slices.Equal(e.Diff, want) {
		t.Errorf("diff = %+v", e.Diff)
	}
}

func TestEnvDiffWithValues(t *testing.T) {
	t.Parallel()
	c := func(env ...graph.EnvVar) graph.Spec {
		return graph.Spec{Containers: []graph.Container{{Name: "api", Env: env}}}
	}
	var d differ
	d.template(
		c(graph.EnvVar{Name: "LOG_LEVEL", Value: "fp:a", Text: "info"}, graph.EnvVar{Name: "DB_URL", Value: "fp:b", Text: "postgres://[redacted]@db", Masked: "value"}),
		c(graph.EnvVar{Name: "LOG_LEVEL", Value: "fp:c", Text: "debug"}, graph.EnvVar{Name: "DB_URL", Value: "fp:d", Text: "postgres://[redacted]@db2", Masked: "value"}, graph.EnvVar{Name: "NEW", Value: "fp:e", Text: "1"}),
	)
	want := []DiffLine{
		{Path: "env LOG_LEVEL", Op: Changed, From: "info", To: "debug", Value: true},
		{Path: "env DB_URL", Op: Changed, From: "postgres://[redacted]@db", To: "postgres://[redacted]@db2", Note: "parts that look like credentials are hidden", Value: true},
		{Path: "env NEW", Op: Added, To: "1", Value: true},
	}
	if !slices.Equal(d.lines, want) {
		t.Errorf("diff = %+v", d.lines)
	}
}

func TestDiffOfAManifest(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	h := graph.Node{
		ID: "HorizontalPodAutoscaler/shop/api", Kind: "HorizontalPodAutoscaler", Namespace: "shop", Name: "api",
		Status:   graph.Status{Ready: true},
		Manifest: map[string]graph.Value{"spec.maxReplicas": {Text: "10", Hash: "fp:1"}, "spec.minReplicas": {Text: "2", Hash: "fp:2"}},
		Change:   by("helm", "helm", "", at(-60)),
	}
	st.Nodes[h.ID] = h
	h.Manifest = map[string]graph.Value{"spec.maxReplicas": {Text: "3", Hash: "fp:3"}, "spec.minReplicas": {Text: "2", Hash: "fp:2"}}
	h.Change = by("kubectl-edit", "kubectl", "edit", at(0))
	apply(t, tr, st, at(0), h)

	e := only(t, tr).Entries[0]
	if e.Summary != "changed (spec.maxReplicas 10 → 3)" {
		t.Errorf("summary = %q", e.Summary)
	}
	if want := []DiffLine{{Path: "spec.maxReplicas", Op: Changed, From: "10", To: "3", Value: true}}; !slices.Equal(e.Diff, want) {
		t.Errorf("diff = %+v", e.Diff)
	}
}

func TestDiffOfARolloutStrategy(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	api := st.Nodes["Deployment/shop/api"]
	api.Manifest = map[string]graph.Value{"spec.strategy.rollingUpdate.maxSurge": {Text: "25%", Hash: "fp:1"}}
	st.Nodes[api.ID] = api
	api.Manifest = map[string]graph.Value{"spec.strategy.rollingUpdate.maxSurge": {Text: "50%", Hash: "fp:2"}}
	api.Change = by("kubectl-edit", "kubectl", "edit", at(0))
	apply(t, tr, st, at(0), api)

	e := only(t, tr).Entries[0]
	if e.Summary != "changed (spec.strategy.rollingUpdate.maxSurge 25% → 50%)" {
		t.Errorf("summary = %q", e.Summary)
	}
	if want := []DiffLine{{Path: "spec.strategy.rollingUpdate.maxSurge", Op: Changed, From: "25%", To: "50%", Value: true}}; !slices.Equal(e.Diff, want) {
		t.Errorf("diff = %+v", e.Diff)
	}
}

func TestDiffWithoutAWriteRecord(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	sa := graph.Node{
		ID: "ServiceAccount/shop/api", Kind: "ServiceAccount", Namespace: "shop", Name: "api", Status: graph.Status{Ready: true},
		Manifest: map[string]graph.Value{"secrets[0].name": {Text: "a", Hash: "fp:1"}},
	}
	st.Nodes[sa.ID] = sa
	sa.Manifest = map[string]graph.Value{"automountServiceAccountToken": {Text: "false", Hash: "fp:2"}, "secrets[0].name": {Text: "a", Hash: "fp:1"}}
	apply(t, tr, st, at(0), sa)

	e := only(t, tr).Entries[0]
	if e.Summary != "changed (automountServiceAccountToken added)" || e.By != nil {
		t.Errorf("entry = %+v", e)
	}
}

// A Service and an Ingress touch the apps whose pods they send requests to,
// so a verdict can blame them (ADR-0016).
func TestRoutingTouchesApps(t *testing.T) {
	t.Parallel()
	st := cluster()
	add := func(n graph.Node) { st.Nodes[n.ID] = n }
	edge := func(ty graph.EdgeType, from, to string) { e := graph.NewEdge(ty, from, to); st.Edges[e.ID] = e }
	add(graph.Node{ID: "Service/shop/checkout", Kind: "Service", Namespace: "shop", Name: "checkout"})
	add(graph.Node{ID: "Ingress/shop/web", Kind: "Ingress", Namespace: "shop", Name: "web"})
	edge(graph.EdgeSelects, "Service/shop/checkout", "Pod/shop/checkout-1-a")
	edge(graph.EdgeRoutes, "Ingress/shop/web", "Service/shop/checkout")
	for _, id := range []string{"Service/shop/checkout", "Ingress/shop/web"} {
		kind, _, _ := strings.Cut(id, "/")
		if got := appsTouched(st, Entry{Object: id, Kind: kind}); !slices.Equal(got, []string{"Deployment/shop/checkout"}) {
			t.Errorf("%s touches %v", id, got)
		}
	}
}

// kubectl replace drops the revision annotation until the controller adds
// the next one, and the previous writer's record: the change still shows.
func TestTemplateChangeWithoutARevision(t *testing.T) {
	t.Parallel()
	st, tr := cluster(), New(MaxSets)
	api := st.Nodes["Deployment/shop/api"]
	api.Spec = &graph.Spec{Revision: "3", Containers: []graph.Container{{Name: "api", Image: "nginx:1.27", Env: []graph.EnvVar{{Name: "PROMO_BANNER", Value: "fp:a", Text: "on"}}}}}
	st.Nodes[api.ID] = api
	api.Spec = &graph.Spec{Containers: []graph.Container{{Name: "api", Image: "nginx:1.27"}}}
	apply(t, tr, st, at(0), api)
	e := only(t, tr).Entries[0]
	if e.What != "template" || e.Summary != "env PROMO_BANNER removed" {
		t.Errorf("entry = %+v", e)
	}
}
