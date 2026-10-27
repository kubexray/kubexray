// Package feed is the change feed (ADR-0012): every meaningful change to the
// cluster, grouped into change sets, each with a verdict on what it did.
// A Tracker observes the deltas of ADR-0003 as they're emitted, so it knows
// before and after; Assess updates verdicts from the cause engine's
// incidents.
package feed

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// Verdict is what a change set did.
type Verdict string

// Verdicts (ADR-0012, section 3).
const (
	Settling Verdict = "settling" // its apps are still rolling out
	Healthy  Verdict = "healthy"  // its apps were ready and rolled out afterwards
	Broke    Verdict = "broke"    // the cause engine names it as an incident's cause
	Fixed    Verdict = "fixed"    // made during an incident on its apps, which then ended
	Stuck    Verdict = "stuck"    // its apps didn't settle within SettleWindow
	NoEffect Verdict = "none"     // it touches no app
)

// Entry is one change to one object.
type Entry struct {
	At        time.Time     `json:"at"`
	Object    string        `json:"object"`
	Kind      string        `json:"kind"`
	Name      string        `json:"name"`
	Namespace string        `json:"namespace,omitempty"`
	What      string        `json:"what"`    // created, deleted, image, resources, probes, template, replicas, edited, changed, ready, notready, cordoned, uncordoned
	Summary   string        `json:"summary"` // e.g. "image nginx:1.27 → nginx:1.29"
	By        *graph.Change `json:"by,omitempty"`
	Origin    *graph.Origin `json:"origin,omitempty"`
	Drift     string        `json:"drift,omitempty"` // a hand edit on an object a GitOps tool or Helm manages
	Version   *Versions     `json:"version,omitempty"`
	// Diff is what changed in the object, before and after (ADR-0015); on
	// the first of an object's entries in a change.
	Diff []DiffLine `json:"diff,omitempty"`
}

// Versions is a Helm chart or app version change read from standard labels.
type Versions struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// Set is a group of entries made together: one Helm upgrade, one sync, one
// edit.
type Set struct {
	ID        string        `json:"id"`
	At        time.Time     `json:"at"` // the first entry
	Last      time.Time     `json:"last"`
	Title     string        `json:"title"`
	Namespace string        `json:"namespace,omitempty"`
	By        *graph.Change `json:"by,omitempty"`
	Origin    *graph.Origin `json:"origin,omitempty"`
	Entries   []Entry       `json:"entries"`
	Apps      []string      `json:"apps,omitempty"` // the apps it touches, directly or through what they use
	Verdict   Verdict       `json:"verdict"`
	Note      string        `json:"note,omitempty"`     // e.g. "settled in 48s", "checkout is degraded 1m later"
	Incident  string        `json:"incident,omitempty"` // the app it broke
	Ended     time.Time     `json:"ended,omitzero"`     // when the incident it caused ended

	during    map[string]bool // apps that had an incident when it was made
	healthAt  time.Time       // when it was first seen settled
	disturbed bool            // one of its apps was seen unsettled since
}

// Observe records the entries in a delta, comparing with prev, the state it
// applies to. Snapshots start over from a new baseline (a restart) and add
// no entries.
func (t *Tracker) Observe(prev *graph.State, m graph.Message) {
	if m.Type != graph.MessageDelta || prev == nil {
		return
	}
	var entries []Entry
	for _, op := range m.Ops {
		switch {
		case op.Op == graph.OpUpsertNode && op.Node != nil:
			old, existed := prev.Nodes[op.Node.ID]
			entries = append(entries, compare(old, *op.Node, existed, m.TS)...)
		case op.Op == graph.OpRemoveNode:
			if old, ok := prev.Nodes[op.ID]; ok && tracked(old) {
				entries = append(entries, entry(old, m.TS, "deleted", removedWord(old), nil))
			}
		}
	}
	if len(entries) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range entries {
		t.add(prev, e)
	}
}

// recentSets is how many of the newest sets a change may join.
const recentSets = 20

