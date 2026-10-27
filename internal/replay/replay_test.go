package replay

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

var t0 = time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)

// recording builds what the recorder would hold for the ConfigMap story: the
// checkout app is healthy, its ConfigMap is edited, then a new pod
// crash-loops. Names are deliberately identifying.
func recording(t *testing.T) [][]byte {
	t.Helper()
	at := func(m float64) time.Time { return t0.Add(time.Duration(m * float64(time.Minute))) }
	g := graph.New()
	dep := graph.Node{
		ID: "Deployment/payments-prod/checkout", Kind: "Deployment", Namespace: "payments-prod", Name: "checkout", Created: at(-60),
		Labels: map[string]string{"team": "acme-payments"},
		Status: graph.Status{Ready: true, Replicas: &graph.Replicas{Desired: 2, Ready: 2, Updated: 2}},
		Spec: &graph.Spec{
			Revision: "1", ServiceAccount: "checkout-sa", Volumes: []string{"Secret/payments-tls", "PVC/checkout-cache"},
			Containers: []graph.Container{{
				Name: "checkout", Image: "registry.acme.internal/payments/checkout:4.2.0",
				Env: []graph.EnvVar{
					{Name: "PAYMENTS_GATEWAY_URL", Value: "fp:0123456789ab"},
					{Name: "ACME_DB_PASSWORD", From: "Secret/payments-db:acme-password"},
				},
				EnvFrom: []string{"ConfigMap/checkout-config"},
				Probes:  graph.Probes{Readiness: true, ReadinessCheck: "http GET :8080/payments/healthz"},
			}},
		},
		Change: &graph.Change{At: at(-60), By: "helm", Tool: "helm"},
		Origin: &graph.Origin{Tool: "helm", Name: "acme-payments"},
	}
	cm := graph.Node{
		ID: "ConfigMap/payments-prod/checkout-config", Kind: "ConfigMap", Namespace: "payments-prod", Name: "checkout-config", Created: at(-60),
		Change: &graph.Change{At: at(-60), By: "helm", Tool: "helm"},
	}
	pod := func(name string, created float64, ready bool, reason string) graph.Node {
		return graph.Node{
			ID: "Pod/payments-prod/" + name, Kind: "Pod", Namespace: "payments-prod", Name: name, Created: at(created), Parent: "Node/ip-10-0-3-17",
			Status: graph.Status{Ready: ready, Reason: reason, Since: at(created)},
		}
	}
	g.UpsertNode(graph.Node{ID: "Node/ip-10-0-3-17", Kind: "Node", Name: "ip-10-0-3-17", Status: graph.Status{Ready: true}})
	g.UpsertNode(graph.Node{ID: "Namespace/payments-prod", Kind: "Namespace", Name: "payments-prod", Status: graph.Status{Ready: true}})
	g.UpsertNode(dep)
	g.UpsertNode(cm)
	for _, p := range []graph.Node{pod("checkout-7d9f-a", -60, true, ""), pod("checkout-7d9f-b", -60, true, "")} {
		g.UpsertNode(p)
		g.UpsertEdge(graph.NewEdge(graph.EdgeOwns, dep.ID, p.ID))
		g.UpsertEdge(graph.NewEdge(graph.EdgeMounts, p.ID, cm.ID))
	}

	var msgs [][]byte
	add := func(m graph.Message, ts time.Time) {
		m.TS = ts
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, raw)
	}
	_, _, snap := g.FlushAndSnapshot()
	add(snap, at(-1))

	cm.Change = &graph.Change{At: at(0), By: "kubectl-patch", Tool: "kubectl", Via: "patch"} // the edit
	g.UpsertNode(cm)
	d, _ := g.Flush()
	add(d, at(0))

	g.RemoveNode("Pod/payments-prod/checkout-7d9f-a") // replaced, picks up the bad config
	g.RemoveEdge(graph.NewEdge(graph.EdgeMounts, "Pod/payments-prod/checkout-7d9f-a", cm.ID).ID)
	p := pod("checkout-7d9f-c", 1, false, "CrashLoopBackOff")
	p.Status.Restarts = 3
	p.Status.Container, p.Status.Exit = "checkout", 1
	p.Status.Message = "Back-off pulling registry.acme.internal on ip-10-0-3-17" // Kubernetes messages name things
	g.UpsertNode(p)
	g.UpsertEdge(graph.NewEdge(graph.EdgeOwns, dep.ID, p.ID))
	g.UpsertEdge(graph.NewEdge(graph.EdgeMounts, p.ID, cm.ID))
	dep.Status = graph.Status{Ready: false, Replicas: &graph.Replicas{Desired: 2, Ready: 1, Updated: 2}}
	g.UpsertNode(dep)
	d, _ = g.Flush()
	add(d, at(2)) // two minutes in: past the grace period
	return msgs
}

