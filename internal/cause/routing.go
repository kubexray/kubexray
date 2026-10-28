package cause

import (
	"fmt"
	"strings"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// routing covers what sits between users and the app's pods, and its
// autoscaler (ADR-0018 records their diffs). A routing change can't make
// pods crash, so it's only a candidate when requests fail or slow down.
func (a *analysis) routing() []Cause {
	sig := a.inc.Signal
	traffic := sig != nil && (sig.Kind == ErrorsSignal || sig.Kind == LatencySignal)
	var out []Cause
	add := func(n graph.Node, kind Kind) {
		if n.Change == nil || !a.inWindow(n.Change.At) || n.Change.Tool == "controller" {
			return
		}
		verb := "changed"
		if created(n) {
			verb = "created"
		}
		c := Cause{
			Kind: kind, Object: n.ID, At: n.Change.At, By: n.Change, Origin: n.Origin, Confidence: Possible,
			Summary: fmt.Sprintf("%s %s was %s", n.Kind, n.Name, verb),
		}
		if gap := sig.Since.Sub(n.Change.At); traffic && kind == KindRouting && gap >= 0 && gap <= trafficHold+trafficEvery {
			c.Confidence = Likely
			when := "within a minute of it"
			if gap >= time.Minute {
				when = duration(gap) + " after it"
			}
			c.Evidence = []string{fmt.Sprintf("requests started %s %s", failingWord(sig), when)}
		}
		out = append(out, c)
	}

	if traffic {
		services := map[string]bool{}
		for _, e := range a.x.st.Edges {
			if e.Type == graph.EdgeSelects && a.x.appOf(e.To) == a.app.ID && !services[e.From] {
				services[e.From] = true
				if n, ok := a.x.st.Nodes[e.From]; ok {
					add(n, KindRouting)
				}
			}
		}
		for _, e := range a.x.st.Edges {
			if e.Type == graph.EdgeRoutes && services[e.To] {
				if n, ok := a.x.st.Nodes[e.From]; ok {
					add(n, KindRouting)
				}
			}
		}
		for _, n := range a.x.st.Nodes {
			if n.Kind == "NetworkPolicy" && n.Namespace == a.app.Namespace && a.selectsPods(n) {
				add(n, KindRouting)
			}
		}
	}
	if sig != nil || a.inc.Symptom.Ready < a.inc.Symptom.Desired {
		for _, n := range a.x.st.Nodes {
			// What it targets is a value: known only when values are on.
			if n.Kind == "HorizontalPodAutoscaler" && n.Namespace == a.app.Namespace &&
				n.Manifest["spec.scaleTargetRef.name"].Text == a.app.Name && n.Manifest["spec.scaleTargetRef.kind"].Text == a.app.Kind {
				add(n, KindAutoscaler)
			}
		}
	}
	return out
}

// trafficEvery is how often traffic is read: a break is seen up to this
// long after it begins.
const trafficEvery = time.Minute

func failingWord(s *Signal) string {
	if s.Kind == LatencySignal {
		return "slowing down"
	}
	return "failing"
}

// selectsPods reports whether a NetworkPolicy's podSelector, as its
// manifest records it, matches the app's pods: matchLabels only, or an
// empty selector, which selects every pod in the namespace.
func (a *analysis) selectsPods(np graph.Node) bool {
	if v, ok := np.Manifest["spec.podSelector"]; ok && v.Text == "{}" {
		return true
	}
	const prefix = "spec.podSelector.matchLabels"
	want := map[string]string{}
	for path, v := range np.Manifest {
		if rest, ok := strings.CutPrefix(path, prefix); ok && v.Text != "" {
			key := strings.TrimPrefix(rest, ".")
			if strings.HasPrefix(rest, "[") {
				key = strings.Trim(rest, `[]"`)
			}
			want[key] = v.Text
		}
	}
	if len(want) == 0 {
		return false
	}
	for _, p := range a.pods {
		match := true
		for k, v := range want {
			if p.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
