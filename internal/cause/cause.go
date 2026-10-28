// Package cause finds broken apps and the change most likely to have broken
// them (ADR-0006). It is pure logic over the graph model: no I/O, no logs, no
// Secret or ConfigMap contents. The UI, `kubexray why` and post-mortems all
// render its results.
package cause

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// Confidence is how strongly a change is tied to a symptom (ADR-0006,
// section 3).
type Confidence string

// Confidence levels, from strongest to weakest.
const (
	VeryLikely Confidence = "very likely"
	Likely     Confidence = "likely"
	Possible   Confidence = "possible"
)

// rank orders confidence levels: higher is stronger.
func (c Confidence) rank() int {
	switch c {
	case VeryLikely:
		return 3
	case Likely:
		return 2
	case Possible:
		return 1
	default:
		return 0
	}
}

// Kind is the type of change a cause describes.
type Kind string

// Kinds of causes.
const (
	KindImage     Kind = "image"     // a container image changed
	KindResources Kind = "resources" // CPU or memory requests or limits changed
	KindTemplate  Kind = "template"  // the pod template changed in a way the model doesn't record
	KindProbe     Kind = "probe"     // a health probe was added or removed
	KindRestart   Kind = "restart"   // pods were restarted without changing the template
	KindChange    Kind = "change"    // the app changed, details unknown
	KindConfig    Kind = "config"    // a ConfigMap or Secret the app uses was edited
	KindMissing   Kind = "missing"   // a ConfigMap, Secret or PVC the app uses doesn't exist
	KindNode      Kind = "node"      // a node running the app's pods is unhealthy
	KindScale     Kind = "scale"     // the app was scaled to zero
	KindQuota     Kind = "quota"     // a ResourceQuota that refuses the app's pods changed
	// What routes requests to the app changed: a Service in front of it, an
	// Ingress to that Service, or a NetworkPolicy selecting its pods.
	KindRouting    Kind = "routing"
	KindAutoscaler Kind = "autoscaler" // the HPA that scales the app changed
)

// Cause is one candidate explanation for an incident.
type Cause struct {
	Kind       Kind          `json:"kind"`
	Object     string        `json:"object"` // ID of what changed: the app, a ConfigMap, a Node…
	At         time.Time     `json:"at,omitzero"`
	By         *graph.Change `json:"by,omitempty"`
	Origin     *graph.Origin `json:"origin,omitempty"`
	From       string        `json:"from,omitempty"`
	To         string        `json:"to,omitempty"`
	Summary    string        `json:"summary"`
	Confidence Confidence    `json:"confidence"`
	Evidence   []string      `json:"evidence,omitempty"`
}

// Symptom describes what is wrong with an app.
type Symptom struct {
	Summary  string   `json:"summary"`          // e.g. "3 pods CrashLoopBackOff"
	Reason   string   `json:"reason,omitempty"` // the most common pod reason
	BadPods  []string `json:"badPods,omitempty"`
	Ready    int32    `json:"ready"`
	Desired  int32    `json:"desired"`
	Restarts int32    `json:"restarts,omitempty"`
}

// Step is a suggested next command. kubexray never runs it.
type Step struct {
	Command string `json:"command"`
	Note    string `json:"note,omitempty"`
}

// Incident is an app's health and, when it's broken, the likely causes.
type Incident struct {
	App       string     `json:"app"`
	Kind      string     `json:"kind"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Healthy   bool       `json:"healthy"`
	Since     time.Time  `json:"since,omitzero"`
	Symptom   Symptom    `json:"symptom"`
	Impact    []string   `json:"impact,omitempty"`    // e.g. "Ingress web → Service api → 0 of 3 pods ready"
	Causes    []Cause    `json:"causes,omitempty"`    // best first; empty when nothing qualifies
	NoCause   string     `json:"noCause,omitempty"`   // said when Causes is empty
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"` // what Kubernetes says is wrong (ADR-0009)
	Next      []Step     `json:"next,omitempty"`
	// LastChange is the app's most recent change, for context when it's healthy.
	LastChange *graph.Change `json:"lastChange,omitempty"`
	// Signal is what spotted it when its pods look healthy (ADR-0016).
	Signal *Signal `json:"signal,omitempty"`
}

// Down reports whether the app has no ready pods at all: none of those it
// wants, or none at all while traffic is routed to it (scaled to zero).
func (i Incident) Down() bool {
	return !i.Healthy && i.Symptom.Ready == 0 && (i.Symptom.Desired > 0 || len(i.Symptom.BadPods) == 0)
}

