# kubexray Roadmap

**kubexray is the flight recorder for your Kubernetes cluster.** It records every change, lets you rewind to any moment, traces a breakage back to the change that caused it, and turns an incident into a replay anyone can watch in a browser.

> See what changed. See what broke. Share what happened.

This document describes where the project is headed. Each milestone stands on its own and ends with something you can demo.

---

## Why kubexray

Dashboards show you the cluster *now*: a pod is crashing, a Deployment is 0/3. During an incident you need different answers:

1. **What changed?** Most outages start with a change: a new image, an edited ConfigMap, a scale-down.
2. **What did it break?** The trail from the user-facing symptom (Ingress errors) down to the cause.
3. **Can I show my team?** Screenshots and log excerpts don't capture how an incident unfolded.

kubexray answers all three, free and open source, from a single binary on your laptop. Nothing needs to be installed in the cluster to get started.

---

## Principles

- **Explain, don't just display.** When something is red, say why in plain English and what to do next.
- **Every change is a moment you can return to.** The recording is the product; the live view is just its latest frame.
- **Read-only and private by default.** Safe to point at any cluster. Secret and ConfigMap contents are never read. Shared replays are scrubbed.
- **Clean and organized.** A structured layout (way in → load balancer → app → config & storage) instead of a hairball graph.
- **One binary.** The Go backend serves the embedded UI. Run it locally, or in-cluster with Helm for 24/7 recording.

---

## Architecture at a glance

```
┌──────────────┐  watch   ┌───────────────────────────────┐  WebSocket   ┌────────────────────────┐
│  Kubernetes  │ ───────► │  kubexray (Go)                │ ───────────► │  UI (React)            │
│  API server  │          │  • shared informers           │   deltas     │  • cluster map         │
└──────────────┘          │  • graph model + change facts │              │  • incident cards      │
                          │  • recorder (ring buffer)     │ ◄─────────── │  • timeline / rewind   │
                          │  • causality engine           │   seek,      │  • explanations        │
                          │  • replay export (scrubbed)   │   export     │                        │
                          └───────────────────────────────┘              └────────────────────────┘
                                          │ replay file
                                          ▼
                                  kubexray.com player (static, no cluster access)
```

---

## v0.1: Record & Rewind

**Goal:** run `kubexray`, break something, and watch it show you what changed and what broke. This milestone produces the README GIF.

### Cluster model
- [x] Shared informers for Namespaces, Nodes, Pods, Deployments, ReplicaSets, StatefulSets, DaemonSets, Jobs, Services, Ingresses, PVCs, ConfigMaps and Secrets (metadata only).
- [x] Graph model: objects become nodes; owner references, selectors, volume and env references, and ingress routes become edges.
- [x] Versioned delta schema ([ADR-0003](docs/adr/0003-graph-delta-schema.md)) shared by the live stream, rewind and replay files.
- [x] Read-only; objects cut down before caching: annotations reduced to a small allow-list, managed fields to who wrote what and when ([ADR-0005](docs/adr/0005-change-facts-and-attribution.md)).
- [x] **Change facts** in the model: container images, replica counts, resource requests and limits, and *when* a ConfigMap or Secret changed (never its contents).
- [x] **Who made the change:** the tool behind each change (kubectl, Helm, Argo CD, Flux, a controller) and the Helm release, Argo CD app or Flux source an object belongs to.
- [ ] **Which commit:** the GitOps revision behind a change, from Argo CD `Application` and Flux `Kustomization` status (follow-up ADR).

### Recorder & rewind
- [x] Recorder: a ring buffer of snapshots and deltas with a configurable window (default 1 hour), with keyframes for fast seeking.
- [x] Timeline: scrub back to any moment and see the cluster exactly as it was. Markers for breakages, rollouts and config changes.

### Caused by
- [x] Change log: every rollout, image change, scale and config edit as a readable event (*"api: image nginx:1.27 → nginx:1.72"*).
- [x] Causality engine: when a workload breaks, find the most likely change and build the chain from symptom to cause (Ingress → Service with 0 ready pods → failing pods → the change). Covers the app's own changes (image, config, scale) and the ConfigMaps, Secrets and volumes it uses.
- [x] Honest answers: causes are labelled **likely**, come with their evidence (*"api-config edited 2m before the first crash; no other changes to api"*), and list other recent changes as alternatives.
- [x] Incident cards: *"api is down since 14:02 · likely caused by an edit to api-config by kubectl"*, with the chain highlighted on the map and a plain-English explanation.

### Terminal
- [x] `kubexray why <kind>/<name>`: prints the incident chain, the likely cause, who made it, and the command to fix it.
- [x] `kubexray changes --since 1h`: every change in the window, newest first, with who made it.