func write(t *testing.T, anonymize bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	err := Write(t.Context(), &buf, recording(t), Options{From: t0.Add(-time.Minute), To: t0.Add(3 * time.Minute), Cluster: "acme-prod-eu", Version: "v0.2.0", Anonymize: anonymize})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	f, err := Read(bytes.NewReader(write(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Cluster != "acme-prod-eu" || f.Header.Anonymized || f.Header.Kubexray != "v0.2.0" || len(f.Messages) != 3 {
		t.Errorf("header = %+v, %d messages", f.Header, len(f.Messages))
	}
	last := f.Incidents[len(f.Incidents)-1]
	if len(last.Incidents) != 1 || last.Incidents[0].Causes[0].Object != "ConfigMap/payments-prod/checkout-config" {
		t.Errorf("incidents = %+v; want the ConfigMap edit as the cause", last.Incidents)
	}
	if len(f.Changes) == 0 || f.Changes[0].Object != "ConfigMap/payments-prod/checkout-config" {
		t.Errorf("changes = %+v", f.Changes)
	}
	// The change feed: the edit, judged by what it did.
	if len(f.Feed) != 1 || f.Feed[0].Title != "ConfigMap checkout-config edited" || f.Feed[0].Verdict != "broke" || f.Feed[0].Incident != "Deployment/payments-prod/checkout" {
		t.Errorf("feed = %+v", f.Feed)
	}
}

func TestAnonymizeLeavesNothingIdentifying(t *testing.T) {
	t.Parallel()
	zr, err := gzip.NewReader(bytes.NewReader(write(t, true)))
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(zr)
	for _, secret := range []string{"payments", "checkout", "acme", "ip-10-0-3-17", "registry", "7d9f", "team", "GATEWAY", "PASSWORD", "healthz", "-sa", "cache"} {
		if strings.Contains(string(text), secret) {
			t.Errorf("anonymized replay still contains %q", secret)
		}
	}
	// The story survives: kinds, the image tag, the tool, the cause.
	for _, kept := range []string{`"kind":"Deployment"`, ":4.2.0", `"tool":"kubectl"`, "CrashLoopBackOff", "was edited", "fp:0123456789ab", "http GET :8080/…", `"Secret/secret-`} {
		if !strings.Contains(string(text), kept) {
			t.Errorf("anonymized replay lost %q", kept)
		}
	}

	// And it's a valid replay whose IDs still line up.
	f, err := Read(bytes.NewReader(write(t, true)))
	if err != nil {
		t.Fatalf("anonymized replay doesn't read back: %v", err)
	}
	if f.Header.Cluster != "cluster" || !f.Header.Anonymized {
		t.Errorf("header = %+v", f.Header)
	}
	inc := f.Incidents[len(f.Incidents)-1].Incidents[0]
	if inc.Namespace != "namespace-1" || !strings.HasPrefix(inc.Name, "deployment-") || inc.Causes[0].Kind != cause.KindConfig {
		t.Errorf("incident = %+v", inc)
	}
}

func TestAnonymizerIsConsistent(t *testing.T) {
	t.Parallel()
	a := newAnonymizer()
	first, again, other := a.id("Pod/shop/api-1"), a.id("Pod/shop/api-1"), a.id("Pod/shop/api-2")
	if first != again || first == other {
		t.Error("the same name must map to the same replacement, different names to different ones")
	}
	if got := a.id("Deployment/shop/api"); got != "Deployment/namespace-1/deployment-1" {
		t.Errorf("id = %q", got)
	}
	if got := a.edgeID("owns:Deployment/shop/api:Pod/shop/api-1"); got != "owns:Deployment/namespace-1/deployment-1:Pod/namespace-1/pod-1" {
		t.Errorf("edge id = %q", got)
	}
	// The same repository keeps its replacement across tags, so "the tag
	// changed" stays visible; digests are dropped (they identify too).
	for _, tt := range []struct{ in, want string }{
		{"registry.acme.internal:5000/team/app:1.2.3", "image-1:1.2.3"},
		{"registry.acme.internal:5000/team/app:1.3.0", "image-1:1.3.0"},
		{"registry.acme.internal:5000/team/app", "image-1"},
		{"nginx@sha256:abc", "image-2@digest-1"},
		{"nginx@sha256:def", "image-2@digest-2"},
		{"app:1.2@sha256:abc", "image-3:1.2@digest-1"},
	} {
		if got := a.image(tt.in); got != tt.want {
			t.Errorf("image(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAnonymizerKeepsToolsNotNames(t *testing.T) {
	t.Parallel()
	a := newAnonymizer()
	for _, tt := range []struct {
		in   graph.Change
		want string // By after anonymizing
	}{
		{graph.Change{By: "kubectl-patch", Tool: "kubectl", Via: "patch"}, "kubectl-patch"},
		{graph.Change{By: "kubectl-ivan-hotfix", Tool: "kubectl", Via: "ivan-hotfix"}, "kubectl"},
		{graph.Change{By: "argocd-acme-prod", Tool: "argocd"}, "argocd"},
		{graph.Change{By: "acme-billing-controller-manager", Tool: "controller"}, "manager-1"},
		{graph.Change{By: "terraform-acme", Tool: "other"}, "manager-2"},
	} {
		got := a.change(tt.in)
		if got.By != tt.want || strings.Contains(got.Via, "ivan") {
			t.Errorf("change(%q) = %+v, want By %q", tt.in.By, got, tt.want)
		}
	}
}

func TestAnonymizerKeepsWellKnownNames(t *testing.T) {
	t.Parallel()
	a := newAnonymizer()
	for in, want := range map[string]string{
		"ConfigMap/shop/kube-root-ca.crt":   "ConfigMap/namespace-1/kube-root-ca.crt",
		"Deployment/kube-system/coredns":    "Deployment/kube-system/coredns",
		"Service/default/kubernetes":        "Service/default/kubernetes",
		"Deployment/kube-system/acme-agent": "Deployment/kube-system/deployment-1",
	} {
		if got := a.id(in); got != want {
			t.Errorf("id(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIncidentsAppearAfterTheGracePeriodWithoutNewChanges(t *testing.T) {
	t.Parallel()
	// The pod starts crash-looping at +0 and nothing else changes; the
	// incident must be stamped about 30 s later, not at the end of the window.
	g := graph.New()
	g.UpsertNode(graph.Node{ID: "Deployment/shop/api", Kind: "Deployment", Namespace: "shop", Name: "api", Status: graph.Status{Replicas: &graph.Replicas{Desired: 1, Ready: 1, Updated: 1}, Ready: true}})
	_, _, snap := g.FlushAndSnapshot()
	snap.TS = t0
	g.UpsertNode(graph.Node{ID: "Pod/shop/api-x", Kind: "Pod", Namespace: "shop", Name: "api-x", Created: t0, Status: graph.Status{Reason: "CrashLoopBackOff", Restarts: 1, Since: t0}})
	g.UpsertEdge(graph.NewEdge(graph.EdgeOwns, "Deployment/shop/api", "Pod/shop/api-x"))
	d, _ := g.Flush()
	d.TS = t0
	raw := func(m graph.Message) []byte { b, _ := json.Marshal(m); return b }
	var buf bytes.Buffer
	if err := Write(t.Context(), &buf, [][]byte{raw(snap), raw(d)}, Options{From: t0, To: t0.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	f, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range f.Incidents {
		if len(m.Incidents) > 0 {
			if late := m.TS.Sub(t0); late < 30*time.Second || late > 40*time.Second {
				t.Errorf("incident first written %v after it began, want 30-40 s", late)
			}
			return
		}
	}
	t.Error("the incident never appeared")
}

func TestWriteStopsWhenCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Write(ctx, io.Discard, recording(t), Options{To: t0}); !errors.Is(err, context.Canceled) {
		t.Errorf("Write = %v, want context.Canceled", err)
	}
}

func gz(lines ...string) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	for _, l := range lines {
		_, _ = w.Write([]byte(l + "\n"))
	}
	_ = w.Close()
	return buf.Bytes()
}

const header = `{"v":1,"type":"header","from":"2026-10-23T14:00:00Z","to":"2026-10-23T14:05:00Z"}`

func TestReadRejectsBadFiles(t *testing.T) {
	t.Parallel()
	snapshot := `{"v":1,"type":"snapshot","seq":1,"ts":"2026-10-23T14:00:00Z","nodes":[],"edges":[]}`
	tests := map[string][]byte{
		"not gzip":        []byte("hello"),
		"empty":           gz(),
		"no header first": gz(snapshot),
		"wrong version":   gz(`{"v":2,"type":"header"}`),
		"unknown type":    gz(header, snapshot, `{"v":1,"type":"exploit"}`),
		"delta first":     gz(header, `{"v":1,"type":"delta","seq":2,"ts":"2026-10-23T14:00:00Z","ops":[]}`),
		"bad op":          gz(header, snapshot, `{"v":1,"type":"delta","seq":2,"ts":"2026-10-23T14:00:00Z","ops":[{"op":"upsertNode"}]}`),
		"gap":             gz(header, snapshot, `{"v":1,"type":"delta","seq":5,"ts":"2026-10-23T14:00:00Z","ops":[]}`),
		"no snapshot":     gz(header),
		"line too long":   gz(header, `{"v":1,"type":"changes","changes":[],"x":"`+strings.Repeat("a", MaxLine)+`"}`),
		"not json":        gz(header, "{"),
	}
	for name, file := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Read(bytes.NewReader(file)); err == nil {
				t.Error("Read accepted it")
			}
		})
	}
}

func TestReadRejectsHugeFiles(t *testing.T) {
	t.Parallel()
	// The real limit is 50 MiB; the logic is the same with a small one.
	const limit = 1 << 20
	line := `{"v":1,"type":"changes","changes":[]}`
	lines := make([]string, 0, limit/len(line)+2)
	lines = append(lines, header)
	for range limit/len(line) + 1 {
		lines = append(lines, line)
	}
	if _, err := read(bytes.NewReader(gz(lines...)), limit); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("read = %v, want a size error", err)
	}
	if _, err := read(bytes.NewReader(gz(lines[:10]...)), limit); err == nil || strings.Contains(err.Error(), "larger than") {
		t.Errorf("a small file must fail for its content (no snapshot), not its size: %v", err)
	}
}

func TestWriteNeedsMessages(t *testing.T) {
	t.Parallel()
	if err := Write(t.Context(), io.Discard, nil, Options{}); err == nil {
		t.Error("Write of nothing succeeded")
	}
}
