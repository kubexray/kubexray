// Package recommend turns an incident into a safety recommendation: an
// admission policy that would have stopped it, proven against the recording
// before anyone sees it (ADR-0014).
//
// The proof evaluates the policy with the API server's own admission code
// (k8s.io/apiserver), locally: nothing is written to the cluster.
package recommend

import (
	"context"
	"fmt"
	"slices"
	"strings"

	admv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/cel/environment"
	"sigs.k8s.io/yaml"
)

// Guardrail is a recommended policy and the binding that enables it.
type Guardrail struct {
	Policy  *admv1.ValidatingAdmissionPolicy        `json:"policy"`
	Binding *admv1.ValidatingAdmissionPolicyBinding `json:"binding"`
}

// YAML is the guardrail as one file for kubectl apply: the policy, then the
// binding.
func (g Guardrail) YAML() ([]byte, error) {
	p, err := yaml.Marshal(g.Policy)
	if err != nil {
		return nil, err
	}
	b, err := yaml.Marshal(g.Binding)
	if err != nil {
		return nil, err
	}
	return slices.Concat(p, []byte("---\n"), b), nil
}

// Want is what a case expects of the policy.
type Want string

// The two things a case can want.
const (
	Reject Want = "reject" // the policy must refuse it
	Accept Want = "accept" // the policy must allow it
)

// Case is one object the policy is tried on: created when Old is nil,
// updated from Old otherwise.
type Case struct {
	Check  string // which check it belongs to, e.g. "rejects the change that broke checkout"
	Label  string // what it is, e.g. "ConfigMap shop/checkout-config with LOG_LEVEL=verbose"
	Object *unstructured.Unstructured
	Old    *unstructured.Unstructured
	Want   Want
}

// Check is one part of the proof and its outcome.
type Check struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Tried    int      `json:"tried,omitempty"`    // objects tried
	Failures []string `json:"failures,omitempty"` // what went wrong, for the reader and the model
}

// Proof is every check run on a guardrail.
type Proof struct {
	Checks []Check `json:"checks"`
}

// Passed reports whether every check passed.
func (p Proof) Passed() bool {
	return len(p.Checks) > 0 && !slices.ContainsFunc(p.Checks, func(c Check) bool { return !c.Passed })
}

// Failures lists what failed, to show the model what to fix.
func (p Proof) Failures() []string {
	var out []string
	for _, c := range p.Checks {
		for _, f := range c.Failures {
			out = append(out, c.Name+": "+f)
		}
	}
	return out
}

// Scope is what the guardrail may apply to: the incident's kind, in its
// namespace.
type Scope struct {
	Kind      string // e.g. ConfigMap
	Namespace string
}

// Check names, in the order they run (ADR-0014, section 3).
const (
	CheckCompiles = "compiles"
	CheckNarrow   = "applies only to this app's objects"
)

// Prove runs the checks: the policy compiles, it's narrow, and every case
// gets what it wants. Cases are grouped into checks by Case.Check, in order.
func Prove(ctx context.Context, g Guardrail, scope Scope, cases []Case) Proof {
	var p Proof
	v, err := compile(g)
	if err != nil {
		p.Checks = append(p.Checks, Check{Name: CheckCompiles, Failures: []string{err.Error()}})
		return p
	}
	p.Checks = append(p.Checks, Check{Name: CheckCompiles, Passed: true})

	narrow := Check{Name: CheckNarrow}
	narrow.Failures = narrowness(ctx, g, v, scope)
	narrow.Passed = len(narrow.Failures) == 0
	p.Checks = append(p.Checks, narrow)

	var order []string
	byCheck := map[string]*Check{}
	for _, c := range cases {
		chk, ok := byCheck[c.Check]
		if !ok {
			chk = &Check{Name: c.Check, Passed: true}
			byCheck[c.Check] = chk
			order = append(order, c.Check)
		}
		chk.Tried++
		if msg := try(ctx, g, v, c); msg != "" {
			chk.Passed = false
			if len(chk.Failures) < maxFailures {
				chk.Failures = append(chk.Failures, msg)
			}
		}
	}
	for _, name := range order {
		p.Checks = append(p.Checks, *byCheck[name])
	}
	return p
}