// State says how the app is doing in a few words: "is healthy", "is down",
// "is degraded" (fewer ready pods than wanted), or "has failing pods" (all
// wanted pods are ready, but others fail, e.g. a stuck rollout).
func (i Incident) State() string {
	switch {
	case i.Healthy:
		return "is healthy"
	case i.Signal != nil && len(i.Symptom.BadPods) == 0:
		return i.Signal.State
	case i.Symptom.Desired == 0 && i.Symptom.Ready == 0 && len(i.Symptom.BadPods) == 0:
		return "is scaled to zero"
	case i.Down():
		return "is down"
	case i.Symptom.Ready < i.Symptom.Desired:
		return "is degraded"
	case len(i.Symptom.BadPods) == 0:
		return "can't create new pods"
	default:
		return "has failing pods"
	}
}

// Options tune the analysis.
type Options struct {
	Now      time.Time
	Grace    time.Duration // how long a problem must last to be an incident
	Lookback time.Duration // how far before an incident to look for causes
	Skew     time.Duration // changes this long after the start still count (clock skew)
}

// DefaultOptions are the values from ADR-0006.
func DefaultOptions(now time.Time) Options {
	return Options{Now: now, Grace: 30 * time.Second, Lookback: 30 * time.Minute, Skew: 10 * time.Second}
}

// Analyze returns every app that has been broken for longer than the grace
// period, oldest incident first.
func Analyze(st *graph.State, opts Options) []Incident {
	x := newIndex(st)
	var out []Incident
	for _, app := range x.apps() {
		inc := x.explain(app, opts)
		if inc.Healthy || (!inc.Since.IsZero() && opts.Now.Sub(inc.Since) < opts.Grace) {
			continue
		}
		out = append(out, inc)
	}
	slices.SortFunc(out, func(a, b Incident) int {
		return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.App, b.App))
	})
	return out
}

// ErrNotAnApp is returned by Explain for objects that aren't apps or pods.
var ErrNotAnApp = fmt.Errorf("not an app: kubexray explains deployments, statefulsets, daemonsets, jobs and pods")

// Explain analyzes one app, healthy or not, regardless of the grace period.
// A pod is explained through the app it belongs to.
func Explain(st *graph.State, id string, opts Options) (Incident, error) {
	return NewExplainer(st, opts)(id)
}

// NewExplainer returns Explain for many apps of one state, indexing the
// state once.
func NewExplainer(st *graph.State, opts Options) func(id string) (Incident, error) {
	x := newIndex(st)
	return func(id string) (Incident, error) { return x.explainID(id, opts) }
}

func (x *index) explainID(id string, opts Options) (Incident, error) {
	st := x.st
	n, ok := st.Nodes[id]
	if !ok {
		return Incident{}, fmt.Errorf("%s not found", id)
	}
	if n.Kind != "Pod" && !appKinds[n.Kind] {
		return Incident{}, ErrNotAnApp
	}
	// Explain pods and ReplicaSets through the app that runs them.
	return x.explain(st.Nodes[x.appOf(id)], opts), nil
}

var appKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "Job": true, "ReplicaSet": true}

// Pod reasons that mean the pod is failing, not just starting.
var failingReasons = map[string]bool{
	"CrashLoopBackOff": true, "Error": true, "OOMKilled": true,
	"ImagePullBackOff": true, "ErrImagePull": true, "InvalidImageName": true,
	"CreateContainerConfigError": true, "CreateContainerError": true,
	"Evicted": true, "Unschedulable": true, ReasonNodeNotReady: true,
}

// ReasonNodeNotReady marks pods that aren't ready because their node isn't.
const ReasonNodeNotReady = "NodeNotReady"

var pullReasons = map[string]bool{"ImagePullBackOff": true, "ErrImagePull": true, "InvalidImageName": true}

func terminating(p graph.Node) bool { return p.Status.Reason == "Terminating" }

// bad is a pod that counts against its app: not ready. A ready pod is fine
// even when its reason still names an old crash (the mapper keeps the last
// termination reason of recovered pods, e.g. OOMKilled).
func bad(p graph.Node) bool { return !p.Status.Ready }

// active reports whether a pod is part of the app's current state. Finished
// pods aren't: succeeded pods, pods shutting down, and failed or evicted pods
// that a controller has already replaced. For Jobs and standalone pods a
// failed pod is the outcome itself, so it stays.
func active(app graph.Node, p graph.Node) bool {
	switch {
	case terminating(p), p.Status.Phase == "Succeeded":
		return false
	case p.Status.Phase == "Failed":
		return app.Kind == "Job" || app.Kind == "Pod"
	default:
		return true
	}
}

// index answers questions about the graph's structure.
type index struct {
	st       *graph.State
	in, out  map[string][]graph.Edge
	podsOf   map[string][]graph.Node // app ID → its pods, sorted by name
	appByPod map[string]string
	services map[string][]graph.Node // namespace → Services, see servicesIn
	alertsOf map[string][]graph.Node // app ID → its firing alerts
}

