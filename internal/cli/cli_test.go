package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

func TestParseTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"deploy/api", "Deployment/shop/api", true},
		{"Deployment/api", "Deployment/shop/api", true},
		{"sts/db", "StatefulSet/shop/db", true},
		{"ds/agent", "DaemonSet/shop/agent", true},
		{"job/backup", "Job/shop/backup", true},
		{"pod/api-7d9f-x2k", "Pod/shop/api-7d9f-x2k", true},
		{"api", "", false},
		{"deploy/", "", false},
		{"svc/api", "", false},
	}
	for _, tt := range tests {
		got, err := ParseTarget(tt.in, "shop")
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ParseTarget(%q) = %q, %v; want %q (ok=%v)", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

// Times are rendered in local time; pin the zone so output is stable.
func init() { time.Local = time.UTC }

var now = time.Date(2026, 10, 23, 14, 4, 0, 0, time.UTC)

func TestWhyIncident(t *testing.T) {
	t.Parallel()
	inc := cause.Incident{
		App: "Deployment/shop/api", Kind: "Deployment", Namespace: "shop", Name: "api",
		Since:   time.Date(2026, 10, 23, 14, 2, 11, 0, time.UTC),
		Symptom: cause.Symptom{Summary: "3 pods ImagePullBackOff", Ready: 0, Desired: 3, BadPods: []string{"a", "b", "c"}},
		Impact:  []string{"Ingress web → Service api → 0 of 3 pods ready"},
		Causes: []cause.Cause{
			{
				Kind: cause.KindImage, Summary: "image changed nginx:1.27 → nginx:1.72 (revision 13 → 14)", Confidence: cause.VeryLikely,
				At: time.Date(2026, 10, 23, 14, 2, 9, 0, time.UTC), By: &graph.Change{Tool: "helm", By: "helm"},
				Evidence: []string{"3 pods can't pull nginx:1.72"},
			},
			{
				Kind: cause.KindConfig, Summary: "ConfigMap api-config was edited", Confidence: cause.Possible,
				At: time.Date(2026, 10, 23, 13, 50, 0, 0, time.UTC), By: &graph.Change{Tool: "kubectl", Via: "patch", By: "kubectl-patch"},
			},
		},
		Next: []cause.Step{
			{Command: "kubectl -n shop describe pod api-2-a", Note: "the pod's events"},
			{Command: "helm -n shop rollback shop", Note: "roll back the Helm release"},
		},
	}
	var out strings.Builder
	if err := Why(&out, inc, now); err != nil {
		t.Fatal(err)
	}
	want := `api (Deployment, shop) is down: 0 of 3 pods ready, since 14:02:11 (1m ago)
  impact            Ingress web → Service api → 0 of 3 pods ready
  symptom           3 pods ImagePullBackOff
  very likely cause image changed nginx:1.27 → nginx:1.72 (revision 13 → 14) by helm at 14:02:09 (1m ago)
  evidence          3 pods can't pull nginx:1.72
  also changed      ConfigMap api-config was edited by kubectl patch at 13:50:00 (14m ago)  (possible)
  next              kubectl -n shop describe pod api-2-a   # the pod's events
                    helm -n shop rollback shop             # roll back the Helm release
`
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestWhyNoCause(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	_ = Why(&out, cause.Incident{
		Kind: "Deployment", Namespace: "shop", Name: "api",
		Symptom: cause.Symptom{Summary: "1 pod CrashLoopBackOff", Ready: 1, Desired: 2, BadPods: []string{"x"}},
		NoCause: "nothing about api or what it uses changed in the 30 minutes before it broke",
	}, now)
	for _, want := range []string{"is degraded", "none found: nothing about api", "outside the cluster"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestWhyDiagnosis(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	_ = Why(&out, cause.Incident{
		Kind: "Deployment", Namespace: "shop", Name: "api",
		Symptom: cause.Symptom{Summary: "2 pods Unschedulable", Ready: 0, Desired: 2, BadPods: []string{"x", "y"}},
		Diagnosis: &cause.Diagnosis{
			Summary:  "The scheduler can't place 2 pods: no node has enough free memory for its memory request",
			Evidence: []string{`the scheduler says: "0/3 nodes are available: 3 Insufficient memory."`, "memory request 64Gi"},
		},
		NoCause: "nothing about api or what it uses changed in the 30 minutes before it broke",
	}, now)
	want := `  symptom           2 pods Unschedulable
  diagnosis         The scheduler can't place 2 pods: no node has enough free memory for its memory request
  based on          the scheduler says: "0/3 nodes are available: 3 Insufficient memory."
                    memory request 64Gi
  cause             none found: nothing about api or what it uses changed in the 30 minutes before it broke.
`
	if !strings.HasSuffix(out.String(), want) {
		t.Errorf("output:\n%s\nwant it to end with:\n%s", out.String(), want)
	}
}

func TestWhyHealthy(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	_ = Why(&out, cause.Incident{
		Kind: "Deployment", Namespace: "shop", Name: "api", Healthy: true,
		Symptom:    cause.Symptom{Ready: 3, Desired: 3},
		LastChange: &graph.Change{At: now.Add(-3 * time.Hour), Tool: "argocd", By: "argocd-controller"},
	}, now)
	want := "api (Deployment, shop) is healthy: 3 of 3 pods ready\n  last change       argocd, 3h ago\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestChanges(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	_ = Changes(&out, []cause.Change{
		{
			At: now.Add(-2 * time.Minute), Kind: "ConfigMap", NS: "shop", Name: "api-config", Summary: "edited (contents not read)",
			By: &graph.Change{Tool: "kubectl", Via: "patch"},
		},
		{At: now.Add(-50 * time.Hour), Kind: "Namespace", Name: "shop", Summary: "changed", By: &graph.Change{Tool: "other", By: "terraform"}},
	}, now, time.Hour)
	want := "14:02:00  2m ago    ConfigMap shop/api-config edited (contents not read)  by kubectl patch\n" +
		"12:04:00  2d ago    Namespace shop changed  by terraform\n\n" +
		"Showing the latest change per object. Keep kubexray running to record every change.\n"
	if out.String() != want {
		t.Errorf("output:\n%q\nwant:\n%q", out.String(), want)
	}

	out.Reset()
	_ = Changes(&out, nil, now, time.Hour)
	if out.String() != "No changes in the last 1h0m0s.\n" {
		t.Errorf("empty output = %q", out.String())
	}
}

func TestIncidents(t *testing.T) {
	t.Parallel()
	node := cause.Cause{Kind: cause.KindNode, Object: "Node/worker-2", Summary: "node worker-2 is not ready", Confidence: cause.VeryLikely, At: now.Add(-time.Minute)}
	incs := []cause.Incident{
		{App: "Deployment/shop/api", Namespace: "shop", Name: "api", Symptom: cause.Symptom{Summary: "1 pod not ready", Ready: 1, Desired: 2, BadPods: []string{"x"}}, Causes: []cause.Cause{node}},
		{App: "Deployment/shop/checkout", Namespace: "shop", Name: "checkout", Symptom: cause.Symptom{Summary: "2 pods not ready", Desired: 2, BadPods: []string{"y"}}, Causes: []cause.Cause{node}},
		{App: "Deployment/shop/batch", Namespace: "shop", Name: "batch", Symptom: cause.Symptom{Summary: "1 pod CrashLoopBackOff", Desired: 1, BadPods: []string{"z"}}},
	}
	var out strings.Builder
	_ = Incidents(&out, incs, cause.GroupIncidents(incs), now)
	// Both entries have an app that's down; the larger one comes first.
	want := "node worker-2 is not ready at 14:03:00 (1m ago): 2 apps affected\n" +
		"                    shop/checkout is down: 2 pods not ready\n" +
		"                    shop/api is degraded: 1 pod not ready\n\n" +
		"shop/batch is down: 1 pod CrashLoopBackOff\n" +
		"  cause             none found\n\n" +
		"Run kubexray why <kind>/<name> -n <namespace> for the evidence and next steps.\n"
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
	out.Reset()
	_ = Incidents(&out, nil, nil, now)
	if !strings.Contains(out.String(), "No incidents") {
		t.Errorf("empty = %q", out.String())
	}
}

func TestAgo(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{-time.Second: "just now", 5 * time.Second: "5s ago", 90 * time.Second: "1m ago", 3 * time.Hour: "3h ago", 72 * time.Hour: "3d ago"} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}
