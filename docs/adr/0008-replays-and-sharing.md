# 0008. Replays, sharing and grouped incidents

- Status: Accepted
- Date: 2026-10-23

## Context

v0.2 turns an incident into something anyone can watch ([roadmap](../../ROADMAP.md)): export a time window, play it on kubexray.com without access to the cluster, share a link that opens on the incident, and draft a post-mortem from it. It also groups incidents that share a cause, so a broken node is one incident, not ten.

A replay leaves the user's machine, may be posted publicly, and is opened by people who never had access to the cluster. Two risks follow: **leaking** what the cluster contains, and **attacking** whoever opens a replay with a crafted file.

## Decision

### 1. File format

A replay is a gzip-compressed NDJSON file, extension `.kxr`, of ADR-0003 messages:

```text
{"v":1,"type":"header","from":…,"to":…,"cluster":"prod-eu","anonymized":false,"kubexray":"v0.2.0"}
{"v":1,"type":"snapshot",…}          the recorder's keyframe at or before `from`
{"v":1,"type":"incidents",…}         incidents at that moment
{"v":1,"type":"delta",…}             every change up to `to`
{"v":1,"type":"incidents",…}         whenever the incidents change
{"v":1,"type":"changes","changes":[…]}   who changed what in the window, last
```

- **Incidents are computed at export time** by running the cause engine (ADR-0006) over the replayed states, at most once per second of recorded time. The player is a static page without the Go engine; it shows the engine's own results, not a re-implementation.
- The header's `cluster` is the kubeconfig context name, or `"cluster"` when anonymized.

### 2. What a replay contains

Exactly the live stream's allow-list (ADR-0003, ADR-0005): no Secret or ConfigMap contents, no environment values, no annotations other than the allow-listed ones. Exporting adds nothing.

### 3. Anonymization (optional, on by default in the UI)

Replaces every identifying string consistently within one file:

| Field | Becomes |
|---|---|
| Namespace names | `namespace-1`, `namespace-2`, … |
| Object names | `<kind>-<n>`, e.g. `deployment-3`, `pod-12` (node IDs are rebuilt from these) |
| Label keys and values | `label-1=value-4` |
| Container images | `image-2:<tag>` (the tag is kept: "the tag changed" is the story) |
| Helm release / Argo CD / Flux names | `release-1` |
| Cluster name | `cluster` |

Kinds, statuses, reasons, counts, times, tool names (`kubectl`, `helm`, `argocd`) and field managers that name a tool are kept: they carry the story and identify nothing. Other field managers become `manager-<n>`. Incidents are computed **after** anonymization, so their summaries only contain anonymized names.

### 4. Opening a replay safely

Replays are untrusted. Readers (the Go reader used by `kubexray play` and tests, and the player):

- reject files over 50 MiB uncompressed (stop decompressing at the limit) and lines over 16 MiB;
- require `v: 1` and the header first;
- validate every operation before applying it (ADR-0003: a bad message is rejected whole);
- render every string as text (React; no HTML).

The player additionally loads remote replays only over `https:`, and its page carries a Content-Security-Policy (`<meta>`, since GitHub Pages can't set headers): scripts and styles from itself only, `connect-src https:` for remote replays.

### 5. Where replays are played

- **`kubexray play incident.kxr`**: serves the same UI locally in replay mode, with the same server protections as live mode (ADR-0007).
- **kubexray.com**: a static build of the same UI in player mode, published with GitHub Pages: drop a file, or open `?replay=<https-url>`. Permalinks add `&t=<time>` and `&app=<id>`. The homepage plays a recorded demo incident.

### 6. Grouped incidents

The cause engine groups incidents whose top cause is the **same change** (same kind, object and time) at confidence *likely* or *very likely*: a NotReady node, or a ConfigMap shared by several apps. A group is shown as one incident, *"node worker-2 is not ready: 6 apps affected"*, listing the apps. Possible causes never group: a coincidence must not merge unrelated incidents.

### 7. Post-mortem draft

Generated in the browser from the incident (or group), the engine's cause and evidence, and the events in the window: impact, timeline, likely cause, who made the change, the fix (the first change after which the app recovered), duration, and the replay permalink when there is one. It's Markdown to copy or download, explicitly a draft for a person to finish.

## Alternatives considered

| Option | Why not |
|---|---|
| Recompute incidents in the player (port the engine to TypeScript, or WebAssembly) | Two engines to keep identical, or a large WASM download; exporting the engine's results is simpler and exactly consistent |
| Anonymize by hashing names | Hashes of short names can be brute-forced; per-file counters reveal nothing |
| Hosting uploaded replays on kubexray.com | Turns kubexray into a data host with retention and abuse problems; users share files or links to where they put them |
| A replay as one JSON document | Can't be streamed or validated message by message, and needs the whole file in memory |

## Consequences

- An exported replay is self-contained: the player needs nothing but the file.
- Anonymized replays lose names but keep the story, including that an image tag changed.
- The player is a second build of the same UI, so both stay in step automatically.
- Publishing kubexray.com needs a DNS record at the domain registrar; until then the player is at the GitHub Pages address.
