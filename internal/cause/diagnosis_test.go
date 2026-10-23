package cause

import (
	"strings"
	"testing"

	"github.com/kubexray/kubexray/internal/graph"
)

// sick builds the Deployment "api" with two pods in the given state, both
// running the container "app" with the given spec.
func sick(status graph.Status, ctr graph.Container) *cluster {
	c := newCluster()
	ctr.Name = "app"
	d := c.add(graph.Node{
		ID: graph.ID("Deployment", "shop", "api"), Kind: "Deployment", Namespace: "shop", Name: "api",
		Status: graph.Status{Replicas: &graph.Replicas{Desired: 2, Ready: 0, Updated: 2}},
		Spec:   &graph.Spec{Revision: "1", Containers: []graph.Container{ctr}},
	})
	for _, name := range []string{"api-a", "api-b"} {
		p := c.add(graph.Node{
			ID: graph.ID("Pod", "shop", name), Kind: "Pod", Namespace: "shop", Name: name, Created: mins(-10),
			Status: status, Spec: &graph.Spec{Containers: []graph.Container{ctr}},
		})
		c.edge(graph.EdgeOwns, d.ID, p.ID)
	}
	return c
}

func diagnosis(t *testing.T, c *cluster) Diagnosis {
	t.Helper()
	inc := analyze(t, c, mins(0))
	if inc.Diagnosis == nil {
		t.Fatalf("no diagnosis for %q", inc.Symptom.Summary)
	}
	return *inc.Diagnosis
}

func assertDiagnosis(t *testing.T, d Diagnosis, summary string, evidence ...string) {
	t.Helper()
	if !strings.Contains(d.Summary, summary) {
		t.Errorf("summary = %q, want it to contain %q", d.Summary, summary)
	}
	for _, e := range evidence {
		if !hasLine(d.Evidence, e) {
			t.Errorf("evidence %q lacks %q", d.Evidence, e)
		}
	}
}

func TestDiagnoseOutOfMemory(t *testing.T) {
	t.Parallel()
	oom := graph.Status{Phase: "Running", Reason: "OOMKilled", Container: "app", Exit: 137, Restarts: 4, Since: mins(-10)}
	withLimit := graph.Container{Limits: graph.Resources{Memory: "64Mi"}, Requests: graph.Resources{Memory: "32Mi"}}
	c := sick(oom, withLimit)
	assertDiagnosis(t, diagnosis(t, c), "The container app runs out of memory: Kubernetes kills it when it uses more than its 64Mi limit",
		"2 pods OOMKilled (exit code 137), 8 restarts in all", "memory limit 64Mi", "memory request 32Mi", "kubectl top")
	if inc := analyze(t, c, mins(0)); !hasStep(inc.Next, "top pod api-a --containers") {
		t.Errorf("next steps %+v lack kubectl top", inc.Next)
	}

	assertDiagnosis(t, diagnosis(t, sick(oom, graph.Container{})), "has no memory limit, so its node ran out", "no memory limit set")
}

func TestDiagnoseCrashes(t *testing.T) {
	t.Parallel()
	crash := func(code int32) graph.Status {
		return graph.Status{Phase: "Running", Reason: "CrashLoopBackOff", Container: "app", Exit: code, Restarts: 3, Since: mins(-10)}
	}
	assertDiagnosis(t, diagnosis(t, sick(crash(127), graph.Container{})), "keeps exiting with code 127: its command wasn't found in the image")
	assertDiagnosis(t, diagnosis(t, sick(crash(1), graph.Container{})), "code 1: a general error; its logs say why", "2 pods failing, 6 restarts in all")

	// Killed, with a liveness probe: likely the probe.
	d := diagnosis(t, sick(crash(137), graph.Container{Probes: graph.Probes{Liveness: true}}))
	assertDiagnosis(t, d, "it was killed (SIGKILL), possibly by its liveness probe", "it has a liveness probe")
	// Without one, the probe isn't blamed.
	if d := diagnosis(t, sick(crash(137), graph.Container{})); strings.Contains(d.Summary, "liveness") {
		t.Errorf("summary blames a probe that doesn't exist: %q", d.Summary)
	}
	// No exit code known: nothing more to say than the symptom.
	if inc := analyze(t, sick(crash(0), graph.Container{}), mins(0)); inc.Diagnosis != nil {
		t.Errorf("diagnosis without an exit code: %+v", inc.Diagnosis)
	}
}

func TestDiagnoseReadinessProbe(t *testing.T) {
	t.Parallel()
	running := graph.Status{Phase: "Running", Since: mins(-10)}
	assertDiagnosis(t, diagnosis(t, sick(running, graph.Container{Probes: graph.Probes{Readiness: true}})),
		"2 pods are running but failing their readiness probe, so Services send them no traffic")
	if inc := analyze(t, sick(running, graph.Container{}), mins(0)); inc.Diagnosis != nil {
		t.Errorf("readiness blamed without a readiness probe: %+v", inc.Diagnosis)
	}
}

