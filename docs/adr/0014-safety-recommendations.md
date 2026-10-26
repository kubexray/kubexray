# 0014. Safety recommendations: every incident writes the guardrail that would have stopped it

- Status: Accepted
- Date: 2026-10-26

## Context

After an air crash, investigators read the flight recorder and issue a safety recommendation, so the same failure can't happen again. kubexray already does the first two parts. It records every change (ADR-0003, ADR-0005), and it explains what broke and the change that likely caused it (ADR-0006, ADR-0009). The lesson then stays in a post-mortem. Nothing stops the same ConfigMap value, image tag or memory limit from breaking the same app next month.

Kubernetes can enforce such a lesson natively. A ValidatingAdmissionPolicy (GA since 1.30) rejects objects that fail a CEL expression, with no extra software in the cluster. Writing one from an incident is skilled work: it means turning "the crash log says `invalid LOG_LEVEL: verbose`" into a precise, safe rule. A language model can draft that rule. But a drafted rule can be wrong in two ways that matter. It can miss the bad change, which makes it useless. Or it can block changes that are fine, which turns it into an outage of its own.

AI tools for Kubernetes already explain errors, investigate incidents and draft fixes. None we found takes an incident through to a guardrail and proves that guardrail against the incident itself. kubexray can, because it has the recording.

## Decision

### 1. What it is

When an incident has recovered and its likely cause is one an admission rule can express, kubexray offers **Draft a safety recommendation**. The result is a ValidatingAdmissionPolicy and a ValidatingAdmissionPolicyBinding, with a plain-English rationale. It's shown only if it passes the proof in section 3.

For the demo incident:

> **Safety recommendation:** `LOG_LEVEL` in ConfigMap shop/checkout-config must be one of debug, info, warn or error.
> *Why:* on Oct 24 it was set to `verbose`, and checkout crash-looped (`invalid LOG_LEVEL: verbose`) until it was set back.
> *Proven:* rejects the change that broke checkout, accepts the fix, and accepts everything running in the cluster now.

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: kubexray-shop-checkout-config-log-level}
spec:
  matchConstraints:
    resourceRules: [{apiGroups: [""], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [configmaps]}]
  matchConditions:
    - {name: this-configmap, expression: "object.metadata.namespace == 'shop' && object.metadata.name == 'checkout-config'"}
  validations:
    - expression: "!has(object.data.LOG_LEVEL) || object.data.LOG_LEVEL in ['debug', 'info', 'warn', 'error']"
      messageExpression: "'LOG_LEVEL must be debug, info, warn or error, not ' + object.data.LOG_LEVEL"