// add files an entry into the newest matching set, or a new one.
func (t *Tracker) add(st *graph.State, e Entry) {
	apps := appsTouched(st, e)
	// The newest recent set it belongs with: another author's change in
	// between (an autoscaler) doesn't split a Helm upgrade.
	for i := len(t.sets) - 1; i >= 0 && i >= len(t.sets)-recentSets; i-- {
		if s := t.sets[i]; together(s, e) {
			s.Entries = append(s.Entries, e)
			s.Last = e.At
			s.Apps = union(s.Apps, apps)
			s.Title = title(s)
			return
		}
	}
	t.nextID++
	s := &Set{
		ID: fmt.Sprintf("%d-%d", e.At.UnixMilli(), t.nextID), At: e.At, Last: e.At, Namespace: e.Namespace,
		By: e.By, Origin: e.Origin, Entries: []Entry{e}, Apps: apps, during: map[string]bool{},
	}
	for _, a := range apps {
		if t.ongoing[a] {
			s.during[a] = true
		}
	}
	s.Title = title(s)
	t.sets = append(t.sets, s)
	if len(t.sets) > t.limit {
		t.sets[0] = nil
		t.sets = t.sets[1:]
	}
}

// together reports whether an entry belongs to a set: the same author (or
// none for both), the same release or namespace, within Window.
func together(s *Set, e Entry) bool {
	if e.At.Sub(s.Last) > Window {
		return false
	}
	sameAuthor := (s.By == nil && e.By == nil) || (s.By != nil && e.By != nil && s.By.By == e.By.By)
	sameRelease := s.Origin != nil && e.Origin != nil && *s.Origin == *e.Origin
	return sameAuthor && (sameRelease || s.Namespace == e.Namespace)
}

// Seed puts the sets of a tracker filled from history (Rebuild) before the
// ones this tracker has seen since.
func (t *Tracker) Seed(history *Tracker) {
	history.mu.Lock()
	old := slices.Clone(history.sets)
	history.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sets = append(old, t.sets...)
	if over := len(t.sets) - t.limit; over > 0 {
		t.sets = slices.Clone(t.sets[over:])
	}
}

// Sets returns the sets made since a time, newest first. They're copies.
func (t *Tracker) Sets(since time.Time) []Set {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Set
	for i := len(t.sets) - 1; i >= 0; i-- {
		s := t.sets[i]
		if s.Last.Before(since) {
			break
		}
		c := *s
		c.Entries = slices.Clone(s.Entries)
		c.Apps = slices.Clone(s.Apps)
		out = append(out, c)
	}
	return out
}

// appKinds are the kinds a verdict is about.
var appKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "Job": true}

// tracked are the kinds that make entries (ADR-0012, section 1).
func tracked(n graph.Node) bool {
	if n.Kind == "Secret" && strings.HasPrefix(n.Name, "sh.helm.release.v1.") {
		return false // Helm's record of a release, written with every install and upgrade
	}
	switch n.Kind {
	case "Deployment", "StatefulSet", "DaemonSet", "Job", "ConfigMap", "Secret", "Service", "Ingress",
		"PersistentVolumeClaim", "ResourceQuota", "Namespace", "Node":
		return n.Name != "kube-root-ca.crt" // copied into every namespace by Kubernetes
	}
	// Any other kind kubexray keeps a manifest history of (ADR-0018).
	return n.Manifest != nil
}

func removedWord(n graph.Node) string {
	if n.Kind == "Node" {
		return "left the cluster"
	}
	return "deleted"
}

func entry(n graph.Node, at time.Time, what, summary string, by *graph.Change) Entry {
	e := Entry{At: at, Object: n.ID, Kind: n.Kind, Name: n.Name, Namespace: n.Namespace, What: what, Summary: summary, By: by, Origin: n.Origin}
	if by != nil && by.Tool == "kubectl" && n.Origin != nil && what != "created" && what != "deleted" {
		e.Drift = fmt.Sprintf("%s %s is managed by %s; the next %s puts it back, or this hand edit is the only copy of the fix", n.Kind, n.Name, originName(n.Origin), originAction(n.Origin))
	}
	return e
}

