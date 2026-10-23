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

func (a *analysis) badWith(match func(string) bool) int {
	return a.badWhere(func(p graph.Node) bool { return match(p.Status.Reason) })
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
	// Going back to any template the Deployment had before reuses that
	// ReplicaSet, creation time and all. Its pods were created by this
	// rollout, though: a Deployment write that made them is the rollout.
	// Scaling up from zero creates pods too, but isn't a template change.
	if c := a.app.Change; c != nil && c.At.After(at) && c.Via != "scale" && c.Tool != "controller" {
		if first := a.firstPodOf(cur.ID); !first.IsZero() && first.Sub(c.At).Abs() <= attributionSlack {
			at = c.At
		}
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
	if from, to, lowered, raised, ok := resourceDiff(prev.Spec, cur.Spec); ok {
		c := base
		c.Kind, c.From, c.To = KindResources, from, to
		c.Summary = fmt.Sprintf("resources changed %s → %s (%s)", from, to, revs)
		oom := a.badWith(func(r string) bool { return r == "OOMKilled" }) + a.restartedFromOOM()
		noRoom := a.badWhere(func(p graph.Node) bool {
			return p.Status.Reason == "Unschedulable" && strings.Contains(p.Status.Message, "Insufficient")
		})
		switch {
		case oom > 0 && lowered:
			c.Confidence = VeryLikely
			c.Evidence = []string{fmt.Sprintf("memory limit lowered and %s ran out of memory (OOMKilled)", plural(oom, "pod"))}
		case noRoom > 0 && raised:
			c.Confidence = VeryLikely
			c.Evidence = []string{fmt.Sprintf("requests raised and %s can't be scheduled: no node has room for them", plural(noRoom, "pod"))}
		case b.strong():
			c.Confidence, c.Evidence = Likely, b.evidence("the rollout")
		default:
			c.Confidence = Possible
		}
		out = append(out, c)
	}
	if what, added, ok := probeDiff(prev.Spec, cur.Spec); ok {
		c := base
		c.Kind = KindProbe
		c.Summary = fmt.Sprintf("%s (%s)", what, revs)
		// Only the new pods have the probe.
		switch running := a.badWhere(func(p graph.Node) bool {
			return p.Status.Reason == "" && p.Status.Phase == "Running" && a.ownedBy(p, cur.ID)
		}); {
		case added == "readiness" && running > 0:
			c.Confidence = VeryLikely
			c.Evidence = []string{fmt.Sprintf("%s running but never ready since the probe was added", plural(running, "pod")+" "+isAre(running))}
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
			c.Summary = fmt.Sprintf("pod template changed (%s) in a way kubexray doesn't record: environment, command, probe settings or volumes", revs)
			c.Confidence = Possible
			if b.strong() {
				c.Confidence, c.Evidence = Likely, b.evidence("the rollout")
			}
		}
		out = append(out, c)
	}
	return out
}

func (a *analysis) ownedBy(p graph.Node, owner string) bool {
	return slices.ContainsFunc(a.x.in[p.ID], func(e graph.Edge) bool { return e.Type == graph.EdgeOwns && e.From == owner })
}

// firstPodOf returns when the oldest of the app's pods owned by a ReplicaSet
// was created.
func (a *analysis) firstPodOf(rs string) time.Time {
	var first time.Time
	for _, p := range a.pods {
		if a.ownedBy(p, rs) && (first.IsZero() || p.Created.Before(first)) {
			first = p.Created
		}
	}
	return first
}

// names reports whether a Kubernetes message names an object exactly: "Secret
// shop/db" isn't named by "... in Secret shop/db-credentials".
func names(msg, ref string) bool {
	nameChar := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' }
	for i := strings.Index(msg, ref); i >= 0; {
		end := i + len(ref)
		// Names may contain dots but don't end with one: "shop/db." ends the name.
		rest := msg[end:]
		if rest == "" || !nameChar(rest[0]) && (rest[0] != '.' || len(rest) == 1 || !nameChar(rest[1])) {
			return true
		}
		next := strings.Index(msg[end:], ref)
		if next < 0 {
			break
		}
		i = end + next
	}
	return false
}

// badWhere counts bad pods matching a condition.
func (a *analysis) badWhere(match func(graph.Node) bool) int {
	n := 0
	for _, p := range a.pods {
		if bad(p) && match(p) {
			n++
		}
	}
	return n
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
// whether its memory limit went down, and whether a request went up.
func resourceDiff(prev, cur *graph.Spec) (from, to string, memoryLowered, requestRaised, ok bool) {
	for _, c := range cur.Containers {
		for _, p := range prev.Containers {
			if p.Name != c.Name || p.Init != c.Init || (p.Requests == c.Requests && p.Limits == c.Limits) {
				continue
			}
			up := func(before, after string) bool { return after != "" && (before == "" || lower(before, after)) }
			raised := up(p.Requests.CPU, c.Requests.CPU) || up(p.Requests.Memory, c.Requests.Memory)
			return describeResources(p), describeResources(c), lower(c.Limits.Memory, p.Limits.Memory), raised, true
		}
	}
	return "", "", false, false, false
}

// probeDiff reports the first probe added to or removed from a container,
// and which probe was added. Only whether probes exist is recorded
// (ADR-0009), so changes to a probe's settings don't show here.
func probeDiff(prev, cur *graph.Spec) (what, added string, ok bool) {
	for _, c := range cur.Containers {
		for _, p := range prev.Containers {
			if p.Name != c.Name || p.Init != c.Init || p.Probes == c.Probes {
				continue
			}
			for _, k := range []struct {
				name     string
				was, now bool
			}{{"readiness", p.Probes.Readiness, c.Probes.Readiness}, {"liveness", p.Probes.Liveness, c.Probes.Liveness}, {"startup", p.Probes.Startup, c.Probes.Startup}} {
				switch {
				case k.now && !k.was:
					return fmt.Sprintf("%s probe added to %s", k.name, c.Name), k.name, true
				case k.was && !k.now:
					return fmt.Sprintf("%s probe removed from %s", k.name, c.Name), "", true
				}
			}
		}
	}
	return "", "", false
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
	verb := "changed"
	if created(a.app) {
		verb = "created" // a new app with a bad spec is still a cause
	}
	cause := Cause{
		Kind: KindChange, Object: a.app.ID, At: c.At, By: c, Origin: a.app.Origin, Confidence: Possible,
		Summary: fmt.Sprintf("%s %s was %s", a.app.Kind, a.app.Name, verb),
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
		if ch == nil || !a.inWindow(ch.At) || createdWith(n, a.app) {
			continue
		}
		c := Cause{
			Kind: KindConfig, Object: id, At: ch.At, By: ch, Origin: n.Origin, Confidence: Possible,
			Summary: fmt.Sprintf("%s %s was edited", kind, name),
		}
		b := a.split(ch.At)
		// The kubelet names the object it can't use: "couldn't find key
		// password in Secret shop/db-credentials".
		named := a.badWhere(func(p graph.Node) bool {
			return p.Status.Reason == "CreateContainerConfigError" && names(p.Status.Message, kind+" "+n.Namespace+"/"+name)
		})
		switch {
		case named > 0:
			c.Confidence = VeryLikely
			c.Evidence = []string{fmt.Sprintf("%s can't start: the kubelet can't use %s %s", plural(named, "pod"), kind, name)}
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
		if strings.TrimPrefix(r, "Init:") == "OOMKilled" {
			steps = append(steps, Step{Command: fmt.Sprintf("kubectl -n %s top pod %s --containers", ns, badPod), Note: "memory use now (needs metrics-server)"})
		}
	default:
		steps = append(steps, Step{Command: fmt.Sprintf("kubectl -n %s describe pod %s", ns, badPod), Note: "the pod's events"})
	}

	if len(inc.Causes) == 0 {
		return steps
	}
	top := inc.Causes[0]
	workload := strings.ToLower(app.Kind) + "/" + app.Name
	switch top.Kind {
	case KindImage, KindResources, KindTemplate, KindProbe:
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

// created reports whether an object's latest write is its creation.
func created(n graph.Node) bool {
	return n.Change != nil && !n.Created.IsZero() && n.Change.At.Sub(n.Created) <= attributionSlack
}

// createdWith reports whether a dependency was created together with its app
// (or before it) and never changed since: deploying a ConfigMap alongside its
// app isn't an edit that could break it. One created later, e.g. deleted and
// recreated with new contents, is still a candidate.
func createdWith(dep, app graph.Node) bool {
	return created(dep) && !dep.Created.After(app.Created.Add(attributionSlack))
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
