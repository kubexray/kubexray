package cause

import (
	"testing"

	"github.com/kubexray/kubexray/internal/graph"
)

// scaledDown builds api scaled to 0, its Service, and optionally an Ingress
// routing to it.
func scaledDown(routed bool, change *graph.Change) *cluster {
	c := newCluster()
	c.add(graph.Node{
		ID: "Deployment/shop/api", Kind: "Deployment", Namespace: "shop", Name: "api", Created: mins(-60),
		Status: graph.Status{Ready: true, Replicas: &graph.Replicas{}},
		Spec:   &graph.Spec{Revision: "1", Labels: map[string]string{"app": "api", "tier": "web"}},
		Change: change,
	})
	c.add(graph.Node{ID: "Service/shop/api", Kind: "Service", Namespace: "shop", Name: "api", Selector: map[string]string{"app": "api"}})
	c.add(graph.Node{ID: "Service/shop/other", Kind: "Service", Namespace: "shop", Name: "other", Selector: map[string]string{"app": "other"}})
	if routed {
		c.add(graph.Node{ID: "Ingress/shop/web", Kind: "Ingress", Namespace: "shop", Name: "web"})
		c.edge(graph.EdgeRoutes, "Ingress/shop/web", "Service/shop/api")
		c.edge(graph.EdgeRoutes, "Ingress/shop/web", "Service/shop/other")
	}
	return c
}

func TestScaledToZeroBehindAnIngress(t *testing.T) {
	t.Parallel()
	inc := analyze(t, scaledDown(true, by("kubectl-scale", "kubectl", "scale", mins(0))), mins(2))
	if inc.State() != "is scaled to zero" || inc.Symptom.Summary != "scaled to 0 replicas" {
		t.Errorf("state %q, symptom %+v", inc.State(), inc.Symptom)
	}
	if len(inc.Impact) != 1 || inc.Impact[0] != "Ingress web → Service api → no pods" {
		t.Errorf("impact = %v", inc.Impact)
	}
	top := assertTop(t, inc, KindScale, VeryLikely)
	if top.By == nil || top.By.Via != "scale" || !hasStep(inc.Next, "scale deployment/api --replicas=<n>") {
		t.Errorf("cause %+v, next %+v", top, inc.Next)
	}
	if inc.Diagnosis == nil || inc.Diagnosis.Summary != "api has no pods: it's scaled to 0, but traffic is still routed to it" {
		t.Errorf("diagnosis = %+v", inc.Diagnosis)
	}
}

func TestScaledToZeroIsFineWithoutTraffic(t *testing.T) {
	t.Parallel()
	// Nothing routes to it: scaling to zero is a normal thing to do.
	if incs := Analyze(scaledDown(false, by("kubectl-scale", "kubectl", "scale", mins(0))).st, DefaultOptions(mins(2))); len(incs) != 0 {
		t.Errorf("incidents = %+v", incs)
	}
	// Within the grace period, it isn't an incident yet.
	if incs := Analyze(scaledDown(true, by("kubectl-scale", "kubectl", "scale", mins(0))).st, DefaultOptions(mins(0.2))); len(incs) != 0 {
		t.Errorf("incident during the grace period: %+v", incs)
	}
	// Scaled down long ago: the incident starts with the scale, which still
	// explains it.
	inc := analyze(t, scaledDown(true, by("kubectl-scale", "kubectl", "scale", mins(-600))), mins(2))
	if top := assertTop(t, inc, KindScale, VeryLikely); !top.At.Equal(mins(-600)) {
		t.Errorf("cause at %v", top.At)
	}
}

func TestScaleIsNotCreditedToAnOlderWrite(t *testing.T) {
	t.Parallel()
	// kubectl scale leaves no managedFields entry: the Deployment's latest
	// write is an older kubectl set. Its current ReplicaSet was scaled down
	// by the controller at the time of the scale.
	c := scaledDown(true, by("kubectl-set", "kubectl", "set", mins(-50)))
	rs := c.replicaSet("Deployment/shop/api", "api-2", mins(-50), spec("2", "nginx"))
	rs.Change = by("kube-controller-manager", "controller", "", mins(0))
	c.add(rs)
	c.replicaSet("Deployment/shop/api", "api-1", mins(-100), spec("1", "nginx"))
	d := c.st.Nodes["Deployment/shop/api"]
	d.Spec.Revision = "2"
	c.add(d)

	inc := analyze(t, c, mins(2))
	top := assertTop(t, inc, KindScale, VeryLikely)
	if top.By != nil || !top.At.Equal(mins(0)) || !inc.Since.Equal(mins(0)) || !hasLine(top.Evidence, "can't say who") {
		t.Errorf("cause = %+v, since %v", top, inc.Since)
	}
}

func TestQuotaRefusingPods(t *testing.T) {
	t.Parallel()
	// A restart can't create pods; the old ones still serve.
	c := newCluster()
	d := c.deployment("2", "nginx", 3, 3, by("kubectl-rollout", "kubectl", "rollout", mins(0)))
	d.Status.Reason, d.Status.Since = "FailedCreate", mins(0)
	d.Status.Message = `pods "api-2-x" is forbidden: exceeded quota: compute, requested: requests.memory=16Mi, used: requests.memory=112Mi, limited: requests.memory=64Mi`
	c.add(d)
	rs := c.replicaSet(d.ID, "api-1", mins(-60), spec("1", "nginx"))
	for _, n := range []string{"api-1-a", "api-1-b", "api-1-c"} {
		c.pod(rs.ID, n, mins(-60), true, "", mins(-60))
	}
	c.add(graph.Node{
		ID: "ResourceQuota/shop/compute", Kind: "ResourceQuota", Namespace: "shop", Name: "compute", Created: mins(-1), Status: graph.Status{Ready: true},
		Change: by("kubectl-create", "kubectl", "create", mins(-1)),
	})

	inc := analyze(t, c, mins(2))
	if inc.State() != "can't create new pods" || inc.Symptom.Summary != "new pods can't be created" || !inc.Since.Equal(mins(0)) {
		t.Errorf("state %q, symptom %+v, since %v", inc.State(), inc.Symptom, inc.Since)
	}
	top := assertTop(t, inc, KindQuota, VeryLikely)
	if top.Summary != "ResourceQuota compute was created" || !hasStep(inc.Next, "describe resourcequota compute") {
		t.Errorf("cause %+v, next %+v", top, inc.Next)
	}
}