```

### 2. What the model does

It drafts, and nothing else. It gets the incident (symptom, diagnosis, the likely cause and its evidence), the before and after of the change as far as kubexray knows them, and the crashed container's last log lines. It answers in a fixed schema enforced by the provider's structured output. The answer holds the policy's match rules and CEL validations, a name, a message, the rationale, and how to rebuild the bad object if kubexray doesn't have it (section 4). If no admission rule can express the cause, it says so. The model never applies anything, and its draft is never shown until it passes the proof.

### 3. The proof: plain code, against the recording

kubexray evaluates the draft locally with the API server's own admission and CEL libraries (`k8s.io/apiserver`), pinned to the cluster's minor version. It writes nothing to the cluster. A draft must pass every check:

1. **It compiles,** within the API server's CEL cost limits.
2. **It's narrow.** It matches the incident's resource kind, in the incident's namespace, scoped by name or label. Cluster-wide rules on core kinds are refused.
3. **It rejects the change that broke the app**: the bad object, rebuilt from the recording (section 4).
4. **It accepts the fix**: the object as it is now, after recovery.
5. **It accepts everything running now.** Every object the policy matches is checked in its current form, so the rule can't block the next ordinary deploy.
6. **It accepts every healthy past version kubexray recorded** for the objects it matches, and rejects the recorded versions that preceded an incident it is about.

If a draft fails, the failures go back to the model, at most twice. Then kubexray either shows a proven recommendation or says it couldn't produce one. Each check appears with the recommendation, so a reader sees what was proven and not just that it passed.

Two things follow from this design. Logs are untrusted input, so a crash log could try to steer the model. But a rule that lets everything through fails check 3, and one that blocks too much fails check 5. What the model writes can't hurt the cluster without failing the proof.

### 4. Rebuilding the bad object

- **Workload changes** (images, resources, probes, replicas): kubexray records these fields (ADR-0005). The bad object is the current object with the recorded values put back.
- **ConfigMap changes:** kubexray never records contents. The bad object is the current ConfigMap with the value the model infers from the crash log (`LOG_LEVEL=verbose` from `invalid LOG_LEVEL: verbose`). That value is labelled *reconstructed from the logs*, and check 3 proves the rule rejects it. kubexray can't prove that was the exact value. The recommendation says so.
- **Secrets:** out of scope. kubexray doesn't read their contents, and won't start for this.

### 5. Consent, and what is sent

Recommendations are drafted only when someone clicks **Draft a safety recommendation**, never automatically. Before anything is sent, the dialog lists exactly what will be sent:

- the incident facts
- the change
- the current object or objects the rule will match, which for a ConfigMap means its contents
- the last 50 lines of the crashed container's previous run, with credential-looking values redacted

Reading the ConfigMap and the logs uses the permissions of whoever runs kubexray: their kubeconfig on a laptop, or `configmaps` contents and `pods/log`, granted in the chart only when `recommendations.enabled` is on. Nothing is sent for other objects, and Secrets are never sent.

### 6. Delivery: kubexray never applies it

The recommendation comes as YAML to copy or download. Its binding defaults to `validationActions: [Warn, Audit]`, and kubexray says so: the rule warns and is logged before it blocks anything. Switching to `[Deny]` is the operator's step, with the one-line change shown. Recommendations also appear in the post-mortem draft and in replays, so the team reviewing the incident sees the proposed fix and its proof together. Opening a pull request against a GitOps repository is left for later.

### 7. Providers and cost

The provider layer is the same one planned for every AI feature:
- **Anthropic** through the official Go SDK, with model `claude-opus-5-5` by default, configurable.
- **OpenAI-compatible endpoints** for local models (Ollama, vLLM), so teams can keep everything in their network.

Keys come from files or the environment and are never logged. Each draft costs one to three model calls, triggered by a person. Every draft writes an audit line (`"audit": true`, event `recommendation`): who asked, which incident, the provider, the model, the result of each check, and token counts.

### 8. What's covered first

| Cause (ADR-0006) | Example guardrail |
|---|---|
| ConfigMap edit | allowed values or format of a key |
| Image change | the image must come from the registry and repository the app uses, by tag pattern or digest |
| Resources | memory limit for this app at least the size it was running at before it ran out of memory |
| Unschedulable | the app's requests no larger than the biggest node can give |
| Probe | readiness probe path must be one the app serves (from the version that worked) |
| Scale to zero | replicas at least 1 for an app a Service still routes to |
| Quota | a new or lower ResourceQuota in the namespace must leave room for its apps' requests |

Node failures and causes outside Kubernetes get no recommendation, and kubexray says why.

### 9. How it's tested

- **The proof** has unit tests with fixtures for each row above: good drafts, drafts that are too broad, drafts that miss the bad change, drafts that block the fix, and drafts that don't compile.
- **The model** is stubbed in tests, including a crash log that tries to give orders.
- **End to end in CI:** on the demo cluster, a stub model returns a canned draft, kubexray proves it, the test applies it with `[Deny]`, and `make break` is then refused by the API server. This shows the guardrail stops the real incident.
- **The public demo** is re-recorded with a real model, so the site shows a real recommendation and its proof.

## Consequences

- Incidents end with a tested change that prevents a repeat, not just an explanation. That's what aviation's safety recommendations do, and nothing we found does it for Kubernetes.
- The AI is held to a test written in plain code, against what actually happened. That is a stronger answer to "can I trust it?" than labels or confidence scores.
- kubexray reads more when asked to: ConfigMap contents and pod logs, at a person's request and with their consent. Its default stays read-only and contents-blind.
- Admission policies can block deploys. Defaulting to Warn and Audit, refusing broad rules, and checking against everything running now make that unlikely. The operator still owns the switch to Deny.
- It depends on the API server's admission libraries matching the cluster's version. kubexray pins them, and refuses to prove a policy for a cluster minor version it doesn't support.
