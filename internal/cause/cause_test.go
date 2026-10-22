package cause

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

var t0 = time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)

// mins is t0 plus n minutes.
func mins(n float64) time.Time { return t0.Add(time.Duration(n * float64(time.Minute))) }

// cluster builds graph states for scenarios.
type cluster struct{ st *graph.State }

func newCluster() *cluster {
	return &cluster{st: &graph.State{Nodes: map[string]graph.Node{}, Edges: map[string]graph.Edge{}}}
}

func (c *cluster) add(n graph.Node) graph.Node {
	c.st.Nodes[n.ID] = n
	return n
}

func (c *cluster) edge(t graph.EdgeType, from, to string) {
	e := graph.NewEdge(t, from, to)
	c.st.Edges[e.ID] = e
}

func spec(rev, image string, limits ...string) *graph.Spec {
	ctr := graph.Container{Name: "api", Image: image}
	if len(limits) > 0 {
		ctr.Limits.Memory = limits[0]
	}
	return &graph.Spec{Revision: rev, Containers: []graph.Container{ctr}}
}

// deployment adds the Deployment "api" in namespace "shop".
func (c *cluster) deployment(rev, image string, desired, ready int32, change *graph.Change) graph.Node {
	return c.add(graph.Node{
		ID: graph.ID("Deployment", "shop", "api"), Kind: "Deployment", Namespace: "shop", Name: "api",
		Status: graph.Status{Ready: ready == desired, Replicas: &graph.Replicas{Desired: desired, Ready: ready, Updated: ready}},
		Spec:   spec(rev, image), Change: change,
	})
}

func (c *cluster) replicaSet(owner, name string, created time.Time, s *graph.Spec) graph.Node {
	rs := c.add(graph.Node{ID: graph.ID("ReplicaSet", "shop", name), Kind: "ReplicaSet", Namespace: "shop", Name: name, Created: created, Spec: s})
	c.edge(graph.EdgeOwns, owner, rs.ID)
	return rs
}

func (c *cluster) pod(owner, name string, created time.Time, ready bool, reason string, since time.Time) {
	p := c.add(graph.Node{
		ID: graph.ID("Pod", "shop", name), Kind: "Pod", Namespace: "shop", Name: name, Created: created,
		Parent: "Node/worker-1",
		Status: graph.Status{Phase: "Running", Ready: ready, Reason: reason, Since: since},
	})
	c.edge(graph.EdgeOwns, owner, p.ID)
}

func by(manager, tool, via string, at time.Time) *graph.Change {
	return &graph.Change{At: at, By: manager, Tool: tool, Via: via}
}

func analyze(t *testing.T, c *cluster, now time.Time) Incident {
	t.Helper()
	incs := Analyze(c.st, DefaultOptions(now))
	if len(incs) != 1 {
		t.Fatalf("Analyze found %d incidents, want 1", len(incs))
	}
	return incs[0]
}

func assertTop(t *testing.T, inc Incident, kind Kind, conf Confidence) Cause {
	t.Helper()
	if len(inc.Causes) == 0 {
		t.Fatalf("no causes found; want %s (%s)", kind, conf)
	}
	top := inc.Causes[0]
	if top.Kind != kind || top.Confidence != conf {
		t.Fatalf("top cause = %s (%s): %q; want %s (%s)\nall: %+v", top.Kind, top.Confidence, top.Summary, kind, conf, inc.Causes)
	}
	return top
}

func hasLine(lines []string, sub string) bool {
	return slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, sub) })
}

func hasStep(steps []Step, sub string) bool {
	return slices.ContainsFunc(steps, func(s Step) bool { return strings.Contains(s.Command, sub) })
}