// maxFailures caps the failures kept per check: enough to fix the policy.
const maxFailures = 5

// try runs one case and returns what went wrong, if anything.
func try(ctx context.Context, g Guardrail, v validating.Validator, c Case) string {
	decision, message := evaluate(ctx, g, v, c.Object, c.Old)
	switch {
	case c.Want == Reject && decision != decided:
		return fmt.Sprintf("%s: should be refused, but was %s", c.Label, decision)
	case c.Want == Accept && decision == decided:
		return fmt.Sprintf("%s: should be allowed, but was refused (%s)", c.Label, message)
	case c.Want == Accept && decision == errored:
		return fmt.Sprintf("%s: the policy failed on it (%s)", c.Label, message)
	}
	return ""
}

// outcome is what the policy did with an object.
type outcome string

const (
	notApplied outcome = "not checked by the policy"
	admitted   outcome = "allowed"
	decided    outcome = "refused"
	errored    outcome = "an evaluation error"
)

// evaluate runs the policy on an object as the API server would, for a
// create (old nil) or an update.
func evaluate(ctx context.Context, g Guardrail, v validating.Validator, obj, old *unstructured.Unstructured) (outcome, string) {
	op := admission.Create
	if old != nil {
		op = admission.Update
	}
	gvk := obj.GroupVersionKind()
	gvr, ok := resources[gvk.Kind]
	if !ok {
		return errored, "kubexray doesn't know the resource for kind " + gvk.Kind
	}
	ns := nsObject(obj.GetNamespace())
	if !matches(g, gvr, op, obj, ns) {
		return notApplied, ""
	}
	var oldObj runtime.Object // a typed nil would look like an old object
	if old != nil {
		oldObj = old
	}
	attrs := admission.NewAttributesRecord(obj, oldObj, gvk, obj.GetNamespace(), obj.GetName(), gvr, "", op, nil, false, &user.DefaultInfo{Name: "kubexray"})
	va := &admission.VersionedAttributes{Attributes: attrs, VersionedKind: gvk, VersionedObject: admission.NewLazyObject(obj)}
	if old != nil {
		va.VersionedOldObject = admission.NewLazyObject(old)
	}
	res := v.Validate(ctx, gvr, va, nil, ns, celconfig.RuntimeCELCostBudget, denyAll{})
	if len(res.Decisions) == 0 {
		return notApplied, "" // its match conditions excluded the object
	}
	result := admitted
	var messages []string
	for _, d := range res.Decisions {
		switch {
		case d.Evaluation == validating.EvalError:
			return errored, d.Message
		case d.Action == validating.ActionDeny:
			result = decided
			messages = append(messages, d.Message)
		}
	}
	return result, strings.Join(messages, "; ")
}

