import { describe, expect, it } from "vitest";
import type { Node } from "../gen/graph";
import { eventsFor } from "./events";
import { fromSnapshot } from "./state";

const T = "2026-10-23T14:00:00Z";
const dep = (image: string): Node => ({
  id: "Deployment/shop/api", kind: "Deployment", ns: "shop", name: "api", created: T,
  status: { ready: true, replicas: { desired: 1, ready: 1, updated: 1 } },
  spec: { containers: [{ name: "api", image }] },
  change: { at: T, by: "kubectl-set", tool: "kubectl", via: "set" },
});

describe("eventsFor", () => {
  const before = fromSnapshot({ v: 1, type: "snapshot", seq: 1, ts: T, nodes: [dep("nginx:1.27")], edges: [] });

  it("describes image changes with who made them", () => {
    const evs = eventsFor(before, { v: 1, type: "delta", seq: 2, ts: T, ops: [{ op: "upsertNode", node: dep("nginx:1.28") }] });
    expect(evs.map((e) => [e.kind, e.text])).toEqual([["change", "api: image nginx:1.27 → nginx:1.28 (kubectl set)"]]);
  });

  it("flags failing pods", () => {
    const pod: Node = { id: "Pod/shop/api-x", kind: "Pod", ns: "shop", name: "api-x", created: T, status: { ready: false, reason: "CrashLoopBackOff" } };
    const evs = eventsFor(before, { v: 1, type: "delta", seq: 2, ts: T, ops: [{ op: "upsertNode", node: pod }] });
    expect(evs[0]).toMatchObject({ kind: "crit", text: "Pod api-x started" });
  });

  it("leaves out system namespaces and noise", () => {
    const sys: Node = { id: "Pod/kube-system/x", kind: "Pod", ns: "kube-system", name: "x", created: T, status: { ready: true } };
    expect(eventsFor(before, { v: 1, type: "delta", seq: 2, ts: T, ops: [{ op: "upsertNode", node: sys }] })).toEqual([]);
  });
});