// badImage: a rollout to an image that doesn't exist (the first demo story).
func badImage(origin *graph.Origin) *cluster {
	c := newCluster()
	d := c.deployment("2", "nginx:1.72", 3, 0, by("kubectl-set", "kubectl", "set", mins(0)))
	if origin != nil {
		d.Origin = origin
		c.add(d)
	}
	c.replicaSet(d.ID, "api-1", mins(-120), spec("1", "nginx:1.27"))
	rs := c.replicaSet(d.ID, "api-2", mins(0), spec("2", "nginx:1.72"))
	for _, n := range []string{"api-2-a", "api-2-b", "api-2-c"} {
		c.pod(rs.ID, n, mins(0), false, "ImagePullBackOff", mins(0.1))
	}
	svc := c.add(graph.Node{ID: "Service/shop/api", Kind: "Service", Namespace: "shop", Name: "api"})
	ing := c.add(graph.Node{ID: "Ingress/shop/web", Kind: "Ingress", Namespace: "shop", Name: "web"})
	c.edge(graph.EdgeRoutes, ing.ID, svc.ID)
	for _, n := range []string{"api-2-a", "api-2-b", "api-2-c"} {
		c.edge(graph.EdgeSelects, svc.ID, "Pod/shop/"+n)
	}
	return c
}

func TestBadImageIsVeryLikely(t *testing.T) {
	t.Parallel()
	inc := analyze(t, badImage(nil), mins(4))

	if !inc.Down() || inc.Symptom.Summary != "3 pods ImagePullBackOff" || !inc.Since.Equal(mins(0.1)) {
		t.Errorf("symptom = %+v since %v", inc.Symptom, inc.Since)
	}
	top := assertTop(t, inc, KindImage, VeryLikely)
	if top.From != "nginx:1.27" || top.To != "nginx:1.72" || top.By == nil || top.By.By != "kubectl-set" {
		t.Errorf("cause = %+v", top)
	}
	if !strings.Contains(top.Summary, "revision 1 → 2") || !hasLine(top.Evidence, "3 pods can't pull nginx:1.72") {
		t.Errorf("summary %q, evidence %v", top.Summary, top.Evidence)
	}
	if !hasLine(top.Evidence, "no other changes to api") {
		t.Errorf("a single candidate should say nothing else changed: %v", top.Evidence)
	}
	if want := "Ingress web → Service api → 0 of 3 pods ready"; !slices.Equal(inc.Impact, []string{want}) {
		t.Errorf("impact = %v, want %q", inc.Impact, want)
	}
	if !hasStep(inc.Next, "describe pod api-2-a") || !hasStep(inc.Next, "rollout undo deployment/api") {
		t.Errorf("next = %+v", inc.Next)
	}
}

func TestRollbackFollowsTheOrigin(t *testing.T) {
	t.Parallel()
	for tool, want := range map[string]string{"helm": "helm -n shop rollback shop", "argocd": "git revert", "flux": "git revert"} {
		inc := analyze(t, badImage(&graph.Origin{Tool: tool, Name: "shop"}), mins(4))
		if !hasStep(inc.Next, want) {
			t.Errorf("%s: next = %+v, want %q", tool, inc.Next, want)
		}
	}
}

// configThenRestart: the official demo story. A ConfigMap is edited; minutes
// later one pod restarts and crash-loops on the new value while older pods
// keep running on the old one.
func configThenRestart() *cluster {
	c := newCluster()
	d := c.deployment("1", "shop/api:3", 3, 2, by("kubectl-client-side-apply", "kubectl", "apply", mins(-60)))
	rs := c.replicaSet(d.ID, "api-1", mins(-60), spec("1", "shop/api:3"))
	c.pod(rs.ID, "api-1-old1", mins(-60), true, "", mins(-59))
	c.pod(rs.ID, "api-1-old2", mins(-60), true, "", mins(-59))
	c.pod(rs.ID, "api-1-new", mins(3), false, "CrashLoopBackOff", mins(3))
	for _, p := range []string{"api-1-old1", "api-1-old2", "api-1-new"} {
		c.edge(graph.EdgeMounts, "Pod/shop/"+p, "ConfigMap/shop/api-config")
		c.edge(graph.EdgeMounts, "Pod/shop/"+p, "ConfigMap/shop/kube-root-ca.crt")
	}
	c.add(graph.Node{
		ID: "ConfigMap/shop/api-config", Kind: "ConfigMap", Namespace: "shop", Name: "api-config",
		Change: by("kubectl-patch", "kubectl", "patch", mins(0)),
	})
	c.add(graph.Node{
		ID: "ConfigMap/shop/kube-root-ca.crt", Kind: "ConfigMap", Namespace: "shop", Name: "kube-root-ca.crt",
		Change: by("kube-controller-manager", "controller", "", mins(1)),
	})
	// An unrelated edit nearby must not be blamed.
	c.add(graph.Node{
		ID: "ConfigMap/shop/other", Kind: "ConfigMap", Namespace: "shop", Name: "other",
		Change: by("kubectl-edit", "kubectl", "edit", mins(2)),
	})
	return c
}

