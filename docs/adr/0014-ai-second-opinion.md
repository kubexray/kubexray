# 0014. A second opinion from the logs, by a language model

- Status: Proposed
- Date: 2026-10-26

## Context

The cause engine (ADR-0006) finds the likely cause of an incident from facts Kubernetes records: what the app uses, what changed and when (`managedFields`), and which pods started before or after the change. It is predictable, explainable and tested, and it says "likely" because timing is evidence, not proof.

What it can't read is why a container crashed. That is in the container's logs, free text in whatever format the app uses: `invalid LOG_LEVEL: verbose`, a Java stack trace, `connection refused to db:5432`. People read those lines to confirm or rule out a cause. Rules can't, because no fixed pattern covers every app. A language model can.

A model asked to "confirm" a cause from the same facts the rules used adds nothing, and tends to agree. It only adds evidence when it reads something the rules can't. Models also make things up, follow instructions hidden in their input, and cost money. Sending logs to a provider can also break the promise that kubexray keeps cluster data private ([threat model](../threat-model.md)).

## Decision

### 1. What the model does: one narrow question about the logs

When an incident has a likely cause and a crashed container, kubexray asks a model one question: **do these logs support the likely cause, contradict it, or can't you tell?** The model never decides the cause, never names a cause on its own, and never acts on the cluster. Its answer is an addition to the incident, labelled as AI.

The answer has a fixed shape, enforced by the provider's structured output:

```json
{
  "verdict": "supports" | "contradicts" | "unclear",
  "reason": "one or two sentences",
  "quotes": ["log lines, copied exactly"],
  "check": "for contradicts: what to look at instead"
}
```

### 2. Every claim is checked against the logs

Before an answer is shown, kubexray checks in plain code that every quote appears in the log text it sent (after trimming whitespace). A `supports` or `contradicts` answer needs at least one valid quote. An answer with no valid quote, or with any quote that isn't in the logs, is dropped and logged as `ai_rejected`. The incident then shows only the rules' answer, as it does today.

The reason is capped at 300 characters and shown as text. It is never rendered as HTML or Markdown, and in Slack its formatting characters are escaped.

### 3. Logs are untrusted input

Application output can be shaped by anyone who can make the app log something. So the model's instructions are in the system prompt and the logs go in the user message, inside a delimited block the model is told is data. The answer must fit the schema, and its effect is limited by design: a manipulated answer can at most add a labelled note, never change or hide the rules' cause. The quote check stops invented evidence. A test fixture with log lines that try to give orders checks that injected instructions don't change the output beyond what the schema allows.

### 4. What is sent, and what isn't

- **Sent:** the app's kind, namespace and name; the likely cause's summary and evidence (already shown in the UI); the symptom; and the last 50 lines (at most 8 KiB) of the crashed container's previous run, the same as `kubectl logs --previous --tail=50`.
- **Redacted from the logs before sending:** values that look like credentials (bearer tokens, JWTs, `password=`/`token=`/`secret=` values, AWS keys, private key blocks, URLs with credentials). Quotes are checked against the redacted text.
- **Never sent:** Secret or ConfigMap contents (kubexray never reads them), environment variables, or other pods' logs.

### 5. Off by default; the operator picks the model

The feature is off until it's turned on. Turning it on needs a provider and, in a cluster, permission to read pod logs (`pods/log`), which the chart grants only then.

- **Anthropic (the default provider):** the official Go SDK, model `claude-opus-5-5` by default at `low` effort (a classification task), configurable with `--ai-model`. Refusals (`stop_reason: refusal`) are handled as "unclear", with the API's server-side fallback switched on.
- **OpenAI-compatible endpoints:** for local or self-hosted models (Ollama, vLLM, LM Studio) and other providers, with a base URL. Teams that can't send logs out run the model inside their network.

Keys come from files (mounted Secrets) or the environment, never flags, and are never logged.

### 6. When it runs, and what it costs

- At most once per incident and likely cause (keyed by app, incident start and cause), cached, and asked again only if the cause changes.
- Only for crashes with logs to read: CrashLoopBackOff, Error and OOMKilled, with a previous container.
- Two requests at a time at most, 20 seconds each, and a budget per hour (default 30). Past the budget, kubexray skips and says so in its log.
- It never blocks anything. The incident appears at once, and the note follows when the model answers. If the provider is slow, down or refuses, nothing else changes.
- Each request writes an audit line (`"audit": true`, event `ai_request`) with the app, provider, model, verdict and token counts, without the log text.

### 7. Where it appears

The note appears in four places:

- the incident card and side panel, under the evidence, as *From the logs (AI)* with the verdict, the reason and the quoted lines
- `kubexray why`, with `--ai`
- alerts, as one line and the quote
- post-mortem drafts

In replays, notes are kept as their own message type so the player shows them. Anonymized replays leave them out, because log lines can carry names the anonymizer doesn't know.

### 8. How it's tested

- The quote check, redaction and the budget have unit tests.
- A fake provider covers each verdict, invented quotes, refusals, timeouts and the prompt-injection fixture.
- CI runs end to end on the demo cluster against a stub OpenAI-compatible server that returns canned answers, so CI needs no key or model. A manual test runs against a real model.
- The public demo replay is re-recorded with the feature on, so the site shows a real answer quoting `invalid LOG_LEVEL: verbose`.

## Consequences

- An incident can now say why a container crashed, in the app's own words, and warn when the logs don't fit the likely cause. That's the case where acting on the rules alone wastes the most time.
- kubexray gains a network dependency and a running cost for teams that turn the feature on, so it stays optional, rate-limited and outside the critical path.
- Reading logs widens what kubexray sees. It's opt-in, it needs its own permission, and teams can keep it inside their network with a local model.
- Model output can be wrong. The design limits the damage: it's labelled, checked against the logs, kept apart from the rules' answer, and never acts.
