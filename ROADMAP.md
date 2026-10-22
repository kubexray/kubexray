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
- [x] Read-only; annotations and managed fields stripped before caching.
- [ ] **Change facts** in the model: container images, replica counts, resource requests and limits, and *when* a ConfigMap or Secret changed (never its contents).
- [ ] **Who made the change:** the tool behind each change (kubectl, Helm, Argo CD, Flux, a controller) and, where present, the Helm release revision or the GitOps commit. Read from a small allow-list of safe fields; everything else is still discarded.

### Recorder & rewind
- [ ] Recorder: a ring buffer of snapshots and deltas with a configurable window (default 1 hour), with keyframes for fast seeking.
- [ ] Timeline: scrub back to any moment and see the cluster exactly as it was. Markers for breakages, rollouts and config changes.

### Caused by
- [ ] Change log: every rollout, image change, scale and config edit as a readable event (*"api: image nginx:1.27 → nginx:1.72"*).
- [ ] Causality engine: when a workload breaks, find the most likely change and build the chain from symptom to cause (Ingress → Service with 0 ready pods → failing pods → the change). Covers the app's own changes (image, config, scale) and the ConfigMaps, Secrets and volumes it uses.
- [ ] Honest answers: causes are labelled **likely**, come with their evidence (*"api-config edited 2m before the first crash; no other changes to api"*), and list other recent changes as alternatives.
- [ ] Incident cards: *"api is down since 14:02 · likely caused by an edit to api-config by kubectl"*, with the chain highlighted on the map and a plain-English explanation.

### Terminal
- [ ] `kubexray why <kind>/<name>`: prints the incident chain, the likely cause, who made it, and the command to fix it.
- [ ] `kubexray changes --since 1h`: every change in the window, newest first, with who made it.

### Web UI
- [ ] Secure local server: listens on 127.0.0.1 only, one-time session token, strict Origin and Host checks ([threat model](docs/threat-model.md)).
- [ ] The approved design: namespaces as sections, apps as cards with pod dots, "Needs attention" strip, hover to trace a chain, explanations with copyable commands.
- [ ] Single binary: `kubexray` opens the UI in the browser; the UI is embedded with `embed`.

### Demo
- [ ] `make demo`: creates a kind cluster, deploys the sample shop app and opens kubexray.
- [ ] `make break`: the official demo story. Someone edits a ConfigMap; minutes later a pod restarts and crash-loops on the bad value. kubexray links the crash to the earlier edit and names who made it, a cause nobody would connect by hand.
- [ ] `make break SCENARIO=bad-image`: the simpler bad-rollout story, then a rollback.
- [ ] Performance baseline: smooth with 1,000 pods, with a load generator checked into the repo.

**Done when:** `make demo && make break` shows the outage; the incident card and `kubexray why` both name the ConfigMap edit and who made it as the likely cause; and rewinding shows the moment it broke.

---

## v0.2: Share

**Goal:** turn an incident into something anyone can watch.

- [ ] Export: save a time window as a replay file (gzip NDJSON, the same messages as the live stream).
- [ ] Scrubbing: the same allow-list as the live stream, plus optional anonymization of names, namespaces and labels.
- [ ] kubexray.com player: a static page that plays replay files. Drop a file in or open a shared link; no cluster access, no install.
- [ ] Incident permalinks: share a replay that opens on the incident, with the chain already highlighted.
- [ ] A recorded demo incident on the kubexray.com homepage.
- [ ] **Post-mortem draft:** one click exports Markdown with impact, timeline, likely cause, who made the change, the fix, duration and the replay link.
- [ ] **One cause, many symptoms:** when one change or failure breaks several apps (a node going NotReady, a shared ConfigMap edit), show one incident card listing every affected app, instead of one alert per app.

**Done when:** an incident exported from `make break` plays on kubexray.com with no cluster, its post-mortem draft is accurate, and a node failure in the demo cluster produces one grouped incident.

---

## v0.3: Deeper diagnosis

**Goal:** explain more kinds of breakage, more precisely.

- [ ] Explanations for OOMKilled (with limit vs. usage), failed probes, unschedulable pods (resources, taints, affinity), pending PVCs and evictions.
- [ ] More causes: node failures, ConfigMap or Secret edits followed by crashes, scale-downs, quota changes.
- [ ] Evidence links: every diagnosis shows the events, container states and log lines it is based on.
- [ ] Diff mode: pick two moments and see what changed between them.
- [ ] Break-it scenarios: `make break SCENARIO=oom|bad-config|bad-probe|unschedulable`, each with an expected diagnosis in CI.

**Done when:** every break-it scenario produces the right incident card and cause.

---

## v0.4: Always-on recording & release

**Goal:** record 24/7, so the incident from 3 a.m. is there in the morning.

- [ ] In-cluster mode via Helm: least-privilege read-only ClusterRole, recording persisted to a volume with size-based retention.
- [ ] Authentication for in-cluster access: bearer token or an auth proxy; no anonymous mode.
- [ ] **Alert → replay:** when Prometheus Alertmanager fires, kubexray posts the likely cause, who made the change and a replay link to Slack or a webhook.
- [ ] Release engineering: GoReleaser (multi-arch binaries and images, Homebrew tap), cosign signatures, SBOM and provenance.
- [ ] CI: envtest, an end-to-end run on kind, and Playwright UI screenshots.

**Done when:** `helm install` records continuously, survives restarts, and `brew install kubexray` works with signed releases.

---

## Later

- **Blast radius:** select a ConfigMap, Secret, Service or node and see everything that depends on it.
- **Preview a deploy:** `kubexray diff -f app.yaml` shows what a change will affect before you apply it.
- **Misconfiguration hints:** Services that select nothing, missing limits or probes, `:latest` tags.
- **Traffic:** Hubble/Cilium flows on the map, NetworkPolicy denies shown on the connection they block.
- **Scale & polish:** ⌘K search, multiple clusters, 5,000+ pods.
