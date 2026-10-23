package cause

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/kubexray/kubexray/internal/graph"
)

// routedServices returns the Services that select an app's pods and that an
// Ingress routes traffic to, matched on the pods the app would create: with
// no pods, there are no selects edges to follow.
func (x *index) routedServices(app graph.Node) []graph.Node {
	if app.Spec == nil || len(app.Spec.Labels) == 0 {
		return nil
	}
	var out []graph.Node
	for _, n := range x.st.Nodes {
		if n.Kind != "Service" || n.Namespace != app.Namespace || len(n.Selector) == 0 {
			continue
		}
		matches := true
		for k, v := range n.Selector {
			if app.Spec.Labels[k] != v {
				matches = false
				break
			}
		}
		routed := slices.ContainsFunc(x.in[n.ID], func(e graph.Edge) bool { return e.Type == graph.EdgeRoutes })
		if matches && routed {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b graph.Node) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// scaledToZero explains a Deployment or StatefulSet scaled to zero while an
// Ingress still sends it traffic: not broken by its own measure (0 of 0
// ready), but its users get errors.
func (x *index) scaledToZero(app graph.Node) (Incident, bool) {
	r := app.Status.Replicas
	if r == nil || r.Desired != 0 || (app.Kind != "Deployment" && app.Kind != "StatefulSet") || len(x.podsOf[app.ID]) > 0 {
		return Incident{}, false
	}
	services := x.routedServices(app)
	if len(services) == 0 {
		return Incident{}, false
	}
	inc := Incident{
		App: app.ID, Kind: app.Kind, Namespace: app.Namespace, Name: app.Name, LastChange: app.Change,
		Symptom: Symptom{Summary: "scaled to 0 replicas"},
	}
	var routes []string
	for _, svc := range services {
		var ingresses []string
		for _, e := range x.in[svc.ID] {
			if e.Type == graph.EdgeRoutes {
				ingresses = append(ingresses, "Ingress "+x.st.Nodes[e.From].Name)
			}
		}
		slices.Sort(ingresses)
		route := strings.Join(ingresses, ", ") + " → Service " + svc.Name
		routes = append(routes, route)
		inc.Impact = append(inc.Impact, route+" → no pods")
	}
	inc.Diagnosis = &Diagnosis{
		Summary:  fmt.Sprintf("%s has no pods: it's scaled to 0, but traffic is still routed to it", app.Name),
		Evidence: routes,
	}
	// Kubernetes doesn't record who scales an app: a scale through the /scale
	// subresource (kubectl scale, autoscalers) leaves no managedFields entry.
	// Its latest write counts only if it was itself a scale. The time is
	// still known for a Deployment: its controller scaled the current
	// ReplicaSet down at that moment.
	cause := Cause{Kind: KindScale, Object: app.ID, Origin: app.Origin, Confidence: VeryLikely, Summary: "scaled to 0"}
	if c := app.Change; c != nil && c.Via == "scale" {
		cause.At, cause.By = c.At, c
	} else {
		if cur, _ := x.revisions(app); cur != nil && cur.Change != nil {
			cause.At = cur.Change.At
		}
		cause.Evidence = []string{"Kubernetes doesn't record who scales an app, so kubexray can't say who did"}
	}
	inc.Since = cause.At
	inc.Causes = []Cause{cause}
	inc.Next = []Step{{
		Command: fmt.Sprintf("kubectl -n %s scale %s/%s --replicas=<n>", app.Namespace, strings.ToLower(app.Kind), app.Name),
		Note:    "bring it back",
	}}
	return inc, true
}

// quota finds the ResourceQuota that refused the app's pods ("exceeded
// quota: compute, requested: …"), if someone changed it in the window.
func (a *analysis) quota() []Cause {
	_, rest, ok := strings.Cut(a.app.Status.Message, "exceeded quota: ")
	if !ok {
		return nil
	}
	name, _, _ := strings.Cut(rest, ",")
	id := graph.ID("ResourceQuota", a.app.Namespace, strings.TrimSpace(name))
	q, ok := a.x.st.Nodes[id]
	if !ok || q.Change == nil || !a.inWindow(q.Change.At) {
		return nil
	}
	what := "edited"
	if created(q) {
		what = "created"
	}
	return []Cause{{
		Kind: KindQuota, Object: id, At: q.Change.At, By: q.Change, Origin: q.Origin, Confidence: VeryLikely,
		Summary:  fmt.Sprintf("ResourceQuota %s was %s", q.Name, what),
		Evidence: []string{fmt.Sprintf("Kubernetes refuses %s's new pods: they would exceed quota %s", a.app.Name, q.Name)},
	}}
}
