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

func (a *anonymizer) namespace(ns string) string { return a.replace("namespace", ns, "namespace") }

func (a *anonymizer) name(kind, name string) string {
	if kind == "Namespace" {
		return a.namespace(name)
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

// image keeps the tag ("the tag changed" is the story) and replaces the rest.
func (a *anonymizer) image(image string) string {
	repo, tag := image, ""
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		repo, tag = image[:i], image[i:]
	}
	if at := strings.Index(repo, "@"); at >= 0 { // digests identify too
		repo, tag = repo[:at], ""
	}
	return a.replace("image", repo, "image") + tag
}

// knownTools are change tools that name software, not a person or system.
var knownTools = map[string]bool{"kubectl": true, "helm": true, "argocd": true, "flux": true, "controller": true}

func (a *anonymizer) node(n *graph.Node) {
	kind := n.Kind
	n.ID = a.id(n.ID)
	n.Namespace = a.namespace(n.Namespace)
	n.Name = a.name(kind, n.Name)
	if n.Parent != "" {
		n.Parent = a.id(n.Parent)
	}
	if len(n.Labels) > 0 {
		labels := make(map[string]string, len(n.Labels))
		for k, v := range n.Labels {
			labels[a.replace("label-key", k, "label")] = a.replace("label-value", v, "value")
		}
		n.Labels = labels
	}
	if n.Spec != nil {
		spec := *n.Spec
		spec.Containers = make([]graph.Container, len(n.Spec.Containers))
		for i, c := range n.Spec.Containers {
			c.Name = a.replace("container", c.Name, "container")
			c.Image = a.image(c.Image)
			spec.Containers[i] = c
		}
		n.Spec = &spec
	}
	if n.Change != nil && !knownTools[n.Change.Tool] {
		c := *n.Change
		c.By = a.replace("manager", c.By, "manager")
		n.Change = &c
	}
	if n.Origin != nil {
		o := *n.Origin
		o.Name = a.replace("release", o.Name, "release")
		n.Origin = &o
	}
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