func TestConfigEditThenRestartIsLikely(t *testing.T) {
	t.Parallel()
	inc := analyze(t, configThenRestart(), mins(5))

	if inc.Down() || inc.Symptom.Summary != "1 pod CrashLoopBackOff" {
		t.Errorf("symptom = %+v", inc.Symptom)
	}
	top := assertTop(t, inc, KindConfig, Likely)
	if top.Object != "ConfigMap/shop/api-config" || top.By.By != "kubectl-patch" {
		t.Errorf("cause = %+v", top)
	}
	for _, want := range []string{"1 pod started after the edit is failing", "2 pods started before it are healthy"} {
		if !hasLine(top.Evidence, want) {
			t.Errorf("evidence %v lacks %q", top.Evidence, want)
		}
	}
	if len(inc.Causes) != 1 {
		t.Errorf("causes = %+v; unrelated and automatic ConfigMaps must not be candidates", inc.Causes)
	}
	if !hasStep(inc.Next, "logs api-1-new --previous") || !hasStep(inc.Next, "rollout restart deployment/api") {
		t.Errorf("next = %+v", inc.Next)
	}
}

// After a config edit, a rollout restart starts all-new pods that crash. The
// restart triggered it, but the edit is the cause.
func TestConfigEditBeatsTheRestartThatExposedIt(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("2", "shop/api:3", 2, 0, by("kubectl-rollout", "kubectl", "rollout", mins(5)))
	c.replicaSet(d.ID, "api-1", mins(-60), spec("1", "shop/api:3"))
	rs := c.replicaSet(d.ID, "api-2", mins(5), spec("2", "shop/api:3"))
	for _, p := range []string{"api-2-a", "api-2-b"} {
		c.pod(rs.ID, p, mins(5), false, "CrashLoopBackOff", mins(5.2))
		c.edge(graph.EdgeMounts, "Pod/shop/"+p, "ConfigMap/shop/api-config")
	}
	c.add(graph.Node{
		ID: "ConfigMap/shop/api-config", Kind: "ConfigMap", Namespace: "shop", Name: "api-config",
		Change: by("kubectl-patch", "kubectl", "patch", mins(0)),
	})

	inc := analyze(t, c, mins(8))
	assertTop(t, inc, KindConfig, Likely)
	if len(inc.Causes) != 2 || inc.Causes[1].Kind != KindRestart || inc.Causes[1].Confidence != Possible {
		t.Errorf("want the restart listed second as possible: %+v", inc.Causes)
	}
}

func TestMissingSecretIsVeryLikely(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("1", "shop/api:3", 1, 0, nil)
	rs := c.replicaSet(d.ID, "api-1", mins(-1), spec("1", "shop/api:3"))
	c.pod(rs.ID, "api-1-a", mins(-1), false, "CreateContainerConfigError", mins(-1))
	c.edge(graph.EdgeMounts, "Pod/shop/api-1-a", "Secret/shop/db-credentials")

	top := assertTop(t, analyze(t, c, mins(1)), KindMissing, VeryLikely)
	if top.Summary != "Secret db-credentials doesn't exist, but the pods use it" {
		t.Errorf("summary = %q", top.Summary)
	}
}

func TestLoweredMemoryLimitThenOOMIsVeryLikely(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("2", "shop/api:3", 2, 0, by("helm", "helm", "", mins(0)))
	c.replicaSet(d.ID, "api-1", mins(-60), spec("1", "shop/api:3", "512Mi"))
	rs := c.replicaSet(d.ID, "api-2", mins(0), spec("2", "shop/api:3", "128Mi"))
	c.pod(rs.ID, "api-2-a", mins(0), false, "OOMKilled", mins(1))
	c.pod(rs.ID, "api-2-b", mins(0), false, "CrashLoopBackOff", mins(1))

	top := assertTop(t, analyze(t, c, mins(3)), KindResources, VeryLikely)
	if !strings.Contains(top.Summary, "memory limit 512Mi → memory limit 128Mi") || top.By.Tool != "helm" {
		t.Errorf("cause = %+v", top)
	}
}