// compile builds the policy's validator the way the API server's
// ValidatingAdmissionPolicy plugin does (validating.compilePolicy), refusing
// what a recommendation must not use.
func compile(g Guardrail) (validating.Validator, error) {
	p := g.Policy
	switch {
	case p == nil || g.Binding == nil:
		return nil, fmt.Errorf("a guardrail needs a policy and a binding")
	case p.Spec.ParamKind != nil || g.Binding.Spec.ParamRef != nil:
		return nil, fmt.Errorf("parameters aren't allowed: the policy must stand on its own")
	case len(p.Spec.Validations) == 0:
		return nil, fmt.Errorf("the policy has no validations")
	case g.Binding.Spec.PolicyName != p.Name:
		return nil, fmt.Errorf("the binding names policy %q, not %q", g.Binding.Spec.PolicyName, p.Name)
	case len(g.Binding.Spec.ValidationActions) == 0:
		return nil, fmt.Errorf("the binding has no validation actions")
	}
	for _, e := range expressions(p) {
		if strings.Contains(e, "authorizer") {
			return nil, fmt.Errorf("expressions may not use the authorizer: %s", e)
		}
	}
	opt := cel.OptionalVariableDeclarations{HasAuthorizer: true}
	msgOpt := cel.OptionalVariableDeclarations{}
	compiler, err := cel.NewCompositedCompiler(environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()))
	if err != nil {
		return nil, err
	}
	vars := make([]cel.NamedExpressionAccessor, len(p.Spec.Variables))
	for i, v := range p.Spec.Variables {
		vars[i] = &validating.Variable{Name: v.Name, Expression: v.Expression}
	}
	compiler.CompileAndStoreVariables(vars, opt, environment.StoredExpressions)

	var matcher matchconditions.Matcher
	if mc := p.Spec.MatchConditions; len(mc) > 0 {
		acc := make([]cel.ExpressionAccessor, len(mc))
		for i := range mc {
			acc[i] = (*matchconditions.MatchCondition)(&mc[i])
		}
		matcher = matchconditions.NewMatcher(compiler.CompileCondition(acc, opt, environment.StoredExpressions), p.Spec.FailurePolicy, "policy", "validate", p.Name)
	}
	validations := make([]cel.ExpressionAccessor, len(p.Spec.Validations))
	messages := make([]cel.ExpressionAccessor, len(p.Spec.Validations))
	for i, v := range p.Spec.Validations {
		validations[i] = &validating.ValidationCondition{Expression: v.Expression, Message: v.Message, Reason: v.Reason}
		if v.MessageExpression != "" {
			messages[i] = &validating.MessageExpressionCondition{MessageExpression: v.MessageExpression}
		}
	}
	v := validating.NewValidator(
		compiler.CompileCondition(validations, opt, environment.StoredExpressions),
		matcher,
		compiler.CompileCondition(nil, opt, environment.StoredExpressions),
		compiler.CompileCondition(messages, msgOpt, environment.StoredExpressions),
		p.Spec.FailurePolicy,
		nil,
	)
	// Compile errors surface when an expression is evaluated; find them now
	// so a broken draft fails here, not as a mystery on every case.
	if err := compileErrors(compiler, p, opt); err != nil {
		return nil, err
	}
	return v, nil
}

// compileErrors compiles each expression on its own to report the first
// error with the expression it's in.
func compileErrors(c *cel.CompositedCompiler, p *admv1.ValidatingAdmissionPolicy, opt cel.OptionalVariableDeclarations) error {
	check := func(kind, expr string) error {
		res := c.CompileCELExpression(&validating.ValidationCondition{Expression: expr}, opt, environment.StoredExpressions)
		if res.Error != nil {
			return fmt.Errorf("%s %q doesn't compile: %s", kind, expr, res.Error.Detail)
		}
		return nil
	}
	for _, m := range p.Spec.MatchConditions {
		if err := check("match condition", m.Expression); err != nil {
			return err
		}
	}
	for _, v := range p.Spec.Validations {
		if err := check("validation", v.Expression); err != nil {
			return err
		}
		if v.MessageExpression != "" {
			res := c.CompileCELExpression(&validating.MessageExpressionCondition{MessageExpression: v.MessageExpression}, cel.OptionalVariableDeclarations{}, environment.StoredExpressions)
			if res.Error != nil {
				return fmt.Errorf("message expression %q doesn't compile: %s", v.MessageExpression, res.Error.Detail)
			}
		}
	}
	return nil
}

func expressions(p *admv1.ValidatingAdmissionPolicy) []string {
	out := make([]string, 0, len(p.Spec.Variables)+len(p.Spec.MatchConditions)+2*len(p.Spec.Validations))
	for _, v := range p.Spec.Variables {
		out = append(out, v.Expression)
	}
	for _, m := range p.Spec.MatchConditions {
		out = append(out, m.Expression)
	}
	for _, v := range p.Spec.Validations {
		out = append(out, v.Expression, v.MessageExpression)
	}
	return out
}

// denyAll answers authorizer checks; recommendations may not use them.
type denyAll struct{}

func (denyAll) Authorize(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
	return authorizer.DecisionDeny, "kubexray recommendations don't use the authorizer", nil
}

// resources maps the kinds recommendations cover to their API resources.
var resources = map[string]schema.GroupVersionResource{
	"ConfigMap":     {Version: "v1", Resource: "configmaps"},
	"ResourceQuota": {Version: "v1", Resource: "resourcequotas"},
	"Deployment":    {Group: "apps", Version: "v1", Resource: "deployments"},
	"StatefulSet":   {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"DaemonSet":     {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"Job":           {Group: "batch", Version: "v1", Resource: "jobs"},
}

// nsObject is a namespace with the label Kubernetes sets on every one.
func nsObject(name string) *corev1.Namespace {
	if name == "" {
		return nil
	}
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelMetadataName: name}}}
}