func newIndex(st *graph.State) *index {
	x := &index{st: st, in: map[string][]graph.Edge{}, out: map[string][]graph.Edge{}, podsOf: map[string][]graph.Node{}, appByPod: map[string]string{}}
	for _, e := range st.Edges {
		x.out[e.From] = append(x.out[e.From], e)
		x.in[e.To] = append(x.in[e.To], e)
	}
	x.alertsOf = map[string][]graph.Node{}
	for _, n := range st.Nodes {
		switch n.Kind {
		case "Pod":
			app := x.appOf(n.ID)
			x.appByPod[n.ID] = app
			x.podsOf[app] = append(x.podsOf[app], n)
		case graph.KindAlert:
			x.alertsOf[n.Parent] = append(x.alertsOf[n.Parent], n)
		}
	}
	for _, alerts := range x.alertsOf {
		slices.SortFunc(alerts, func(a, b graph.Node) int { return cmp.Compare(a.Name, b.Name) })
	}
	for _, pods := range x.podsOf {
		slices.SortFunc(pods, func(a, b graph.Node) int { return cmp.Compare(a.Name, b.Name) })
	}
	return x
}

// appOf follows owner references up from a pod to the app that runs it
// (Pod ← ReplicaSet ← Deployment). A pod without a known owner is its own app.
func (x *index) appOf(id string) string {
	cur := id
	for range 4 {
		next := ""
		for _, e := range x.in[cur] {
			if o, ok := x.st.Nodes[e.From]; ok && e.Type == graph.EdgeOwns && appKinds[o.Kind] {
				next = o.ID
				break
			}
		}
		if next == "" {
			break
		}
		cur = next
	}
	return cur
}

// apps lists every app: workloads at the top of their ownership chain, and
// standalone pods.
func (x *index) apps() []graph.Node {
	seen := map[string]bool{}
	var apps []graph.Node
	for _, n := range x.st.Nodes {
		id := ""
		switch {
		case n.Kind == "Pod":
			id = x.appByPod[n.ID]
		case appKinds[n.Kind]:
			id = x.appOf(n.ID) // a ReplicaSet owned by a Deployment isn't an app itself
		}
		if id != "" && !seen[id] {
			seen[id] = true
			apps = append(apps, x.st.Nodes[id])
		}
	}
	slices.SortFunc(apps, func(a, b graph.Node) int { return cmp.Compare(a.ID, b.ID) })
	return apps
}

// deps returns the ConfigMaps, Secrets and PVCs the app's pods use, by ID,
// including ones that don't exist.
func (x *index) deps(pods []graph.Node) []string {
	var ids []string
	for _, p := range pods {
		for _, e := range x.out[p.ID] {
			if e.Type == graph.EdgeMounts && !strings.HasSuffix(e.To, "/kube-root-ca.crt") && !slices.Contains(ids, e.To) {
				ids = append(ids, e.To)
			}
		}
	}
	slices.Sort(ids)
	return ids
}

// explain builds the incident for one app.
func (x *index) explain(app graph.Node, opts Options) Incident {
	pods := slices.DeleteFunc(slices.Clone(x.podsOf[app.ID]), func(p graph.Node) bool { return !active(app, p) })
	// A pod on a NotReady node reports nothing itself (its kubelet is gone);
	// say why it isn't ready. The map applies the same rule (web/src/lib/view.ts)
	// to pods that aren't part of an incident yet.
	for i, p := range pods {
		if n, ok := x.st.Nodes[p.Parent]; ok && n.Kind == "Node" && !n.Status.Ready && !p.Status.Ready && p.Status.Reason == "" {
			pods[i].Status.Reason = ReasonNodeNotReady
		}
	}
	inc := Incident{App: app.ID, Kind: app.Kind, Namespace: app.Namespace, Name: app.Name, LastChange: app.Change}
	if x.scaledToZero(app, &inc) {
		return inc
	}
	inc.Symptom = symptom(app, pods)
	var traffic *graph.Traffic
	if t, ok := x.st.Nodes[graph.TrafficID(app)]; ok {
		traffic = t.Traffic
	}
	// Kubernetes refusing to create pods (a quota, a webhook) is a problem even
	// while the old pods still serve: the rollout or scale-up is stuck. The
	// reason survives anonymization; the message doesn't.
	refused := app.Kind == "Deployment" && (app.Status.Reason == "FailedCreate" || app.Status.Message != "")
	if (len(inc.Symptom.BadPods) == 0 && inc.Symptom.Ready >= inc.Symptom.Desired && !refused) || (app.Kind == "Job" && app.Status.Phase == "Complete") {
		inc.Signal = cmp.Or(trafficSignal(traffic, opts.Now), alertSignal(x.alertsOf[app.ID]), signal(pods, opts.Now))
		if inc.Signal == nil {
			inc.Healthy = true
			return inc
		}
		// Running and ready, but restarting or near its memory limit.
		inc.Symptom.Summary = inc.Signal.Summary
		inc.Since = inc.Signal.Since
		a := &analysis{x: x, app: app, pods: pods, inc: &inc, opts: opts}
		inc.Causes = a.causes()
		if len(inc.Causes) == 0 {
			inc.NoCause = fmt.Sprintf("nothing about %s or what it uses changed in the %s before it started", app.Name, duration(opts.Lookback))
		}
		inc.Next = nextSteps(inc, app)
		return inc
	}
	if refused && len(inc.Symptom.BadPods) == 0 {
		inc.Symptom.Summary = "new pods can't be created"
	}
	inc.Signal = cmp.Or(trafficSignal(traffic, opts.Now), alertSignal(x.alertsOf[app.ID])) // failing pods, and what else says so
	inc.Since = since(pods)
	if inc.Since.IsZero() && refused {
		inc.Since = app.Status.Since // when the ReplicaFailure condition appeared
	}
	inc.Impact = x.impact(pods, inc.Symptom)
	inc.Diagnosis = x.diagnose(app, pods, inc.Symptom, opts)

	a := &analysis{x: x, app: app, pods: pods, inc: &inc, opts: opts}
	inc.Causes = a.causes()
	if len(inc.Causes) == 0 {
		inc.NoCause = fmt.Sprintf("nothing about %s or what it uses changed in the %s before it broke", app.Name, duration(opts.Lookback))
	}
	inc.Next = nextSteps(inc, app)
	return inc
}

