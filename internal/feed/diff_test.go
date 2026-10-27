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
		{Path: "LOG_LEVEL", Op: Changed, From: "info", To: "verbose"},
		{Path: "NEW", Op: Added, To: "on"},
		{Path: "OLD", Op: Removed, From: "x"},
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
		{Path: "env LOG_LEVEL", Op: Changed, From: "info", To: "debug"},
		{Path: "env DB_URL", Op: Changed, From: "postgres://[redacted]@db", To: "postgres://[redacted]@db2", Note: "parts that look like credentials are hidden"},
		{Path: "env NEW", Op: Added, To: "1"},
	}
	if !slices.Equal(d.lines, want) {
		t.Errorf("diff = %+v", d.lines)
	}
}
