package recommend

import (
	"context"
	"strings"
	"testing"

	admv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func obj(t *testing.T, o runtime.Object, kind, apiVersion string) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetKind(kind)
	u.SetAPIVersion(apiVersion)
	return u
}

func configMap(t *testing.T, ns, name, logLevel string) *unstructured.Unstructured {
	return obj(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Data:       map[string]string{"LOG_LEVEL": logLevel, "PAYMENT_URL": "http://payments"},
	}, "ConfigMap", "v1")
}

// guardrail is a policy on ConfigMaps with these match conditions and
// validation, bound with Warn and Audit.
func guardrail(match []admv1.MatchCondition, expr string, resources ...string) Guardrail {
	if len(resources) == 0 {
		resources = []string{"configmaps"}
	}
	return Guardrail{
		Policy: &admv1.ValidatingAdmissionPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "kubexray-test"},
			Spec: admv1.ValidatingAdmissionPolicySpec{
				MatchConstraints: &admv1.MatchResources{ResourceRules: []admv1.NamedRuleWithOperations{{
					RuleWithOperations: admv1.RuleWithOperations{
						Operations: []admv1.OperationType{admv1.Create, admv1.Update},
						Rule:       admv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: resources},
					},
				}}},
				MatchConditions: match,
				Validations: []admv1.Validation{{
					Expression:        expr,
					MessageExpression: "'LOG_LEVEL must be debug, info, warn or error, not ' + object.data.LOG_LEVEL",
				}},
			},
		},
		Binding: &admv1.ValidatingAdmissionPolicyBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "kubexray-test"},
			Spec: admv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        "kubexray-test",
				ValidationActions: []admv1.ValidationAction{admv1.Warn, admv1.Audit},
			},
		},
	}
}

var (
	checkoutConfig = []admv1.MatchCondition{{Name: "checkout-config", Expression: "object.metadata.namespace == 'shop' && object.metadata.name == 'checkout-config'"}}
	logLevels      = "!has(object.data.LOG_LEVEL) || object.data.LOG_LEVEL in ['debug', 'info', 'warn', 'error']"
	scope          = Scope{Kind: "ConfigMap", Namespace: "shop"}
)

// demoCases are the demo incident: verbose broke checkout, info fixed it,
// and other ConfigMaps in the cluster are running fine.
func demoCases(t *testing.T) []Case {
	fixed := configMap(t, "shop", "checkout-config", "info")
	return []Case{
		{Check: "rejects the change that broke checkout", Label: "LOG_LEVEL=verbose", Object: configMap(t, "shop", "checkout-config", "verbose"), Old: fixed, Want: Reject},
		{Check: "accepts the fix", Label: "LOG_LEVEL=info", Object: fixed, Old: configMap(t, "shop", "checkout-config", "verbose"), Want: Accept},
		{Check: "accepts everything running now", Label: "ConfigMap shop/checkout-config", Object: fixed, Want: Accept},
		{Check: "accepts everything running now", Label: "ConfigMap shop/api-config", Object: configMap(t, "shop", "api-config", "trace"), Want: Accept},
		{Check: "accepts everything running now", Label: "ConfigMap default/other", Object: configMap(t, "default", "other", "verbose"), Want: Accept},
	}
}

func names(p Proof) map[string]bool {
	out := map[string]bool{}
	for _, c := range p.Checks {
		out[c.Name] = c.Passed
	}
	return out
}

func TestProveTheDemoGuardrail(t *testing.T) {
	p := Prove(context.Background(), guardrail(checkoutConfig, logLevels), scope, demoCases(t))
	if !p.Passed() {
		t.Fatalf("the demo guardrail should pass: %v", p.Failures())
	}
	if len(p.Checks) != 5 || p.Checks[0].Name != CheckCompiles || p.Checks[1].Name != CheckNarrow {
		t.Errorf("checks = %+v", p.Checks)
	}
	if c := p.Checks[4]; c.Name != "accepts everything running now" || c.Tried != 3 {
		t.Errorf("running check = %+v", c)
	}
}