func originName(o *graph.Origin) string {
	tool := map[string]string{"helm": "Helm release", "argocd": "Argo CD application", "flux": "Flux"}[o.Tool]
	if tool == "" {
		tool = o.Tool
	}
	if o.Name == "" {
		return tool
	}
	return tool + " " + o.Name
}

func originAction(o *graph.Origin) string {
	switch o.Tool {
	case "helm":
		return "helm upgrade"
	case "argocd":
		return "sync"
	default:
		return "reconcile"
	}
}

// author is the write behind a change, if the object's latest write is it:
// Kubernetes records no write for some changes (scales, ADR-0009).
func author(n graph.Node, at time.Time) *graph.Change {
	if c := n.Change; c != nil && at.Sub(c.At) <= Window && c.At.Sub(at) <= Window {
		return c
	}
	return nil
}

// compare turns one node update into entries.
func compare(old, n graph.Node, existed bool, at time.Time) []Entry {
	if !tracked(n) {
		return nil
	}
	by := author(n, at)
	if !existed {
		if n.Kind == "Node" {
			return []Entry{entry(n, at, "created", "joined the cluster", nil)}
		}
		e := entry(n, at, "created", "created", by)
		if v := chartVersion(n); v != "" {
			e.Version = &Versions{To: v}
		}
		return []Entry{e}
	}
	var out []Entry
	add := func(what, summary string, who *graph.Change) { out = append(out, entry(n, at, what, summary, who)) }

	if n.Kind == "Node" {
		switch {
		case old.Status.Ready && !n.Status.Ready:
			add("notready", "not ready"+paren(n.Status.Reason), nil)
		case !old.Status.Ready && n.Status.Ready:
			add("ready", "ready again", nil)
		}
		cordoned := func(x graph.Node) bool { return x.Status.Reason == "SchedulingDisabled" }
		switch {
		case cordoned(n) && !cordoned(old):
			add("cordoned", "cordoned: no new pods land here", by)
		case !cordoned(n) && cordoned(old) && n.Status.Ready:
			add("uncordoned", "uncordoned", by)
		}
		return out
	}

	if appKinds[n.Kind] {
		out = append(out, specEntries(old, n, at, by)...)
		if r0, r1 := old.Status.Replicas, n.Status.Replicas; r0 != nil && r1 != nil && r0.Desired != r1.Desired {
			add("replicas", fmt.Sprintf("scaled %d → %d replicas", r0.Desired, r1.Desired), by)
		}
		// A change outside the pod template, such as the rollout strategy.
		if lines := manifestDiff(old, n); len(out) == 0 && len(lines) > 0 && by != nil && by.Tool != "controller" {
			add("changed", "changed"+paren(describe(lines)), by)
		}
		if len(out) > 0 {
			out[0].Diff = objectDiff(old, n)
		}
		return withVersion(old, n, out)
	}

	written := n.Change != nil && (old.Change == nil || n.Change.At.After(old.Change.At))
	// Some objects carry no record of their writes (ServiceAccounts, often):
	// a changed manifest still says something changed, by someone unknown.
	unrecorded := n.Change == nil && old.Change == nil && old.Manifest != nil && len(manifestDiff(old, n)) > 0
	switch {
	case unrecorded:
		add("changed", "changed", nil)
	case !written:
	case n.Kind == "ConfigMap" && (old.Data != nil || n.Data != nil):
		lines := dataDiff(old.Data, n.Data)
		add("edited", "edited"+paren(describe(lines)), n.Change)
		out[len(out)-1].Diff = lines
	case n.Kind == "ConfigMap" || n.Kind == "Secret":
		add("edited", "edited (contents not read)", n.Change)
	case n.Change.Tool == "controller":
	case n.Kind == "Service" && !maps.Equal(old.Selector, n.Selector):
		add("changed", "selector changed", n.Change)
	default:
		add("changed", "changed", n.Change)
	}
	// Kinds with a manifest history say what changed (ADR-0018).
	if len(out) > 0 && out[len(out)-1].Diff == nil && (old.Manifest != nil || n.Manifest != nil) {
		e := &out[len(out)-1]
		e.Diff = dataDiff(old.Manifest, n.Manifest)
		if e.Summary == "changed" {
			e.Summary += paren(describe(e.Diff))
		}
	}
	return withVersion(old, n, out)
}