func TestNodeProblemIsVeryLikely(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.add(graph.Node{
		ID: "StatefulSet/shop/db", Kind: "StatefulSet", Namespace: "shop", Name: "db",
		Status: graph.Status{Replicas: &graph.Replicas{Desired: 1}},
	})
	c.pod(d.ID, "db-0", mins(-60), false, "", mins(0))
	c.add(graph.Node{ID: "Node/worker-1", Kind: "Node", Name: "worker-1", Status: graph.Status{Ready: false, Reason: "KubeletNotReady", Since: mins(-0.5)}})

	top := assertTop(t, analyze(t, c, mins(2)), KindNode, VeryLikely)
	if top.Summary != "node worker-1 is not ready (KubeletNotReady)" || !top.At.Equal(mins(-0.5)) {
		t.Errorf("cause = %+v", top)
	}
}

func TestNoCauseIsSaidHonestly(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("1", "shop/api:3", 1, 0, by("kubectl-client-side-apply", "kubectl", "apply", mins(-600)))
	rs := c.replicaSet(d.ID, "api-1", mins(-600), spec("1", "shop/api:3"))
	c.pod(rs.ID, "api-1-a", mins(-600), false, "CrashLoopBackOff", mins(0))

	inc := analyze(t, c, mins(2))
	if len(inc.Causes) != 0 {
		t.Errorf("causes = %+v; changes outside the lookback window must not be blamed", inc.Causes)
	}
	if !hasStep(inc.Next, "logs api-1-a --previous") {
		t.Errorf("next = %+v; logs should still be suggested", inc.Next)
	}
}

func TestChangesAfterTheIncidentStartedAreNotCauses(t *testing.T) {
	t.Parallel()
	c := configThenRestart()
	cm := c.st.Nodes["ConfigMap/shop/api-config"]
	cm.Change = by("kubectl-patch", "kubectl", "patch", mins(4)) // after the crash at +3m
	c.add(cm)
	if inc := analyze(t, c, mins(5)); len(inc.Causes) != 0 {
		t.Errorf("causes = %+v", inc.Causes)
	}
}

func TestGracePeriod(t *testing.T) {
	t.Parallel()
	c := badImage(nil)
	if incs := Analyze(c.st, DefaultOptions(mins(0.1).Add(10*time.Second))); len(incs) != 0 {
		t.Errorf("Analyze reported an incident 10s in: %+v", incs)
	}
	inc, err := Explain(c.st, "Deployment/shop/api", DefaultOptions(mins(0.1).Add(10*time.Second)))
	if err != nil || inc.Healthy {
		t.Errorf("Explain = %+v, %v; asking explicitly ignores the grace period", inc, err)
	}
}

func TestStatefulSetChangeWithBeforeAfterEvidence(t *testing.T) {
	t.Parallel()
	c := newCluster()
	s := c.add(graph.Node{
		ID: "StatefulSet/shop/db", Kind: "StatefulSet", Namespace: "shop", Name: "db",
		Status: graph.Status{Replicas: &graph.Replicas{Desired: 2, Ready: 1}},
		Change: by("kubectl-edit", "kubectl", "edit", mins(0)),
	})
	c.pod(s.ID, "db-0", mins(-60), true, "", mins(-60))
	c.pod(s.ID, "db-1", mins(1), false, "CrashLoopBackOff", mins(1))

	top := assertTop(t, analyze(t, c, mins(3)), KindChange, Likely)
	if top.Summary != "StatefulSet db was changed" {
		t.Errorf("summary = %q", top.Summary)
	}
}