func TestProveRefusesBadGuardrails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		g      Guardrail
		failed string // the check that must fail
		says   string // in its failures
	}{
		{"misses the change", guardrail(checkoutConfig, "object.data.LOG_LEVEL != ''"), "rejects the change that broke checkout", "should be refused"},
		{"blocks the fix", guardrail(checkoutConfig, "object.data.LOG_LEVEL == 'debug'"), "accepts the fix", "should be allowed"},
		{"blocks everything", guardrail(checkoutConfig, "false"), "accepts everything running now", "should be allowed"},
		{"applies in every namespace", guardrail(nil, logLevels), CheckNarrow, "every namespace"},
		{"other kinds", guardrail(checkoutConfig, logLevels, "configmaps", "secrets"), CheckNarrow, "not only configmaps"},
		{"wildcard", guardrail(checkoutConfig, logLevels, "*"), CheckNarrow, "wildcard"},
		{"doesn't compile", guardrail(checkoutConfig, "object.data.LOG_LEVEL in ["), CheckCompiles, "doesn't compile"},
		{"wrong type", guardrail(checkoutConfig, "object.data.LOG_LEVEL"), CheckCompiles, ""},
		{"uses the authorizer", guardrail(checkoutConfig, "authorizer.group('').resource('pods').check('get').allowed()"), CheckCompiles, "authorizer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Prove(context.Background(), tc.g, scope, demoCases(t))
			if p.Passed() {
				t.Fatal("passed")
			}
			for _, c := range p.Checks {
				if c.Name != tc.failed {
					continue
				}
				if c.Passed {
					t.Fatalf("check %q passed: %+v", tc.failed, p.Checks)
				}
				if !strings.Contains(strings.Join(c.Failures, " "), tc.says) {
					t.Errorf("failures %q don't say %q", c.Failures, tc.says)
				}
				return
			}
			t.Fatalf("no check %q: %+v", tc.failed, p.Checks)
		})
	}
}

func TestProveParamsAndBindings(t *testing.T) {
	g := guardrail(checkoutConfig, logLevels)
	g.Policy.Spec.ParamKind = &admv1.ParamKind{APIVersion: "v1", Kind: "ConfigMap"}
	if p := Prove(context.Background(), g, scope, demoCases(t)); p.Passed() || !strings.Contains(p.Failures()[0], "parameters") {
		t.Errorf("params: %v", p.Failures())
	}
	g = guardrail(checkoutConfig, logLevels)
	g.Binding.Spec.PolicyName = "other"
	if p := Prove(context.Background(), g, scope, demoCases(t)); p.Passed() {
		t.Error("a binding for another policy passed")
	}
	// A binding can narrow the policy to the namespace instead of a match
	// condition.
	g = guardrail(nil, logLevels)
	g.Binding.Spec.MatchResources = &admv1.MatchResources{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: "shop"}}}
	p := Prove(context.Background(), g, scope, demoCases(t))
	if names(p)[CheckNarrow] != true {
		t.Errorf("a namespace-scoped binding isn't narrow: %v", p.Failures())
	}
}

// A workload guardrail: reports' memory limit can't drop below 128Mi again.
func TestProveAWorkloadGuardrail(t *testing.T) {
	deploy := func(limit string) *unstructured.Unstructured {
		return obj(t, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "reports"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "reports", Image: "busybox",
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}},
			}}}}},
		}, "Deployment", "apps/v1")
	}
	g := guardrail(
		[]admv1.MatchCondition{{Name: "reports", Expression: "object.metadata.namespace == 'shop' && object.metadata.name == 'reports'"}},
		"object.spec.template.spec.containers.all(c, !has(c.resources.limits) || !('memory' in c.resources.limits) || quantity(c.resources.limits.memory).compareTo(quantity('128Mi')) >= 0)",
		"deployments",
	)
	g.Policy.Spec.MatchConstraints.ResourceRules[0].APIGroups = []string{"apps"}
	g.Policy.Spec.Validations[0].MessageExpression = "'reports needs at least 128Mi of memory'"
	cases := []Case{
		{Check: "rejects the change that broke reports", Label: "16Mi", Object: deploy("16Mi"), Old: deploy("128Mi"), Want: Reject},
		{Check: "accepts the fix", Label: "128Mi", Object: deploy("128Mi"), Old: deploy("16Mi"), Want: Accept},
		{Check: "accepts every healthy recorded version", Label: "256Mi", Object: deploy("256Mi"), Want: Accept},
	}
	p := Prove(context.Background(), g, Scope{Kind: "Deployment", Namespace: "shop"}, cases)
	if !p.Passed() {
		t.Fatalf("failures: %v", p.Failures())
	}
}
