package replay

import (
	"fmt"
	"strings"

	"github.com/kubexray/kubexray/internal/graph"
)

// anonymizer replaces identifying strings consistently within one file
// (ADR-0008, section 3): the same name always gets the same replacement, and
// replacements are per-file counters, not hashes, so nothing can be reversed.
type anonymizer struct {
	seen     map[string]map[string]string // namespace → original → replacement
	counters map[string]int
}

func newAnonymizer() *anonymizer {
	return &anonymizer{seen: map[string]map[string]string{}, counters: map[string]int{}}
}

// Standard Kubernetes names identify nothing, and code matches some of them
// literally (kube-root-ca.crt is skipped as noise, system namespaces are
// collapsed in the UI), so they're kept.
var (
	wellKnownNamespaces = map[string]bool{"default": true, "kube-system": true, "kube-public": true, "kube-node-lease": true, "local-path-storage": true}
	wellKnownNames      = map[string]bool{"kube-root-ca.crt": true, "kubernetes": true, "coredns": true, "kube-dns": true, "kube-proxy": true}
)

func (a *anonymizer) replace(space, original, prefix string) string {
	if original == "" {
		return ""
	}
	m := a.seen[space]
	if m == nil {
		m = map[string]string{}
		a.seen[space] = m
	}
	if r, ok := m[original]; ok {
		return r
	}
	a.counters[space]++
	r := fmt.Sprintf("%s-%d", prefix, a.counters[space])
	m[original] = r
	return r
}

func (a *anonymizer) namespace(ns string) string {
	if wellKnownNamespaces[ns] {
		return ns
	}
	return a.replace("namespace", ns, "namespace")
}

func (a *anonymizer) name(kind, name string) string {
	switch {
	case kind == "Namespace":
		return a.namespace(name)
	case wellKnownNames[name]:
		return name
	}
	prefix := strings.ToLower(kind)
	if kind == "PersistentVolumeClaim" {
		prefix = "volume"
	}
	return a.replace("name/"+kind, name, prefix)
}

// id rewrites a node ID (Kind/name or Kind/namespace/name).
func (a *anonymizer) id(id string) string {
	parts := strings.Split(id, "/")
	switch len(parts) {
	case 2:
		return graph.ID(parts[0], "", a.name(parts[0], parts[1]))
	case 3:
		return graph.ID(parts[0], a.namespace(parts[1]), a.name(parts[0], parts[2]))
	default:
		return a.replace("id", id, "object")
	}
}

// edgeID rewrites "<type>:<from>:<to>"; node IDs never contain ':'.
func (a *anonymizer) edgeID(id string) string {
	t, rest, ok := strings.Cut(id, ":")
	from, to, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 {
		return a.replace("edge", id, "edge")
	}
	return t + ":" + a.id(from) + ":" + a.id(to)
}

// image replaces the repository and digest but keeps the tag: "the tag
// changed" is the story. A digest change stays visible as a different
// replacement.
func (a *anonymizer) image(image string) string {
	ref, digest, _ := strings.Cut(image, "@")
	repo, tag := ref, ""
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i:]
	}
	out := a.replace("image", repo, "image") + tag
	if digest != "" {
		out += "@" + a.replace("digest", digest, "digest")
	}
	return out
}

// Known kubectl subcommands; anything else in a kubectl field manager name
// (kubectl-ivan-hotfix) could identify someone.
var kubectlVerbs = map[string]bool{
	"apply": true, "patch": true, "edit": true, "set": true, "scale": true, "rollout": true, "create": true,
	"replace": true, "label": true, "annotate": true, "debug": true, "expose": true, "autoscale": true,
}

// change keeps only the tool family: field manager names are free-form and
// can carry company or person names (acme-billing-controller-manager).
func (a *anonymizer) change(c graph.Change) graph.Change {
	switch c.Tool {
	case "kubectl", "helm", "argocd", "flux":
		c.By = c.Tool
	default:
		c.By = a.replace("manager", c.By, "manager")
	}
	if c.Tool == "kubectl" && kubectlVerbs[c.Via] {
		c.By = "kubectl-" + c.Via
	} else {
		c.Via = ""
	}
	return c
}

