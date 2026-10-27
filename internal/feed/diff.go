package feed

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kubexray/kubexray/internal/graph"
)

// DiffLine is one difference in an object, before and after a change
// (ADR-0017). Values kubexray doesn't record, or masks, are described,
// not shown.
type DiffLine struct {
	Path string `json:"path"` // e.g. "env PROMO_BANNER", "image", "readiness probe"
	Op   string `json:"op"`   // added, removed or changed
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Note string `json:"note,omitempty"` // e.g. "values aren't recorded"
}

// The ops a diff line can have.
const (
	Added   = "added"
	Removed = "removed"
	Changed = "changed"
)

// objectDiff lists what changed in a workload: its pod template, its
// replicas and its release version.
func objectDiff(old, n graph.Node) []DiffLine {
	var d differ
	if old.Spec != nil && n.Spec != nil {
		d.template(*old.Spec, *n.Spec)
	}
	if r0, r1 := old.Status.Replicas, n.Status.Replicas; r0 != nil && r1 != nil && r0.Desired != r1.Desired {
		d.changed("replicas", fmt.Sprint(r0.Desired), fmt.Sprint(r1.Desired))
	}
	if v := versionChange(old, n); v != nil {
		d.value("release version", v.From, v.To)
	}
	return d.lines
}

type differ struct{ lines []DiffLine }

func (d *differ) add(l DiffLine) { d.lines = append(d.lines, l) }

func (d *differ) changed(path, from, to string) {
	d.add(DiffLine{Path: path, Op: Changed, From: from, To: to})
}

// value records a field that may be set, unset or changed.
func (d *differ) value(path, from, to string) {
	switch {
	case from == to:
	case from == "":
		d.add(DiffLine{Path: path, Op: Added, To: to})
	case to == "":
		d.add(DiffLine{Path: path, Op: Removed, From: from})
	default:
		d.changed(path, from, to)
	}
}

// list records items added to or removed from a list.
func (d *differ) list(path string, from, to []string) {
	for _, x := range to {
		if !slices.Contains(from, x) {
			d.add(DiffLine{Path: path, Op: Added, To: x})
		}
	}
	for _, x := range from {
		if !slices.Contains(to, x) {
			d.add(DiffLine{Path: path, Op: Removed, From: x})
		}
	}
}

func (d *differ) template(old, n graph.Spec) {
	byName := func(cs []graph.Container) map[string]graph.Container {
		m := map[string]graph.Container{}
		for _, c := range cs {
			m[key(c)] = c
		}
		return m
	}
	was, now := byName(old.Containers), byName(n.Containers)
	several := len(was) > 1 || len(now) > 1
	for _, c := range n.Containers {
		if c.Ephemeral {
			continue // kubectl debug containers come and go
		}
		p, ok := was[key(c)]
		label := ""
		if several {
			label = "container " + c.Name + ": "
		}
		if !ok {
			d.add(DiffLine{Path: strings.TrimSuffix(label, ": "), Op: Added, To: c.Image})
			continue
		}
		d.container(label, p, c)
	}
	for _, c := range old.Containers {
		if _, ok := now[key(c)]; !ok {
			d.add(DiffLine{Path: "container " + c.Name, Op: Removed, From: c.Image})
		}
	}
	d.value("service account", old.ServiceAccount, n.ServiceAccount)
	d.list("volume", old.Volumes, n.Volumes)
}

func (d *differ) container(label string, p, c graph.Container) {
	d.value(label+"image", p.Image, c.Image)
	d.value(label+"cpu request", p.Requests.CPU, c.Requests.CPU)
	d.value(label+"memory request", p.Requests.Memory, c.Requests.Memory)
	d.value(label+"cpu limit", p.Limits.CPU, c.Limits.CPU)
	d.value(label+"memory limit", p.Limits.Memory, c.Limits.Memory)
	for _, k := range []struct {
		name     string
		was, now bool
		from, to string
	}{
		{"liveness probe", p.Probes.Liveness, c.Probes.Liveness, p.Probes.LivenessCheck, c.Probes.LivenessCheck},
		{"readiness probe", p.Probes.Readiness, c.Probes.Readiness, p.Probes.ReadinessCheck, c.Probes.ReadinessCheck},
		{"startup probe", p.Probes.Startup, c.Probes.Startup, p.Probes.StartupCheck, c.Probes.StartupCheck},
	} {
		switch {
		case k.now && !k.was:
			d.add(DiffLine{Path: label + k.name, Op: Added, To: k.to})
		case k.was && !k.now:
			d.add(DiffLine{Path: label + k.name, Op: Removed, From: k.from})
		case k.from != k.to:
			d.changed(label+k.name, k.from, k.to)
		}
	}
	d.env(label, p.Env, c.Env)
	d.list(label+"env from", p.EnvFrom, c.EnvFrom)
	if p.Command != c.Command {
		d.recorded(label+"command", p.Command != "", c.Command != "", p.Run, c.Run, "contents aren't recorded")
	}
	d.list(label+"port", p.Ports, c.Ports)
}