func TestHealthyAppsAreNotIncidents(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("1", "shop/api:3", 2, 2, by("kubectl-set", "kubectl", "set", mins(-1)))
	rs := c.replicaSet(d.ID, "api-1", mins(-1), spec("1", "shop/api:3"))
	c.pod(rs.ID, "api-1-a", mins(-1), true, "", mins(-1))
	c.pod(rs.ID, "api-1-b", mins(-1), true, "", mins(-1))
	// Terminating pods from the previous rollout don't count.
	c.pod(rs.ID, "api-0-x", mins(-2), false, "Terminating", mins(-1))

	if incs := Analyze(c.st, DefaultOptions(mins(5))); len(incs) != 0 {
		t.Errorf("Analyze = %+v", incs)
	}
	inc, err := Explain(c.st, "Pod/shop/api-1-a", DefaultOptions(mins(5)))
	if err != nil || !inc.Healthy || inc.App != "Deployment/shop/api" || inc.LastChange == nil {
		t.Errorf("Explain(pod) = %+v, %v; want the healthy Deployment with its last change", inc, err)
	}
}

func TestExplainErrors(t *testing.T) {
	t.Parallel()
	c := badImage(nil)
	if _, err := Explain(c.st, "Service/shop/api", DefaultOptions(mins(1))); !errors.Is(err, ErrNotAnApp) {
		t.Errorf("Explain(Service) = %v, want ErrNotAnApp", err)
	}
	if _, err := Explain(c.st, "Deployment/shop/nope", DefaultOptions(mins(1))); err == nil {
		t.Error("Explain of a missing object succeeded")
	}
}

func TestRollbackUsesTheDeploymentChangeTime(t *testing.T) {
	t.Parallel()
	// Rolled back to revision 1's template: Kubernetes reuses the old
	// ReplicaSet (created long ago) and gives it revision 3.
	c := newCluster()
	d := c.deployment("3", "nginx:1.26", 2, 0, by("kubectl", "kubectl", "", mins(0)))
	c.replicaSet(d.ID, "api-2", mins(-30), spec("2", "nginx:1.27"))
	rs := c.replicaSet(d.ID, "api-1", mins(-600), spec("3", "nginx:1.26"))
	c.pod(rs.ID, "api-1-a", mins(0), false, "CrashLoopBackOff", mins(0.5))

	top := assertTop(t, analyze(t, c, mins(2)), KindImage, Possible)
	if !top.At.Equal(mins(0)) || top.From != "nginx:1.27" || top.To != "nginx:1.26" {
		t.Errorf("cause = %+v", top)
	}
}

func TestChanges(t *testing.T) {
	t.Parallel()
	c := badImage(nil)
	c.add(graph.Node{
		ID: "ConfigMap/shop/api-config", Kind: "ConfigMap", Namespace: "shop", Name: "api-config",
		Change: by("kubectl-patch", "kubectl", "patch", mins(-5)),
	})
	c.add(graph.Node{
		ID: "ConfigMap/shop/old", Kind: "ConfigMap", Namespace: "shop", Name: "old",
		Change: by("kubectl-patch", "kubectl", "patch", mins(-90)),
	})
	c.add(graph.Node{
		ID: "Service/shop/auto", Kind: "Service", Namespace: "shop", Name: "auto",
		Change: by("kube-controller-manager", "controller", "", mins(-1)),
	})

	got := Changes(c.st, mins(-60), false)
	if len(got) != 2 {
		t.Fatalf("changes = %+v, want the rollout and the ConfigMap edit", got)
	}
	if got[0].Object != "Deployment/shop/api" || got[0].Summary != "rolled out revision 2: image nginx:1.27 → nginx:1.72" || got[0].By.By != "kubectl-set" {
		t.Errorf("newest = %+v", got[0])
	}
	if got[1].Object != "ConfigMap/shop/api-config" || got[1].Summary != "edited (contents not read)" {
		t.Errorf("second = %+v", got[1])
	}
	if all := Changes(c.st, mins(-60), true); len(all) != 3 {
		t.Errorf("with controllers: %d changes, want 3", len(all))
	}
}

func TestConfidenceText(t *testing.T) {
	t.Parallel()
	for c, want := range map[Confidence]string{VeryLikely: "very likely", Likely: "likely", Possible: "possible", 0: "unknown"} {
		if b, _ := c.MarshalText(); string(b) != want {
			t.Errorf("%d = %q, want %q", c, b, want)
		}
	}
}

// Review regressions.