### Web UI
- [x] Secure local server: listens on 127.0.0.1 only, one-time session token, strict Origin and Host checks ([threat model](docs/threat-model.md)).
- [x] The approved design: namespaces as sections, apps as cards with pod dots, "Needs attention" strip, hover to trace a chain, explanations with copyable commands.
- [x] Single binary: `kubexray` opens the UI in the browser; the UI is embedded with `embed`.

### Demo
- [x] `make demo`: creates a kind cluster, deploys the sample shop app and opens kubexray.
- [x] `make break`: the official demo story. Someone edits a ConfigMap; minutes later a pod restarts and crash-loops on the bad value. kubexray links the crash to the earlier edit and names who made it, a cause nobody would connect by hand.
- [x] `make break SCENARIO=bad-image`: the simpler bad-rollout story, then a rollback.
- [x] Performance baseline: smooth with 1,000 pods, with a load generator checked into the repo (`make perf`, `make bench`).

**Done when:** `make demo && make break` shows the outage; the incident card and `kubexray why` both name the ConfigMap edit and who made it as the likely cause; and rewinding shows the moment it broke.

---

## v0.2: Share

**Goal:** turn an incident into something anyone can watch.

- [x] Export: save a time window as a replay file (gzip NDJSON, the same messages as the live stream).
- [x] Scrubbing: the same allow-list as the live stream, plus optional anonymization of names, namespaces and labels.
- [x] Player: a static page that plays replay files. Drop a file in or open a shared link; no cluster access, no install. Live at [kubexray.com/play](https://kubexray.com/play/), behind the product site at [kubexray.com](https://kubexray.com).
- [x] Incident permalinks: share a replay that opens on the incident, with the chain already highlighted.
- [x] A recorded demo incident on the player's homepage.
- [x] **Post-mortem draft:** one click exports Markdown with impact, timeline, likely cause, who made the change, the fix, duration and the replay link.
- [x] **One cause, many symptoms:** when one change or failure breaks several apps (a node going NotReady, a shared ConfigMap edit), show one incident card listing every affected app, instead of one alert per app.

**Done when:** an incident exported from `make break` plays on kubexray.com with no cluster, its post-mortem draft is accurate, and a node failure in the demo cluster produces one grouped incident.

---

## v0.3: Deeper diagnosis

**Goal:** explain more kinds of breakage, more precisely.

- [x] Explanations for OOMKilled (with the limit; usage needs metrics, so kubexray suggests `kubectl top`), failed probes, unschedulable pods (resources, taints, affinity), pending PVCs, evictions, and pods refused by quotas or admission ([ADR-0009](docs/adr/0009-diagnosis.md)).
- [x] More causes: node failures, ConfigMap or Secret edits followed by crashes, apps scaled to zero while an Ingress routes to them, quota changes, probes added, requests raised.
- [x] Evidence: every diagnosis lists what it's based on: the scheduler's, kubelet's or controller's own message, exit codes, limits and requests. Logs and termination messages stay out by design (ADR-0009); kubexray suggests the `kubectl logs` command instead.
- [x] Diff mode: pick two moments and see what changed between them.
- [x] Break-it scenarios: `make break SCENARIO=oom|bad-config|bad-probe|unschedulable|scale-zero|quota` and the v0.1 ones, each with an expected diagnosis checked on kind in CI (`make e2e`).

**Done when:** every break-it scenario produces the right incident card and cause.

---

## v0.4: Always-on recording & release

**Goal:** record 24/7, so the incident from 3 a.m. is there in the morning.

- [x] In-cluster mode via Helm: least-privilege read-only ClusterRole, recording persisted to a volume with size-based retention.
- [x] Authentication for in-cluster access: bearer token or an auth proxy; no anonymous mode.
- [x] **Alert → replay:** when Prometheus Alertmanager fires, kubexray posts the likely cause, who made the change and a replay link to Slack or a webhook.
- [ ] Release engineering: GoReleaser (multi-arch binaries and images, Homebrew tap), cosign signatures, SBOM and provenance.
- [x] CI: envtest, an end-to-end run on kind, and Playwright UI screenshots.

**Done when:** `helm install` records continuously, survives restarts, and `brew install kubexray` works with signed releases.

---

## v0.5: The change feed

**Goal:** see every change, and what it did, without waiting for something to break ([ADR-0012](docs/adr/0012-change-feed.md)).

- [x] Change feed engine: entries with before and after (images, resources, probes, replicas, edits, nodes), grouped into change sets (one Helm upgrade or sync is one line), from recorded history.
- [x] Verdicts on every change: healthy (and how long it took), broke an app, fixed one, stuck, settling.
- [x] Drift: hand edits to objects Helm, Argo CD or Flux manage.
- [x] Changes view: "since you last looked", today at a glance, an hourly strip, filters, search, and a detail drawer that rewinds the map to just before a change. Incidents view: what's broken now and the changes that broke something.
- [ ] Daily digest to Slack or a webhook: what changed yesterday, what broke, what drifted.
- [x] Sign-in for kubexray on the internet: built-in OIDC (Google, Okta, Entra ID, Keycloak, Dex), an allow-list of emails, domains and groups, per-person sessions, an audit trail, all from Helm values.
- [ ] GitHub sign-in (organizations and teams), which isn't an OIDC provider.

**Done when:** a day of changes on the demo cluster reads correctly in the feed, and the digest posts it.

---

## v0.6: Safety recommendations

After an incident, kubexray drafts the admission policy that would have stopped it, and proves it against the recording before showing it ([ADR-0014](docs/adr/0014-safety-recommendations.md)).

- [ ] The proof, in plain code: compile with the API server's CEL libraries, refuse broad rules, reject the change that broke the app, accept the fix, everything running now and every healthy recorded version.
- [ ] Rebuilding the bad object: recorded workload fields; ConfigMap values reconstructed from the crash log, labelled as such.
- [ ] Providers: Anthropic (official Go SDK) and OpenAI-compatible endpoints (Ollama, vLLM); keys from files; the draft schema; up to two retries with the failures.
- [ ] Draft a safety recommendation: a consent dialog listing what will be sent, the policy and binding (Warn and Audit by default), the rationale and each check; in post-mortems and replays.
- [ ] Helm values (`recommendations.*`, with ConfigMap contents and `pods/log` only when on), `kubexray recommend`, audit lines.
- [ ] Tests: fixtures for each cause; a crash log that gives orders; e2e where the proven policy, applied with Deny, makes the API server refuse `make break`.

**Done when:** the demo incident ends with a proven recommendation, and applying it makes the API server refuse the same break.

---

## v0.7: See what changed, exactly

Diffs on every change, like Argo CD shows them, with values kept inside the cluster (ADR-0015, ADR-0017, ADR-0018).

- [x] See diff: names, sources and fingerprints for pod templates; ConfigMap and Secret edits say what isn't read.
- [x] Values: env values, commands and ConfigMap contents in the UI; Secrets never read; credential-looking names and values masked; values stripped from replays, alerts and post-mortems unless included; `--values` and `diffs.values`.
- [x] Every resource kind: a cleaned manifest history for Ingress, HPA, PodDisruptionBudget, NetworkPolicy, RBAC, CronJob, StorageClass, LimitRange and custom resources the chart is given; diffs in lines or YAML.
- [x] Incident cards name the exact value that changed ("LOG_LEVEL info → verbose"); alerts, post-mortems and the AI model still get no values.
- [x] Drift against what Helm, Argo CD or Flux last applied, with the command to put it back (ADR-0019).
- [x] Values follow Kubernetes permissions for signed-in people (SubjectAccessReview, ADR-0020).

**Done when:** the demo's incident card reads "LOG_LEVEL info → verbose", and a hand edit shows its diff against what Helm applied.

---

## v0.8: Incidents from signals

kubexray notices what users feel, not only what crashes (ADR-0016).

- [x] Restarts and memory close to its limit, from Kubernetes and metrics-server, with no setup.
- [x] Prometheus found automatically; adapters for Traefik, ingress-nginx, Istio and OpenTelemetry; routing changes (Services, Ingresses, NetworkPolicies) as causes.
- [ ] EKS Application Load Balancers through CloudWatch, mapped with TargetGroupBindings.
- [x] Baselines, breaks and sensitivity; signal incidents through the cause engine.
- [ ] Charts of an app's errors and latency with changes marked.
- [x] Alertmanager alerts as incidents; a sources page.
- [ ] The demo cluster gets Traefik and Prometheus; the public demo shows a "failing requests" incident.

**Done when:** a deploy that makes checkout fail requests, with every pod green, is an incident naming the deploy.

---

## Later

- **Blast radius:** select a ConfigMap, Secret, Service or node and see everything that depends on it.
- **Preview a deploy:** `kubexray diff -f app.yaml` shows what a change will affect before you apply it.
- **Misconfiguration hints:** Services that select nothing, missing limits or probes, `:latest` tags.
- **Traffic:** Hubble/Cilium flows on the map, NetworkPolicy denies shown on the connection they block.
- **Scale & polish:** ⌘K search, multiple clusters, 5,000+ pods.
