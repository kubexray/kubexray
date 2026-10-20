# kubexray Roadmap

kubexray is a live, animated X-ray of a Kubernetes cluster. You can watch workloads come and go, rewind to see what happened, find out why something broke, and follow traffic between services.

This document describes where the project is headed. Each milestone stands on its own and ends with something usable you can demo.

---

## Principles

- **Alive, not static.** Every cluster change shows up on the map as motion, not just as a value refreshing in a table.
- **Read-only by default.** It is safe to point at any cluster. Actions that change the cluster need an explicit opt-in and respect RBAC.
- **Explain, don't just display.** When something is red, kubexray should say why in plain English.
- **Zero-friction demo.** `make demo` or the hosted replay at kubexray.com should show the full experience in under a minute, with no cluster required.
- **One binary.** The Go backend serves the embedded UI. Run it locally with your kubeconfig or install it in-cluster with Helm.

---

## Architecture at a glance

```
┌──────────────┐  watch   ┌─────────────────────────────┐  WebSocket   ┌──────────────────────┐
│  Kubernetes  │ ───────► │  kubexray (Go)              │ ───────────► │  UI (React +         │
│  API server  │          │  • shared informers         │   deltas     │  Cytoscape.js)       │
└──────────────┘          │  • graph model + diffing    │              │  • animated map      │
┌──────────────┐  flows   │  • event recorder (rewind)  │ ◄─────────── │  • timeline, panels  │
│ Hubble (opt) │ ───────► │  • diagnostics engine       │   commands   │  • command palette   │
└──────────────┘          └─────────────────────────────┘              └──────────────────────┘
```

---

## v0.1: Live Map

**Goal:** open kubexray and watch a cluster breathe. This milestone produces the README GIF.

### Backend
- [ ] Shared informer factory for Nodes, Namespaces, Pods, Deployments, ReplicaSets, StatefulSets, DaemonSets, Jobs, Services, EndpointSlices, Ingresses, ConfigMaps and Secrets (metadata only).
- [ ] In-memory graph model: objects become nodes, and ownerRefs, selectors and volume mounts become edges.
- [ ] WebSocket stream: a full snapshot on connect, then compact deltas (add / update / remove).
- [ ] **Versioned delta schema from day one.** The same format powers the live stream, the v0.2 rewind and the hosted replay demo.
- [ ] A single Go binary that serves the UI via `embed`. `kubexray` uses the current kubeconfig context.
- [ ] Real-cluster safe: read-only by default, Secrets watched as metadata only, no data leaves the machine.
- [ ] Performance baseline: smooth with 1,000 pods, with a load-generator script checked into the repo.

### Map
- [ ] Layout modes: **by node** (nodes as islands), **by namespace** (colored regions) and **by app** (grouped by `app.kubernetes.io/*` labels).
- [ ] Semantic zoom: cluster → nodes → namespaces → workloads → pods → containers. Each level hides detail that doesn't matter at that level.
- [ ] Services drawn as hubs with their backing pods around them.

### Lifecycle animations
- [ ] Pending pods fade in, and ready pods pulse once.
- [ ] Crashing pods flash red and shake, and OOMKilled pods "pop".
- [ ] CrashLoopBackOff shows as a looping spinner with a restart counter.
- [ ] Evicted or terminated pods drift away and fade out.
- [ ] Rollout playback: new ReplicaSet pods appear as old ones drain, with a progress ring on the Deployment.

### Chaos mode (opt-in, `--allow-actions`)
- [ ] Right-click a pod to delete it, or a node to cordon or drain it, then watch the cluster self-heal on the map.
- [ ] Actions are checked with `SelfSubjectAccessReview` and hidden when RBAC denies them.

### Demo
- [ ] `make demo` creates a kind cluster, deploys a sample microservice app and opens kubexray.
- [ ] Scripted story: deploy → scale up → crash a pod → rolling update → recover.

**Done when:** `make demo` runs end to end, and the map shows every lifecycle animation during the scripted story.

---

## v0.2: Rewind & Diagnose

**Goal:** answer "what happened?" and "why is this broken?". These are the headline features.