// matches reports whether the policy and binding select a request, as the
// API server's matcher does for the rules recommendations use: resource
// rules, exclusions, and namespace and object label selectors.
func matches(g Guardrail, gvr schema.GroupVersionResource, op admission.Operation, obj *unstructured.Unstructured, ns *corev1.Namespace) bool {
	if !matchResources(g.Policy.Spec.MatchConstraints, gvr, op, obj, ns, true) {
		return false
	}
	return matchResources(g.Binding.Spec.MatchResources, gvr, op, obj, ns, false)
}

func matchResources(m *admv1.MatchResources, gvr schema.GroupVersionResource, op admission.Operation, obj *unstructured.Unstructured, ns *corev1.Namespace, required bool) bool {
	if m == nil {
		return !required // a binding without matchResources takes the policy's
	}
	if len(m.ResourceRules) > 0 && !slices.ContainsFunc(m.ResourceRules, func(r admv1.NamedRuleWithOperations) bool { return ruleMatches(r, gvr, op, obj.GetName()) }) {
		return false
	}
	if required && len(m.ResourceRules) == 0 {
		return false
	}
	if slices.ContainsFunc(m.ExcludeResourceRules, func(r admv1.NamedRuleWithOperations) bool { return ruleMatches(r, gvr, op, obj.GetName()) }) {
		return false
	}
	if !selects(m.ObjectSelector, obj.GetLabels()) {
		return false
	}
	if ns != nil && !selects(m.NamespaceSelector, ns.Labels) {
		return false
	}
	return true
}

func ruleMatches(r admv1.NamedRuleWithOperations, gvr schema.GroupVersionResource, op admission.Operation, name string) bool {
	if len(r.ResourceNames) > 0 && !slices.Contains(r.ResourceNames, name) {
		return false
	}
	return has(r.Operations, admv1.OperationType(op)) && has(r.APIGroups, gvr.Group) && has(r.APIVersions, gvr.Version) && has(r.Resources, gvr.Resource)
}

func has[T ~string](list []T, v T) bool {
	return slices.Contains(list, v) || slices.Contains(list, "*")
}

func selects(sel *metav1.LabelSelector, set map[string]string) bool {
	if sel == nil {
		return true
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false
	}
	return s.Matches(labels.Set(set))
}

// narrowness lists why a guardrail could reach past the incident: wildcard
// rules, or applying to the same kind in another namespace or to another
// kind (ADR-0014, section 3, check 2).
func narrowness(ctx context.Context, g Guardrail, v validating.Validator, scope Scope) []string {
	var out []string
	gvr, ok := resources[scope.Kind]
	if !ok {
		return []string{"kubexray doesn't recommend guardrails for " + scope.Kind}
	}
	if mc := g.Policy.Spec.MatchConstraints; mc != nil {
		for _, r := range mc.ResourceRules {
			if slices.Contains(r.Resources, "*") || slices.Contains(r.APIGroups, "*") {
				out = append(out, "its resource rules use a wildcard")
			}
			if slices.ContainsFunc(r.Resources, func(res string) bool { return res != gvr.Resource }) {
				out = append(out, fmt.Sprintf("it applies to %v, not only %s", r.Resources, gvr.Resource))
			}
			if slices.ContainsFunc(r.Operations, func(op admv1.OperationType) bool { return op != admv1.Create && op != admv1.Update }) {
				out = append(out, "it applies to operations other than create and update")
			}
		}
	}
	// Behaviour, not just shape: run an object of the same kind in another
	// namespace through the policy, rules and match conditions included.
	decoy := &unstructured.Unstructured{Object: map[string]any{}}
	decoy.SetGroupVersionKind(schema.GroupVersionKind{Group: gvr.Group, Version: gvr.Version, Kind: scope.Kind})
	decoy.SetNamespace("kubexray-elsewhere")
	decoy.SetName("kubexray-decoy")
	if got, _ := evaluate(ctx, g, v, decoy, nil); got != notApplied {
		out = append(out, fmt.Sprintf("it applies to %ss in every namespace, not only %s", scope.Kind, scope.Namespace))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
