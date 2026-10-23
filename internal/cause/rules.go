package cause

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubexray/kubexray/internal/graph"
)

// analysis gathers and ranks the candidate causes for one incident.
type analysis struct {
	x    *index
	app  graph.Node
	pods []graph.Node
	inc  *Incident
	opts Options
}

// inWindow reports whether a change at t could have caused the incident: no
// more than Lookback before it started, and at most Skew after.
func (a *analysis) inWindow(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	start := a.inc.Since
	if start.IsZero() {
		start = a.opts.Now
	}
	return !t.Before(start.Add(-a.opts.Lookback)) && !t.After(start.Add(a.opts.Skew))
}

func (a *analysis) causes() []Cause {
	cs := slices.Concat(a.rollout(), a.appChange(), a.dependencies(), a.nodes())

	start := a.inc.Since
	slices.SortStableFunc(cs, func(p, q Cause) int {
		return cmp.Or(cmp.Compare(q.Confidence.rank(), p.Confidence.rank()), cmp.Compare(start.Sub(p.At).Abs(), start.Sub(q.At).Abs()))
	})
	if len(cs) == 1 {
		cs[0].Evidence = append(cs[0].Evidence, fmt.Sprintf("no other changes to %s or what it uses in the %s before", a.app.Name, duration(a.opts.Lookback)))
	}
	return cs
}

// beforeAfter is the strongest evidence available without logs: bad pods
// created after a change, while pods created before it are fine.
type beforeAfter struct {
	badAfter, badBefore, goodBefore int
}

func (a *analysis) split(at time.Time) beforeAfter {
	var b beforeAfter
	for _, p := range a.pods {
		switch {
		case bad(p) && p.Created.After(at):
			b.badAfter++
		case bad(p):
			b.badBefore++
		case !p.Created.After(at):
			b.goodBefore++
		}
	}
	return b
}

// strong: only pods started after the change are bad, and older ones are fine.
func (b beforeAfter) strong() bool { return b.badAfter > 0 && b.badBefore == 0 && b.goodBefore > 0 }

// onlyNew: every bad pod started after the change, and no older pod is left.
func (b beforeAfter) onlyNew() bool { return b.badAfter > 0 && b.badBefore == 0 }