// withVersion puts a release's version change on the first of an object's
// entries, so a set made by an upgrade can say which.
func withVersion(old, n graph.Node, out []Entry) []Entry {
	if len(out) == 0 || slices.ContainsFunc(out, func(e Entry) bool { return e.Version != nil }) {
		return out
	}
	if v := versionChange(old, n); v != nil {
		out[0].Version = v
	}
	return out
}

func paren(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

// specEntries compares a workload's pod template.
func specEntries(old, n graph.Node, at time.Time, by *graph.Change) []Entry {
	if old.Spec == nil || n.Spec == nil {
		return nil
	}
	var out []Entry
	add := func(what, summary string) { out = append(out, entry(n, at, what, summary, by)) }
	before := map[string]graph.Container{}
	for _, c := range old.Spec.Containers {
		before[key(c)] = c
	}
	detailed := false
	// Name the container only when there's more than one to tell apart.
	main := 0
	for _, c := range n.Spec.Containers {
		if !c.Init && !c.Ephemeral {
			main++
		}
	}
	for _, c := range n.Spec.Containers {
		if c.Ephemeral {
			continue // kubectl debug containers come and go
		}
		p, ok := before[key(c)]
		label := ""
		switch {
		case c.Init:
			label = "init container " + c.Name + " "
		case main > 1:
			label = c.Name + " "
		}
		if !ok {
			add("template", fmt.Sprintf("%scontainer added (%s)", label, c.Image))
			detailed = true
			continue
		}
		if p.Image != c.Image {
			add("image", fmt.Sprintf("%simage %s → %s", label, p.Image, c.Image))
			detailed = true
		}
		if p.Requests != c.Requests || p.Limits != c.Limits {
			add("resources", label+resourceChanges(p, c))
			detailed = true
		}
		for _, k := range probeKinds {
			was, now := k.get(p.Probes), k.get(c.Probes)
			if now && !was {
				add("probes", fmt.Sprintf("%s%s probe added", label, k.name))
				detailed = true
			} else if was && !now {
				add("probes", fmt.Sprintf("%s%s probe removed", label, k.name))
				detailed = true
			}
		}
	}
	if old.Spec.Revision != "" && n.Spec.Revision != "" && old.Spec.Revision != n.Spec.Revision && !detailed {
		var d differ
		d.template(*old.Spec, *n.Spec)
		what := cmp.Or(describe(append(d.lines, manifestDiff(old, n)...)), "pod template changed")
		add("template", fmt.Sprintf("%s (revision %s → %s)", what, old.Spec.Revision, n.Spec.Revision))
	}
	if v := versionChange(old, n); v != nil && len(out) > 0 {
		out[0].Version = v
	}
	return out
}

func key(c graph.Container) string {
	if c.Init {
		return "init:" + c.Name
	}
	return c.Name
}

var probeKinds = []struct {
	name string
	get  func(graph.Probes) bool
}{
	{"liveness", func(p graph.Probes) bool { return p.Liveness }},
	{"readiness", func(p graph.Probes) bool { return p.Readiness }},
	{"startup", func(p graph.Probes) bool { return p.Startup }},
}

// resourceChanges lists only what changed: "memory limit 64Mi → 16Mi".
func resourceChanges(p, c graph.Container) string {
	var parts []string
	for _, f := range []struct{ label, was, now string }{
		{"cpu request", p.Requests.CPU, c.Requests.CPU},
		{"memory request", p.Requests.Memory, c.Requests.Memory},
		{"cpu limit", p.Limits.CPU, c.Limits.CPU},
		{"memory limit", p.Limits.Memory, c.Limits.Memory},
	} {
		if f.was != f.now {
			parts = append(parts, fmt.Sprintf("%s %s → %s", f.label, cmp.Or(f.was, "none"), cmp.Or(f.now, "none")))
		}
	}
	return strings.Join(parts, ", ")
}

// versionChange reads a chart or app version change from the standard
// labels: helm.sh/chart ("shop-2.0.0") or app.kubernetes.io/version.
func versionChange(old, n graph.Node) *Versions {
	from, to := chartVersion(old), chartVersion(n)
	if to == "" || from == to {
		return nil
	}
	return &Versions{From: from, To: to}
}

// chartVersion is an object's release version, from its chart label
// ("storefront-1.2.0") or app.kubernetes.io/version.
func chartVersion(x graph.Node) string {
	v := x.Labels["helm.sh/chart"]
	if i := strings.LastIndex(v, "-"); i >= 0 && i+1 < len(v) && v[i+1] >= '0' && v[i+1] <= '9' {
		return v[i+1:]
	}
	return x.Labels["app.kubernetes.io/version"]
}

// appsTouched lists the apps an entry affects: an app itself, or the apps
// whose pods use a ConfigMap, Secret or PVC.
func appsTouched(st *graph.State, e Entry) []string {
	if appKinds[e.Kind] {
		return []string{e.Object}
	}
	if e.Kind != "ConfigMap" && e.Kind != "Secret" && e.Kind != "PersistentVolumeClaim" {
		return nil
	}
	owner := map[string]string{} // child → owner
	for _, ed := range st.Edges {
		if ed.Type == graph.EdgeOwns {
			owner[ed.To] = ed.From
		}
	}
	seen := map[string]bool{}
	for _, ed := range st.Edges {
		if ed.Type != graph.EdgeMounts || ed.To != e.Object {
			continue
		}
		app := ed.From
		for range 3 {
			up, ok := owner[app]
			if !ok {
				break
			}
			app = up
		}
		if appKinds[kindOf(app)] {
			seen[app] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func kindOf(id string) string {
	kind, _, _ := strings.Cut(id, "/")
	return kind
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return out
}

// title names a set the way the feed shows it.
func title(s *Set) string {
	var version *Versions
	for _, e := range s.Entries {
		if e.Version != nil {
			version = e.Version
			break
		}
	}
	versions := ""
	switch {
	case version != nil && version.From != "":
		versions = " " + version.From + " → " + version.To
	case version != nil:
		versions = " " + version.To
	}
	installed := !slices.ContainsFunc(s.Entries, func(e Entry) bool { return e.What != "created" })
	if o := s.Origin; o != nil && (len(s.Entries) > 1 || version != nil) {
		switch o.Tool {
		case "helm":
			if installed {
				return "Helm install " + o.Name + versions
			}
			return "Helm upgrade " + o.Name + versions
		case "argocd":
			return "Argo CD sync of " + o.Name + versions
		case "flux":
			return "Flux reconcile of " + o.Name + versions
		}
	}
	if len(s.Entries) == 1 {
		return entryTitle(s.Entries[0])
	}
	objects := map[string]bool{}
	for _, e := range s.Entries {
		objects[e.Object] = true
	}
	if len(objects) == 1 {
		e := s.Entries[0]
		return fmt.Sprintf("%s %s: %d changes", e.Kind, e.Name, len(s.Entries))
	}
	return fmt.Sprintf("%d objects changed in %s", len(objects), cmp.Or(s.Namespace, "the cluster"))
}

func entryTitle(e Entry) string {
	switch e.What {
	case "replicas":
		return e.Name + " " + e.Summary
	case "created", "deleted", "edited":
		return fmt.Sprintf("%s %s %s", e.Kind, e.Name, strings.TrimSuffix(e.Summary, " (contents not read)"))
	case "ready", "notready", "cordoned", "uncordoned":
		return fmt.Sprintf("Node %s %s", e.Name, strings.SplitN(e.Summary, ":", 2)[0])
	default:
		return fmt.Sprintf("%s %s: %s", e.Kind, e.Name, e.Summary)
	}
}