// recorded reports a value that changed, with its text when it was
// recorded, and why not when it wasn't.
func (d *differ) recorded(path string, was, now bool, from, to graph.Value, unread string) {
	l := DiffLine{Path: path, Op: Changed, From: from.Text, To: to.Text}
	switch {
	case !was:
		l.Op, l.From = Added, ""
	case !now:
		l.Op, l.To = Removed, ""
	}
	l.Note = note(unread, from, to, l.Op)
	d.add(l)
}

// note says why a value isn't shown, or isn't shown whole.
func note(unread string, from, to graph.Value, op string) string {
	m := to.Masked
	if op == Removed || m == "" {
		m = cmp.Or(m, from.Masked)
	}
	switch m {
	case "name":
		return "hidden: the name looks like a credential"
	case "value":
		return "parts that look like credentials are hidden"
	case "size":
		return "too long to show whole"
	case "binary":
		return "binary: only that it changed"
	}
	if (op != Added && from.Text == "") || (op != Removed && to.Text == "") {
		return unread
	}
	return ""
}

// dataDiff compares a ConfigMap's keys, with their values when recorded.
func dataDiff(was, now map[string]graph.Value) []DiffLine {
	var d differ
	for _, k := range slices.Sorted(maps.Keys(now)) {
		p, ok := was[k]
		if v := now[k]; !ok || p.Hash != v.Hash {
			d.recorded(k, ok, true, p, v, "values aren't recorded")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(was)) {
		if _, ok := now[k]; !ok {
			d.recorded(k, true, false, was[k], graph.Value{}, "values aren't recorded")
		}
	}
	return d.lines
}

// env compares environment variables by name. A literal value shows when
// it was recorded; otherwise only its fingerprint says it changed.
func (d *differ) env(label string, was, now []graph.EnvVar) {
	find := func(vs []graph.EnvVar, name string) (graph.EnvVar, bool) {
		i := slices.IndexFunc(vs, func(v graph.EnvVar) bool { return v.Name == name })
		if i < 0 {
			return graph.EnvVar{}, false
		}
		return vs[i], true
	}
	for _, v := range now {
		p, ok := find(was, v.Name)
		path := label + "env " + v.Name
		switch {
		case !ok && v.From == "" && v.Text != "":
			d.add(DiffLine{Path: path, Op: Added, To: v.Text, Note: note("", graph.Value{}, envValue(v), Added)})
		case !ok:
			d.add(DiffLine{Path: path, Op: Added, To: source(v), Note: literal(v)})
		case p.From != v.From:
			d.changed(path, source(p), source(v))
		case p.Value != v.Value:
			d.recorded(path, true, true, envValue(p), envValue(v), "its value changed; values aren't recorded")
		}
	}
	for _, v := range was {
		if _, ok := find(now, v.Name); !ok {
			d.add(DiffLine{Path: label + "env " + v.Name, Op: Removed, From: source(v)})
		}
	}
}

func envValue(v graph.EnvVar) graph.Value {
	return graph.Value{Text: v.Text, Hash: v.Value, Masked: v.Masked}
}

// source says where an environment variable's value comes from.
func source(v graph.EnvVar) string {
	if v.From == "" {
		return "a value"
	}
	return v.From
}

func literal(v graph.EnvVar) string {
	if v.From == "" {
		return note("values aren't recorded", graph.Value{}, envValue(v), Added)
	}
	return ""
}

// describe sums up a diff in a few words, for the feed line: "env
// PROMO_BANNER added", "LOG_LEVEL info → verbose", "command changed and 2
// more".
func describe(lines []DiffLine) string {
	var parts []string
	for _, l := range lines {
		if l.Path == "replicas" || l.Path == "release version" {
			continue
		}
		if l.Op == Changed && l.From != "" && l.To != "" && len(l.From)+len(l.To) <= 40 && !strings.ContainsAny(l.From+l.To, "\n") {
			parts = append(parts, l.Path+" "+l.From+" → "+l.To)
			continue
		}
		parts = append(parts, l.Path+" "+l.Op)
	}
	switch len(parts) {
	case 0:
		return ""
	case 1, 2:
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%s, %s and %d more", parts[0], parts[1], len(parts)-2)
	}
}
