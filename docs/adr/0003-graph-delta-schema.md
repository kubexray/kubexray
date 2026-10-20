# 0003. Graph delta schema

- Status: Accepted
- Date: 2026-10-20

## Context

The backend turns Kubernetes objects into a graph and streams it to the UI. The same data feeds three features:

1. **Live view**: snapshot on connect, then a stream of small changes.
2. **Rewind** (v0.2): the backend records changes so the UI can scrub back in time.
3. **Replay files** (v0.2): a recorded time window, played by the hosted demo at kubexray.com.

If these used different formats, they would drift apart. The format also defines exactly what cluster data leaves the backend, so it is a core security control (see the [threat model](../threat-model.md)).

## Decision

### 1. Graph model, not raw Kubernetes objects

The backend never forwards raw Kubernetes objects. It maps each object to a small, **explicitly modelled** node. Any field not in the model can't leak, because it never enters the stream. Sensitive data is left out by an allow-list, not removed by a block-list.

**Node**

| Field | Type | Notes |
|---|---|---|
| `id` | string | Kubernetes `metadata.uid` (unique even if a name is reused) |
| `kind` | string | `Pod`, `Deployment`, `Service`, `Node`, … |
| `ns` | string | Namespace; empty for cluster-scoped objects |
| `name` | string | |
| `parent` | string? | `id` of the node it is visually grouped under (pod → Node, workload → Namespace) |
| `labels` | map | Labels only. Annotations are excluded, because they can hold secrets (`last-applied-configuration`) |
| `status` | object | Kind-specific summary, e.g. `{phase, ready, reason, restarts}` for pods |
| `created` | timestamp | |

**Edge**

| Field | Type | Notes |
|---|---|---|
| `id` | string | Deterministic: `<type>:<from>:<to>` |
| `type` | enum | `owns` (ownerRef), `selects` (Service → Pod), `mounts` (Pod → ConfigMap/Secret/PVC), `routes` (Ingress → Service) |
| `from`, `to` | string | Node ids |

Pod scheduling (pod → Node) is expressed through `parent`, not as an edge, because it drives the layout rather than being a relationship to draw.

### 2. Messages

Every message is JSON with a common envelope:

```json
{ "v": 1, "type": "delta", "seq": 1042, "ts": "2026-10-20T19:57:11.123Z", "ops": [ ... ] }
```

| `type` | Meaning |
|---|---|
| `snapshot` | The full graph. Sent on connect, and as a keyframe in replay files. Large snapshots are split into chunks that share a `seq`. |
| `delta` | A batch of operations |
| `heartbeat` | Keep-alive that carries the current `seq` |
| `error` | Non-fatal error shown in the UI (already sanitised) |

Delta operations replace whole items. There is no field-level patching:

```json
{ "op": "upsertNode", "node": { ... } }
{ "op": "removeNode", "id": "<node id>" }
{ "op": "upsertEdge", "edge": { ... } }
{ "op": "removeEdge", "id": "<edge id>" }
```

Within one delta, operations are ordered so the client never sees an edge without both endpoints: node upserts, then edge upserts, then edge removes, then node removes. An edge whose endpoint does not exist yet (for example, a Service seen before its Pods) is held back and sent once both ends exist. Removing a node also removes its edges.

Whole-item replacement keeps the client simple and idempotent; applying the same op twice is harmless. Nodes are small, so the bandwidth cost is low.

**Batching:** changes are collected for up to 100 ms and sent as one delta. Multiple changes to the same item within a batch collapse to the last one.

**Ordering and recovery:** `seq` increases by one per message. If the client sees a gap, for example after a reconnect, it discards its state and requests a new snapshot. There is no complex resume protocol.

### 3. Replay files use the same messages

A replay file is gzip-compressed NDJSON, one message per line:

```
{"v":1,"type":"header","cluster":"demo","from":"…","to":"…","anonymised":false}
{"v":1,"type":"snapshot", ...}        ← keyframe
{"v":1,"type":"delta", ...}
...
{"v":1,"type":"snapshot", ...}        ← keyframe every 30 s, for fast seeking
```

The live stream, the rewind buffer and replay files are therefore one format. Rewind works by jumping to the closest keyframe and applying deltas from there.

### 4. Versioning

- `v` is an integer. Adding optional fields does **not** change it, and clients ignore unknown fields.
- Removing or changing the meaning of a field increments `v` and requires a new ADR.
- The replay player supports the current and previous `v`.

### 5. Source of truth for types

Go structs in `internal/graph` are the source of truth. TypeScript types for the UI are generated from them with [tygo](https://github.com/gzuidhof/tygo). CI fails if the generated file is out of date, so the backend and UI can't drift apart.

### 6. Limits

- Maximum WebSocket message size: 1 MiB (larger snapshots are chunked).
- Replay files are rejected above a configured size (default 50 MiB uncompressed) and validated message by message.
- All strings are treated as untrusted by the UI and rendered as text only.

## Alternatives considered

| Option | Why not (for now) |
|---|---|
| Forward raw Kubernetes objects | Leaks sensitive fields by default, large payloads, couples the UI to Kubernetes API versions |
| JSON Patch (RFC 6902) deltas | Smaller messages, but ordering-sensitive and harder to make idempotent; not needed at this node size |
| Protobuf over the WebSocket | Smaller and faster, but harder to debug and adds a toolchain. Can be added later as a second encoding with a new ADR if v0.5 benchmarks require it |
| JSON Schema as source of truth | Adds a third representation; generating TS from Go is enough while there is one backend |

## Consequences

- One format serves the live stream, rewind and replay, so v0.2 doesn't need a new protocol.
- The allow-list model makes "what leaves the backend" reviewable in one package (`internal/graph`).
- Every new feature that needs more data (for example traffic flows in v0.4) has to extend the model explicitly. That is extra work, by design.
- JSON is more verbose than a binary format; the v0.1 performance baseline (1,000 pods) will show whether that matters.