func TestRecoveredPodIsHealthy(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("1", "shop/api:3", 1, 1, nil)
	rs := c.replicaSet(d.ID, "api-1", mins(-60*24*7), spec("1", "shop/api:3"))
	// Crashed once a week ago; the mapper keeps "Error" as its reason.
	c.pod(rs.ID, "api-1-a", mins(-60*24*7), true, "Error", mins(-60*24*6))
	p := c.st.Nodes["Pod/shop/api-1-a"]
	p.Status.Restarts = 1
	c.add(p)

	if incs := Analyze(c.st, DefaultOptions(mins(0))); len(incs) != 0 {
		t.Errorf("a recovered pod made a healthy app an incident: %+v", incs)
	}
}

func TestFinishedPodsDontCount(t *testing.T) {
	t.Parallel()
	c := newCluster()
	// A completed Job.
	job := c.add(graph.Node{ID: "Job/shop/backup", Kind: "Job", Namespace: "shop", Name: "backup", Status: graph.Status{Phase: "Complete", Ready: true}})
	c.pod(job.ID, "backup-x", mins(-10), false, "Completed", mins(-9))
	done := c.st.Nodes["Pod/shop/backup-x"]
	done.Status.Phase = "Succeeded"
	c.add(done)
	// A Deployment with a leftover evicted pod and its healthy replacement.
	d := c.deployment("1", "shop/api:3", 1, 1, nil)
	rs := c.replicaSet(d.ID, "api-1", mins(-60), spec("1", "shop/api:3"))
	c.pod(rs.ID, "api-1-evicted", mins(-60), false, "Evicted", mins(-5))
	ev := c.st.Nodes["Pod/shop/api-1-evicted"]
	ev.Status.Phase = "Failed"
	c.add(ev)
	c.pod(rs.ID, "api-1-new", mins(-5), true, "", mins(-5))

	if incs := Analyze(c.st, DefaultOptions(mins(0))); len(incs) != 0 {
		t.Errorf("finished pods made incidents: %+v", incs)
	}
}

func TestFailedJobIsAnIncident(t *testing.T) {
	t.Parallel()
	c := newCluster()
	job := c.add(graph.Node{ID: "Job/shop/migrate", Kind: "Job", Namespace: "shop", Name: "migrate", Status: graph.Status{Phase: "Failed", Reason: "BackoffLimitExceeded"}})
	c.pod(job.ID, "migrate-x", mins(-5), false, "Error", mins(-4))
	failed := c.st.Nodes["Pod/shop/migrate-x"]
	failed.Status.Phase = "Failed"
	c.add(failed)
	c.add(graph.Node{ID: "Secret/shop/db", Kind: "Secret", Namespace: "shop", Name: "db", Change: by("kubectl-edit", "kubectl", "edit", mins(-6))})
	c.edge(graph.EdgeMounts, "Pod/shop/migrate-x", "Secret/shop/db")

	inc := analyze(t, c, mins(0))
	if inc.Symptom.Summary != "1 pod Error" {
		t.Errorf("symptom = %+v", inc.Symptom)
	}
	if hasStep(inc.Next, "rollout restart") {
		t.Errorf("next = %+v; Jobs don't support rollout restart", inc.Next)
	}
}

func TestExplainReplicaSetResolvesToItsDeployment(t *testing.T) {
	t.Parallel()
	inc, err := Explain(badImage(nil).st, "ReplicaSet/shop/api-2", DefaultOptions(mins(4)))
	if err != nil || inc.App != "Deployment/shop/api" || len(inc.Causes) == 0 {
		t.Errorf("Explain(rs) = %+v, %v", inc, err)
	}
}

func TestLaterScaleDoesntMakeAnOldRolloutFresh(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("2", "nginx:1.72", 5, 3, by("kubectl-edit", "kubectl", "edit", mins(-5))) // a scale
	c.replicaSet(d.ID, "api-1", mins(-300), spec("1", "nginx:1.27"))
	rs := c.replicaSet(d.ID, "api-2", mins(-180), spec("2", "nginx:1.72"))
	c.pod(rs.ID, "api-2-a", mins(-180), true, "", mins(-180))
	c.pod(rs.ID, "api-2-b", mins(-4), false, "CrashLoopBackOff", mins(-3.5))

	for _, cs := range analyze(t, c, mins(0)).Causes {
		if cs.Kind == KindImage {
			t.Errorf("a 3-hour-old rollout was blamed: %+v", cs)
		}
	}
}

