package cause

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/kubexray/kubexray/internal/graph"
)

// ingressHops lists the Ingresses that route to a Service, as "Ingress web".
func (x *index) ingressHops(svc string) []string {
	var hops []string
	for _, e := range x.in[svc] {
		if e.Type == graph.EdgeRoutes {
			hops = append(hops, "Ingress "+x.st.Nodes[e.From].Name)
		}
	}
	slices.Sort(hops)
	return hops
}

// routedServices returns the Services that would select an app's pods and
// that an Ingress routes traffic to. With no pods there are no selects edges
// to follow, so they're matched on the pods the app would create, the way
// the watcher matches pods.
func (x *index) routedServices(app graph.Node) []graph.Node {
	if app.Spec == nil || len(app.Spec.Labels) == 0 {
		return nil
	}
	var out []graph.Node
	for _, svc := range x.servicesIn(app.Namespace) {
		if len(svc.Selector) > 0 && labels.SelectorFromSet(svc.Selector).Matches(labels.Set(app.Spec.Labels)) && len(x.ingressHops(svc.ID)) > 0 {
			out = append(out, svc)
		}
	}
	return out
}

// servicesIn returns a namespace's Services, sorted by name, indexing them
// on first use.
func (x *index) servicesIn(ns string) []graph.Node {
	if x.services == nil {
		x.services = map[string][]graph.Node{}
		for _, n := range x.st.Nodes {
			if n.Kind == "Service" {
				x.services[n.Namespace] = append(x.services[n.Namespace], n)
			}
		}
		for _, list := range x.services {
			slices.SortFunc(list, func(a, b graph.Node) int { return strings.Compare(a.Name, b.Name) })
		}
	}
	return x.services[ns]
}

// scaledToZero explains a Deployment or StatefulSet scaled to zero while an
// Ingress still sends it traffic: not broken by its own measure (0 of 0
// ready), but its users get errors. It fills in inc and reports whether the
// app is in that state.
func (x *index) scaledToZero(app graph.Node, inc *Incident) bool {
	r := app.Status.Replicas
	if r == nil || r.Desired != 0 || (app.Kind != "Deployment" && app.Kind != "StatefulSet") || len(x.podsOf[app.ID]) > 0 {
		return false
	}
	services := x.routedServices(app)
	if len(services) == 0 {
		return false
	}
	inc.Symptom = Symptom{Summary: "scaled to 0 replicas"}
	var routes []string
	for _, svc := range services {
		route := strings.Join(x.ingressHops(svc.ID), ", ") + " → Service " + svc.Name
		routes = append(routes, route)
		inc.Impact = append(inc.Impact, route+" → no pods")
	}
	inc.Diagnosis = &Diagnosis{
		Summary:  fmt.Sprintf("%s has no pods: it's scaled to 0, but traffic is still routed to it", app.Name),
		Evidence: routes,
	}

	// Kubernetes doesn't record who scales an app: a write to the /scale
	// subresource (kubectl scale, autoscalers) leaves no managedFields entry.
	// A Deployment's controller scales its current ReplicaSet down at that
	// moment, which dates it; the app's latest write is the author only if it
	// was a scale or happened then (replicas: 0 applied by kubectl, Helm, a
	// GitOps tool).
	cause := Cause{Kind: KindScale, Object: app.ID, Origin: app.Origin, Confidence: VeryLikely, Summary: "scaled to 0"}
	if cur, _ := x.revisions(app); cur != nil && cur.Change != nil {
		cause.At = cur.Change.At
	}
	c := app.Change
	switch {
	case c != nil && (c.Via == "scale" || !cause.At.IsZero() && c.At.Sub(cause.At).Abs() <= attributionSlack):
		cause.At, cause.By = c.At, c
	default:
		cause.Evidence = []string{"Kubernetes doesn't record who scales an app, so kubexray can't say who did"}
	}
	inc.Since = cause.At
	if inc.Since.IsZero() && c != nil {
		inc.Since = c.At // it was scaled at or after its latest write
	}
	inc.Causes = []Cause{cause}
	inc.Next = []Step{{
		Command: fmt.Sprintf("kubectl -n %s scale %s/%s --replicas=<n>", app.Namespace, strings.ToLower(app.Kind), app.Name),
		Note:    "bring it back",
	}}
	return true
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