func (a *anonymizer) node(n *graph.Node) {
	kind := n.Kind
	n.ID = a.id(n.ID)
	n.Namespace = a.namespace(n.Namespace)
	n.Name = a.name(kind, n.Name)
	if n.Parent != "" {
		n.Parent = a.id(n.Parent)
	}
	n.Labels = a.labels(n.Labels)
	n.Selector = a.labels(n.Selector) // the same replacements, so it still matches
	// Kubernetes messages name nodes, volumes and keys: dropped (ADR-0009).
	n.Status.Message = ""
	n.Status.Container = a.replace("container", n.Status.Container, "container")
	if n.Spec != nil {
		spec := *n.Spec
		spec.Containers = make([]graph.Container, len(n.Spec.Containers))
		for i, c := range n.Spec.Containers {
			c.Name = a.replace("container", c.Name, "container")
			c.Image = a.image(c.Image)
			c.Env = a.env(c.Env)
			c.EnvFrom = a.refs(c.EnvFrom)
			c.Probes.LivenessCheck = probeCheck(c.Probes.LivenessCheck)
			c.Probes.ReadinessCheck = probeCheck(c.Probes.ReadinessCheck)
			c.Probes.StartupCheck = probeCheck(c.Probes.StartupCheck)
			spec.Containers[i] = c
		}
		spec.Labels = a.labels(spec.Labels)
		spec.Volumes = a.refs(spec.Volumes)
		spec.ServiceAccount = a.name("ServiceAccount", spec.ServiceAccount)
		n.Spec = &spec
	}
	if n.Change != nil {
		c := a.change(*n.Change)
		n.Change = &c
	}
	if n.Origin != nil {
		o := *n.Origin
		o.Name = a.replace("release", o.Name, "release")
		n.Origin = &o
	}
}

// env renames environment variables and the objects and keys their values
// come from (ADR-0015). Fingerprints stay: they say nothing by themselves.
func (a *anonymizer) env(in []graph.EnvVar) []graph.EnvVar {
	if len(in) == 0 {
		return in
	}
	out := make([]graph.EnvVar, len(in))
	for i, e := range in {
		e.Name = a.replace("env", e.Name, "VAR")
		if obj, key, ok := strings.Cut(e.From, ":"); ok && strings.Contains(obj, "/") {
			e.From = a.ref(obj) + ":" + a.replace("key", key, "key")
		}
		out[i] = e
	}
	return out
}

// refs renames references like "ConfigMap/api-config", as their objects are.
func (a *anonymizer) refs(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, r := range in {
		out[i] = a.ref(r)
	}
	return out
}

func (a *anonymizer) ref(r string) string {
	kind, name, ok := strings.Cut(r, "/")
	if !ok {
		return r
	}
	if kind == "PVC" {
		return kind + "/" + a.name("PersistentVolumeClaim", name)
	}
	return kind + "/" + a.name(kind, name)
}

// probeCheck keeps what kind of check a probe makes and its port, not its
// path, which can name things.
func probeCheck(c string) string {
	if before, _, ok := strings.Cut(c, "/"); ok && strings.HasPrefix(c, "http ") {
		return before + "/…"
	}
	return c
}

func (a *anonymizer) labels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[a.replace("label-key", k, "label")] = a.replace("label-value", v, "value")
	}
	return out
}

func (a *anonymizer) edge(e *graph.Edge) {
	*e = graph.NewEdge(e.Type, a.id(e.From), a.id(e.To))
}

// message anonymizes a snapshot or delta in place.
func (a *anonymizer) message(m *graph.Message) {
	for i := range m.Nodes {
		a.node(&m.Nodes[i])
	}
	for i := range m.Edges {
		a.edge(&m.Edges[i])
	}
	for i := range m.Ops {
		op := &m.Ops[i]
		switch {
		case op.Node != nil:
			n := *op.Node
			a.node(&n)
			op.Node = &n
		case op.Edge != nil:
			e := *op.Edge
			a.edge(&e)
			op.Edge = &e
		case op.Op == graph.OpRemoveNode:
			op.ID = a.id(op.ID)
		case op.Op == graph.OpRemoveEdge:
			op.ID = a.edgeID(op.ID)
		}
	}
}