func (b beforeAfter) evidence(what string) []string {
	ev := []string{fmt.Sprintf("%s started after %s %s failing", plural(b.badAfter, "pod"), what, isAre(b.badAfter))}
	if b.goodBefore > 0 {
		ev = append(ev, fmt.Sprintf("%s started before it %s healthy", plural(b.goodBefore, "pod"), isAre(b.goodBefore)))
	}
	return ev
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// reasons counts the bad pods' reasons.
func (a *analysis) reasons() map[string]int {
	r := map[string]int{}
	for _, p := range a.pods {
		if bad(p) {
			r[p.Status.Reason]++
		}
	}
	return r
}

func (a *analysis) badWith(match func(string) bool) int {
	n := 0
	for r, c := range a.reasons() {
		if match(r) {
			n += c
		}
	}
	return n
}

// rollout finds a Deployment's latest template change by comparing its
// current ReplicaSet with the previous one, which Kubernetes keeps.
func (a *analysis) rollout() []Cause {
	if a.app.Kind != "Deployment" || a.app.Spec == nil {
		return nil
	}
	cur, prev := a.x.revisions(a.app)
	if cur == nil || prev == nil {
		return nil
	}
	at := cur.Created
	// A rollback makes an older ReplicaSet current again, so its creation time
	// says nothing about when the rollout happened; the Deployment's own last
	// write does. Any other later write (a scale, a label) isn't a rollout.
	if c := a.app.Change; cur.Created.Before(prev.Created) && c != nil && c.At.After(at) {
		at = c.At
	}
	if !a.inWindow(at) {
		return nil
	}
	by := a.app.Change
	if by != nil && by.At.Sub(at).Abs() > attributionSlack {
		by = nil // the Deployment's last write wasn't this rollout
	}
	revs := fmt.Sprintf("revision %s → %s", prev.Spec.Revision, cur.Spec.Revision)
	base := Cause{Object: a.app.ID, At: at, By: by, Origin: a.app.Origin}
	b := a.split(at)

	var out []Cause
	if from, to, ok := imageDiff(prev.Spec, cur.Spec); ok {
		c := base
		c.Kind, c.From, c.To = KindImage, from, to
		c.Summary = fmt.Sprintf("image changed %s → %s (%s)", from, to, revs)
		switch n := a.badWith(func(r string) bool { return pullReasons[r] }); {
		case n > 0:
			c.Confidence = VeryLikely
			c.Evidence = []string{fmt.Sprintf("%s can't pull %s", plural(n, "pod"), to)}
		case b.strong():
			c.Confidence, c.Evidence = Likely, b.evidence("the rollout")
		default:
			c.Confidence = Possible
		}
		out = append(out, c)
	}
	if from, to, lowered, ok := resourceDiff(prev.Spec, cur.Spec); ok {
		c := base
		c.Kind, c.From, c.To = KindResources, from, to
		c.Summary = fmt.Sprintf("resources changed %s → %s (%s)", from, to, revs)
		oom := a.badWith(func(r string) bool { return r == "OOMKilled" }) + a.restartedFromOOM()
		switch {
		case oom > 0 && lowered:
			c.Confidence = VeryLikely
			c.Evidence = []string{fmt.Sprintf("memory limit lowered and %s ran out of memory (OOMKilled)", plural(oom, "pod"))}
		case b.strong():
			c.Confidence, c.Evidence = Likely, b.evidence("the rollout")
		default:
			c.Confidence = Possible
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		c := base
		if by != nil && by.Via == "rollout" {
			c.Kind, c.Confidence = KindRestart, Possible
			c.Summary = fmt.Sprintf("pods restarted (%s)", revs)
			c.Evidence = []string{"a restart doesn't change the app itself, but new pods pick up config edits made since the last start"}
		} else {
			c.Kind = KindTemplate
			c.Summary = fmt.Sprintf("pod template changed (%s) in a way kubexray doesn't record: environment, command, probes or volumes", revs)
			c.Confidence = Possible
			if b.strong() {
				c.Confidence, c.Evidence = Likely, b.evidence("the rollout")
			}
		}
		out = append(out, c)
	}
	return out
}

// restartedFromOOM counts pods that recovered after being OOMKilled; they're
// ready again, and their reason still reads OOMKilled.
func (a *analysis) restartedFromOOM() int {
	n := 0
	for _, p := range a.pods {
		if !bad(p) && p.Status.Reason == "OOMKilled" {
			n++
		}
	}
	return n
}

// revisions returns a Deployment's current and previous ReplicaSets.
func (x *index) revisions(d graph.Node) (cur, prev *graph.Node) {
	var rss []graph.Node
	for _, e := range x.out[d.ID] {
		if rs, ok := x.st.Nodes[e.To]; ok && e.Type == graph.EdgeOwns && rs.Kind == "ReplicaSet" && rs.Spec != nil && rs.Spec.Revision != "" {
			rss = append(rss, rs)
		}
	}
	rev := func(n graph.Node) int { r, _ := strconv.Atoi(n.Spec.Revision); return r }
	slices.SortFunc(rss, func(a, b graph.Node) int { return cmp.Compare(rev(b), rev(a)) })
	for i := range rss {
		if rss[i].Spec.Revision == d.Spec.Revision || (cur == nil && d.Spec.Revision == "") {
			cur = &rss[i]
			if i+1 < len(rss) {
				prev = &rss[i+1]
			}
			return cur, prev
		}
	}
	return nil, nil
}

// imageDiff reports the first container whose image changed.
func imageDiff(prev, cur *graph.Spec) (from, to string, ok bool) {
	for _, c := range cur.Containers {
		for _, p := range prev.Containers {
			if p.Name == c.Name && p.Init == c.Init && p.Image != c.Image {
				return p.Image, c.Image, true
			}
		}
	}
	return "", "", false
}

// resourceDiff reports the first container whose requests or limits changed,
// and whether its memory limit went down.
func resourceDiff(prev, cur *graph.Spec) (from, to string, memoryLowered, ok bool) {
	for _, c := range cur.Containers {
		for _, p := range prev.Containers {
			if p.Name != c.Name || p.Init != c.Init || (p.Requests == c.Requests && p.Limits == c.Limits) {
				continue
			}
			return describeResources(p), describeResources(c), lower(c.Limits.Memory, p.Limits.Memory), true
		}
	}
	return "", "", false, false
}

func describeResources(c graph.Container) string {
	parts := []string{}
	add := func(label, v string) {
		if v != "" {
			parts = append(parts, label+" "+v)
		}
	}
	add("cpu request", c.Requests.CPU)
	add("memory request", c.Requests.Memory)
	add("cpu limit", c.Limits.CPU)
	add("memory limit", c.Limits.Memory)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// lower reports whether quantity a is set and smaller than b (or b is unset).
func lower(a, b string) bool {
	qa, errA := resource.ParseQuantity(a)
	if a == "" || errA != nil {
		return false
	}
	if b == "" {
		return true // a limit was added where there was none
	}
	qb, err := resource.ParseQuantity(b)
	return err == nil && qa.Cmp(qb) < 0
}

// appChange covers apps without ReplicaSet history (StatefulSets, DaemonSets,
// Jobs): the app's latest write, if a person or tool made it in the window.
func (a *analysis) appChange() []Cause {
	c := a.app.Change
	if a.app.Kind == "Deployment" || c == nil || c.Tool == "controller" || !a.inWindow(c.At) {
		return nil
	}
	cause := Cause{
		Kind: KindChange, Object: a.app.ID, At: c.At, By: c, Origin: a.app.Origin, Confidence: Possible,
		Summary: fmt.Sprintf("%s %s was changed", a.app.Kind, a.app.Name),
	}
	if b := a.split(c.At); b.strong() {
		cause.Confidence, cause.Evidence = Likely, b.evidence("the change")
	}
	return []Cause{cause}
}

// dependencies covers ConfigMaps, Secrets and PVCs the app uses: edits in the
// window, and references to ones that don't exist.
func (a *analysis) dependencies() []Cause {
	var out []Cause
	configErrors := a.badWith(func(r string) bool { return r == "CreateContainerConfigError" })
	for _, id := range a.x.deps(a.pods) {
		kind, name := kindName(id)
		n, exists := a.x.st.Nodes[id]
		if !exists {
			c := Cause{
				Kind: KindMissing, Object: id, At: a.inc.Since, Confidence: Possible,
				Summary: fmt.Sprintf("%s %s doesn't exist, but the pods use it", kind, name),
			}
			// Pods that can't start without it: config errors, or stuck creating
			// (a missing volume). Running pods that aren't ready are a different
			// problem, and an optional reference may legitimately be missing.
			stuck := configErrors + a.badWith(func(r string) bool { return r == "ContainerCreating" })
			if stuck > 0 {
				c.Confidence = VeryLikely
				c.Evidence = []string{fmt.Sprintf("%s can't start without it", plural(stuck, "pod"))}
			}
			out = append(out, c)
			continue
		}
		ch := n.Change
		if ch == nil || !a.inWindow(ch.At) {
			continue
		}
		c := Cause{
			Kind: KindConfig, Object: id, At: ch.At, By: ch, Origin: n.Origin, Confidence: Possible,
			Summary: fmt.Sprintf("%s %s was edited", kind, name),
		}
		b := a.split(ch.At)
		switch {
		case configErrors > 0:
			c.Confidence = Likely
			c.Evidence = []string{fmt.Sprintf("%s can't load their configuration (CreateContainerConfigError); a key they read may have been removed", plural(configErrors, "pod"))}
		case b.onlyNew():
			// Pods read config when they start: only pods started since the
			// edit see it, so "only new pods fail" points straight at it.
			c.Confidence, c.Evidence = Likely, b.evidence("the edit")
		}
		out = append(out, c)
	}
	return out
}

// nodes covers unhealthy nodes running the app's bad pods.
func (a *analysis) nodes() []Cause {
	seen := map[string]int{}
	for _, p := range a.pods {
		if bad(p) && strings.HasPrefix(p.Parent, "Node/") {
			seen[p.Parent]++
		}
	}
	var out []Cause
	for _, id := range slices.Sorted(maps.Keys(seen)) {
		// Only NotReady counts: a cordoned node stops new pods from being
		// scheduled there, but doesn't affect pods already running.
		n, ok := a.x.st.Nodes[id]
		if !ok || n.Status.Ready {
			continue
		}
		state := "is not ready"
		if n.Status.Reason != "" {
			state += " (" + n.Status.Reason + ")"
		}
		at := n.Status.Since
		if at.IsZero() {
			at = a.inc.Since
		}
		out = append(out, Cause{
			Kind: KindNode, Object: id, At: at, Confidence: VeryLikely,
			Summary:  fmt.Sprintf("node %s %s", n.Name, state),
			Evidence: []string{fmt.Sprintf("%s on this node %s failing", plural(seen[id], "pod"), isAre(seen[id]))},
		})
	}
	return out
}

func kindName(id string) (kind, name string) {
	parts := strings.Split(id, "/")
	return parts[0], parts[len(parts)-1]
}

// nextSteps suggests read-only commands to confirm the cause, and the usual
// fix. kubexray never runs them.
func nextSteps(inc Incident, app graph.Node) []Step {
	var steps []Step
	ns := inc.Namespace
	var badPod string
	if len(inc.Symptom.BadPods) > 0 {
		_, badPod = kindName(inc.Symptom.BadPods[0])
	}
	switch r := inc.Symptom.Reason; {
	case badPod == "":
	case r == "CrashLoopBackOff" || r == "Error" || r == "OOMKilled" || strings.HasPrefix(r, "Init:"):
		steps = append(steps, Step{Command: fmt.Sprintf("kubectl -n %s logs %s --previous", ns, badPod), Note: "why the last attempt failed"})
	default:
		steps = append(steps, Step{Command: fmt.Sprintf("kubectl -n %s describe pod %s", ns, badPod), Note: "the pod's events"})
	}

	if len(inc.Causes) == 0 {
		return steps
	}
	top := inc.Causes[0]
	workload := strings.ToLower(app.Kind) + "/" + app.Name
	switch top.Kind {
	case KindImage, KindResources, KindTemplate:
		steps = append(steps, rollback(top, ns, app.Name))
	case KindConfig:
		if rollable[app.Kind] {
			_, name := kindName(top.Object)
			steps = append(steps, Step{Command: fmt.Sprintf("kubectl -n %s rollout restart %s", ns, workload), Note: "after fixing " + name})
		}
	case KindMissing:
		kind, name := kindName(top.Object)
		steps = append(steps, Step{Command: fmt.Sprintf("kubectl -n %s get %s %s", ns, strings.ToLower(kind), name), Note: "create it, or remove the reference"})
	case KindNode:
		_, name := kindName(top.Object)
		steps = append(steps, Step{Command: "kubectl describe node " + name, Note: "the node's conditions and events"})
	case KindRestart, KindChange:
	}
	return steps
}

// rollable kinds support kubectl rollout restart and undo.
var rollable = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true}

// attributionSlack is how close a write must be to a rollout to be credited
// with it. Shared with Changes so both report the same author.
const attributionSlack = 10 * time.Second

func rollback(c Cause, ns, name string) Step {
	if o := c.Origin; o != nil {
		switch o.Tool {
		case "helm":
			return Step{Command: "helm -n " + ns + " rollback " + o.Name, Note: "roll back the Helm release"}
		case "argocd", "flux":
			return Step{Command: "git revert <commit>", Note: "revert the change in git; " + map[string]string{"argocd": "Argo CD", "flux": "Flux"}[o.Tool] + " will sync it"}
		}
	}
	return Step{Command: fmt.Sprintf("kubectl -n %s rollout undo deployment/%s", ns, name), Note: "go back to the previous revision"}
}

func duration(d time.Duration) string {
	if d%time.Hour == 0 {
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/time.Minute), "minute")
}