func TestCordonedNodeIsNotACause(t *testing.T) {
	t.Parallel()
	c := badImage(nil)
	c.add(graph.Node{ID: "Node/worker-1", Kind: "Node", Name: "worker-1", Status: graph.Status{Ready: true, Reason: "SchedulingDisabled", Since: mins(2)}})
	for _, cs := range analyze(t, c, mins(4)).Causes {
		if cs.Kind == KindNode {
			t.Errorf("cordoned node blamed: %+v", cs)
		}
	}
}

func TestCrashLoopStartIsWhenThePodStarted(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("2", "shop/api:4", 1, 0, by("kubectl-set", "kubectl", "set", mins(0)))
	c.replicaSet(d.ID, "api-1", mins(-300), spec("1", "shop/api:3"))
	rs := c.replicaSet(d.ID, "api-2", mins(0), spec("2", "shop/api:4"))
	// An hour of crash-looping: Ready flipped on every restart.
	c.pod(rs.ID, "api-2-a", mins(0), false, "CrashLoopBackOff", mins(57))
	p := c.st.Nodes["Pod/shop/api-2-a"]
	p.Status.Restarts = 14
	c.add(p)

	inc := analyze(t, c, mins(60))
	if !inc.Since.Equal(mins(0)) {
		t.Errorf("since = %v, want when the pod started", inc.Since)
	}
	assertTop(t, inc, KindImage, Possible)
}

func TestMissingOptionalReferenceWithRunningPodsIsOnlyPossible(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("1", "shop/api:3", 1, 0, nil)
	rs := c.replicaSet(d.ID, "api-1", mins(-5), spec("1", "shop/api:3"))
	c.pod(rs.ID, "api-1-a", mins(-5), false, "", mins(-4)) // running, readiness failing
	c.edge(graph.EdgeMounts, "Pod/shop/api-1-a", "ConfigMap/shop/optional-flags")

	top := assertTop(t, analyze(t, c, mins(0)), KindMissing, Possible)
	if len(top.Evidence) > 1 {
		t.Errorf("evidence = %v; pods that started weren't stopped by it", top.Evidence)
	}
}

func TestTiesGoToTheChangeClosestToTheStart(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("1", "shop/api:3", 1, 0, nil)
	rs := c.replicaSet(d.ID, "api-1", mins(-600), spec("1", "shop/api:3"))
	c.pod(rs.ID, "api-1-a", mins(-600), false, "CrashLoopBackOff", mins(0))
	for name, at := range map[string]time.Time{"before": mins(0).Add(-3 * time.Second), "after": mins(0).Add(9 * time.Second)} {
		id := "ConfigMap/shop/" + name
		c.add(graph.Node{ID: id, Kind: "ConfigMap", Namespace: "shop", Name: name, Change: by("kubectl-patch", "kubectl", "patch", at)})
		c.edge(graph.EdgeMounts, "Pod/shop/api-1-a", id)
	}
	if top := assertTop(t, analyze(t, c, mins(2)), KindConfig, Possible); top.Object != "ConfigMap/shop/before" {
		t.Errorf("top = %s; the edit 3s before the start is closer than the one 9s after", top.Object)
	}
}

func TestNoCauseMessageUsesTheLookback(t *testing.T) {
	t.Parallel()
	c := newCluster()
	d := c.deployment("1", "shop/api:3", 1, 0, nil)
	rs := c.replicaSet(d.ID, "api-1", mins(-600), spec("1", "shop/api:3"))
	c.pod(rs.ID, "api-1-a", mins(-600), false, "CrashLoopBackOff", mins(0))
	opts := DefaultOptions(mins(2))
	opts.Lookback = 2 * time.Hour
	inc, _ := Explain(c.st, "Deployment/shop/api", opts)
	if inc.NoCause != "nothing about api or what it uses changed in the 2 hours before it broke" {
		t.Errorf("NoCause = %q", inc.NoCause)
	}
}