func symptom(app graph.Node, pods []graph.Node) Symptom {
	s := Symptom{Desired: int32(len(pods))} //nolint:gosec // pod counts fit in int32
	if r := app.Status.Replicas; r != nil {
		s.Desired, s.Ready = r.Desired, r.Ready
	} else {
		for _, p := range pods {
			if p.Status.Ready {
				s.Ready++
			}
		}
	}
	reasons := map[string]int{}
	for _, p := range pods {
		s.Restarts += p.Status.Restarts
		if bad(p) {
			s.BadPods = append(s.BadPods, p.ID)
			reasons[p.Status.Reason]++
		}
	}
	// The most common reason, preferring real failures over "starting", then
	// by name so the choice is stable.
	best := ""
	for _, r := range slices.Sorted(maps.Keys(reasons)) {
		if reasons[best] == 0 || reasons[r] > reasons[best] ||
			(reasons[r] == reasons[best] && failingReasons[r] && !failingReasons[best]) {
			best = r
		}
	}
	s.Reason = best
	switch {
	case len(s.BadPods) > 0 && best != "":
		s.Summary = fmt.Sprintf("%s %s", plural(reasons[best], "pod"), best)
	case len(s.BadPods) > 0:
		s.Summary = plural(len(s.BadPods), "pod") + " not ready"
	default:
		s.Summary = fmt.Sprintf("%d of %d pods ready", s.Ready, s.Desired)
	}
	return s
}

// crashLoopSpan bounds how long a crash-looping pod is assumed to have been
// failing since it was created.
const crashLoopSpan = 6 * time.Hour

// since is when the app's trouble started: the earliest moment one of its bad
// pods went bad.
func since(pods []graph.Node) time.Time {
	var t time.Time
	for _, p := range pods {
		if bad(p) {
			if at := wentBad(p); !at.IsZero() && (t.IsZero() || at.Before(t)) {
				t = at
			}
		}
	}
	return t
}

// wentBad estimates when a pod went bad. Its Ready condition's transition time
// is right for a pod that broke once, but a crash-looping pod flips Ready on
// every restart, so that time keeps moving. A crash-looping pod created
// shortly before is assumed to have been failing since it started, which is
// the common case: a bad rollout or config picked up by new pods.
func wentBad(p graph.Node) time.Time {
	s := p.Status.Since
	if s.IsZero() || (p.Status.Restarts > 0 && !p.Created.IsZero() && s.Sub(p.Created) <= crashLoopSpan) {
		return p.Created
	}
	return s
}

// impact describes the path users take to reach the app: Ingress → Service →
// ready pods.
func (x *index) impact(pods []graph.Node, s Symptom) []string {
	services := map[string]bool{}
	for _, p := range pods {
		for _, e := range x.in[p.ID] {
			if e.Type == graph.EdgeSelects {
				services[e.From] = true
			}
		}
	}
	lines := make([]string, 0, len(services))
	for _, svc := range slices.Sorted(maps.Keys(services)) {
		path := ""
		if hops := x.ingressHops(svc); len(hops) > 0 {
			path = strings.Join(hops, ", ") + " → "
		}
		lines = append(lines, fmt.Sprintf("%sService %s → %d of %d pods ready", path, x.st.Nodes[svc].Name, s.Ready, s.Desired))
	}
	return lines
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