### Time travel
- [ ] Event recorder: a ring buffer of every graph delta, with a configurable retention window.
- [ ] Timeline slider: scrub back and see the cluster exactly as it was at any moment (for example 03:14, when the alert fired).
- [ ] Event markers on the timeline for crashes, rollouts, evictions and scaling.
- [ ] Diff mode: choose two moments and highlight what was added, removed or changed between them.

### Explain this pod
- [ ] Click a red pod and get a plain-English diagnosis built from status, events, probes and resource usage.
  - Example: *"OOMKilled 4× in 10 min. Memory limit 128Mi, last peak ~127Mi → raise the limit or investigate a leak."*
- [ ] Rule-based diagnostics engine covering ImagePullBackOff, CrashLoopBackOff, OOMKilled, failed probes, unschedulable pods (with the reason: resources, taints, affinity) and pending PVCs.
- [ ] Each diagnosis links to the evidence it is based on (events, container state, logs tail).

### Break-it scenarios
- [ ] `make break SCENARIO=oom|bad-selector|image-pull|bad-probe|unschedulable` applies a broken manifest, and kubexray diagnoses it.

### Hosted replay demo
- [ ] Export a time window as a replay file (the versioned delta format from v0.1).
- [ ] kubexray.com hosts a static UI that plays a recorded cluster story. Visitors need no cluster.
- [ ] Shareable incident replays: drop in a replay file to share a post-mortem.

**Done when:** every break-it scenario produces a correct diagnosis, rewinding shows the moment it broke, and kubexray.com plays a live demo.

---

## v0.3: Lint, Blast Radius & Ship It

**Goal:** catch problems before they hurt, and make kubexray easy to install and trust.

### Misconfiguration lint, shown on the map
- [ ] Services whose selector matches nothing, drawn as dangling edges.
- [ ] Missing resource requests and limits, and missing liveness or readiness probes.
- [ ] `:latest` image tags and privileged or root containers.
- [ ] Single-replica Deployments, and PDBs that block node drains.
- [ ] ConfigMaps and Secrets that are referenced but missing.

### Blast radius
- [ ] Select a node, ConfigMap, Secret or Service to highlight everything that would break if it disappeared.

### Health score
- [ ] A health score per namespace and workload, with a heat-map overlay on the map.

### Release engineering
- [ ] Helm chart with a least-privilege, read-only ClusterRole by default.
- [ ] GoReleaser: multi-arch binaries and container images, Homebrew tap.
- [ ] Images signed with cosign, plus an SBOM and provenance attestations.
- [ ] CI: unit tests, envtest against the API, an e2e run on a kind cluster, and Playwright UI screenshots.

**Done when:** `helm install` and `brew install` both work, and releases are signed.

---

## v0.4: Traffic

**Goal:** see who talks to whom, and what's being blocked.

- [ ] Hubble integration: animated packets along edges, edge thickness for requests per second, and red dots for drops.
- [ ] NetworkPolicy denies flash as a "wall" on the edge.
- [ ] NetworkPolicy visualizer: who *can* talk to whom versus who *actually* does.
- [ ] Unused allow rules are flagged as cleanup suggestions.
- [ ] Fallback mode without Cilium: an inferred service graph from EndpointSlices and metrics-server.

**Done when:** the demo app's traffic is visible live, and a deliberately broken NetworkPolicy shows up as a wall.

---

## v0.5: Scale & Polish

**Goal:** fast on big clusters and pleasant to use every day.

- [ ] ⌘K command palette: jump to any object, filter with label queries like `app=api,status!=Running`.
- [ ] Multiple clusters: a kubeconfig context switcher, and clusters side by side.
- [ ] Cost overlay: color pods by requested versus actual usage to spot waste.
- [ ] Export snapshots of the current view as PNG or SVG for docs and post-mortems.
- [ ] Performance: benchmarked at 5,000+ pods while keeping smooth rendering. Delta batching and WebGL rendering where needed.

**Done when:** a published benchmark shows smooth interaction at 5k pods.

---

## Ongoing

- Docs site on kubexray.com: quick start, architecture, diagnostics reference.
- A GIF or short clip in the README for every headline feature.
- Good-first-issue labels and a CONTRIBUTING guide once v0.1 ships.