func TestDiagnoseUnschedulable(t *testing.T) {
	t.Parallel()
	pending := func(msg string) graph.Status {
		return graph.Status{Phase: "Pending", Reason: "Unschedulable", Message: msg, Since: mins(-10)}
	}
	// The wording of Kubernetes 1.37, from the demo's unschedulable scenario.
	msg := "0/3 nodes are available: 1 node(s) had untolerated taint(s), 2 Insufficient memory. preemption: 0/3 nodes are available: 3 Preemption is not helpful for scheduling."
	d := diagnosis(t, sick(pending(msg), graph.Container{Requests: graph.Resources{Memory: "64Gi"}}))
	assertDiagnosis(t, d, "The scheduler can't place 2 pods: 2 nodes have too little free memory for its memory request; 1 node has a taint it doesn't tolerate",
		`the scheduler says: "0/3 nodes`, "memory request 64Gi")

	// Older wording, one reason.
	d = diagnosis(t, sick(pending("0/2 nodes are available: 2 node(s) didn't match Pod's node affinity/selector."), graph.Container{}))
	assertDiagnosis(t, d, "The scheduler can't place 2 pods: 2 nodes don't match its node selector or affinity")

	// Wording kubexray doesn't know still gives the scheduler's own message.
	d = diagnosis(t, sick(pending("0/3 nodes are available: 3 something new."), graph.Container{}))
	if d.Summary != "The scheduler can't place 2 pods" || !hasLine(d.Evidence, "something new") {
		t.Errorf("diagnosis = %+v", d)
	}
}

func TestDiagnosePendingVolume(t *testing.T) {
	t.Parallel()
	c := sick(graph.Status{Phase: "Pending", Reason: "Unschedulable", Message: "pod has unbound immediate PersistentVolumeClaims", Since: mins(-10)}, graph.Container{})
	c.add(graph.Node{ID: graph.ID("PersistentVolumeClaim", "shop", "data"), Kind: "PersistentVolumeClaim", Namespace: "shop", Name: "data", Status: graph.Status{Phase: "Pending"}})
	c.edge(graph.EdgeMounts, "Pod/shop/api-a", "PersistentVolumeClaim/shop/data")
	assertDiagnosis(t, diagnosis(t, c), "Its volume data is Pending: no storage has been provisioned for it", "PersistentVolumeClaim data is Pending")
}

func TestDiagnoseEvicted(t *testing.T) {
	t.Parallel()
	// A standalone pod: a controller would have replaced evicted pods.
	c := newCluster()
	c.add(graph.Node{
		ID: "Pod/shop/batch", Kind: "Pod", Namespace: "shop", Name: "batch", Created: mins(-10),
		Status: graph.Status{Phase: "Failed", Reason: "Evicted", Message: "The node was low on resource: memory. Threshold quantity: 100Mi, available: 50Mi.", Since: mins(-10)},
	})
	assertDiagnosis(t, diagnosis(t, c), "The kubelet evicted 1 pod: its node ran low on memory", `the kubelet says: "The node was low on resource: memory.`)
}

func TestDiagnoseConfigAndImages(t *testing.T) {
	t.Parallel()
	cfg := graph.Status{Phase: "Pending", Reason: "CreateContainerConfigError", Container: "app", Message: "couldn't find key MODE in ConfigMap shop/cfg", Since: mins(-10)}
	assertDiagnosis(t, diagnosis(t, sick(cfg, graph.Container{})), "Kubernetes can't create the container app: couldn't find key MODE in ConfigMap shop/cfg")

	pull := graph.Status{
		Phase: "Pending", Reason: "ErrImagePull", Container: "app", Since: mins(-10),
		Message: `rpc error: code = NotFound desc = failed to pull and unpack image "docker.io/library/nginx:1.72": not found`,
	}
	assertDiagnosis(t, diagnosis(t, sick(pull, graph.Container{Image: "nginx:1.72"})), "Kubernetes can't pull nginx:1.72: the image or tag doesn't exist", "2 pods ErrImagePull")

	denied := pull
	denied.Message = "pull access denied, repository does not exist or may require authorization"
	assertDiagnosis(t, diagnosis(t, sick(denied, graph.Container{Image: "acme/private:1"})), "the registry refused access; check imagePullSecrets")
}

func TestDiagnoseRefusedPods(t *testing.T) {
	t.Parallel()
	c := newCluster()
	c.add(graph.Node{
		ID: graph.ID("Deployment", "shop", "api"), Kind: "Deployment", Namespace: "shop", Name: "api",
		Status: graph.Status{
			Reason: "FailedCreate", Message: `pods "api-6d4-x" is forbidden: exceeded quota: compute, requested: memory=1Gi, used: memory=4Gi, limited: memory=4Gi`,
			Replicas: &graph.Replicas{Desired: 2},
		},
		Spec: &graph.Spec{Revision: "1"},
	})
	assertDiagnosis(t, diagnosis(t, c), "Kubernetes refused to create its pods: a ResourceQuota in shop is used up", "exceeded quota: compute")
}
