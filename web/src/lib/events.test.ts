import { describe, expect, it } from "vitest";
import type { Node } from "../gen/graph";
import { changeEvents, mergeEvents, statusEvents } from "./events";
import { fromSnapshot } from "./state";

const T = "2026-10-23T14:00:00Z";
const dep = (image: string): Node => ({
  id: "Deployment/shop/api", kind: "Deployment", ns: "shop", name: "api", created: T,
  status: { ready: true, replicas: { desired: 1, ready: 1, updated: 1 } },
  spec: { containers: [{ name: "api", image }] },
  change: { at: T, by: "kubectl-set", tool: "kubectl", via: "set" },
});

describe("events", () => {
  const before = fromSnapshot({ v: 1, type: "snapshot", seq: 1, ts: T, nodes: [dep("nginx:1.27")], edges: [] });

  it("turns the server's changes into events with who made them", () => {
    const evs = changeEvents([
      { at: T, object: "Deployment/shop/api", kind: "Deployment", name: "api", namespace: "shop", summary: "rolled out revision 2: image nginx:1.27 → nginx:1.28", by: { at: T, by: "kubectl-set", tool: "kubectl", via: "set" } },
      { at: T, object: "Deployment/kube-system/coredns", kind: "Deployment", name: "coredns", namespace: "kube-system", summary: "changed" },
    ]);
    expect(evs.map((e) => [e.kind, e.text])).toEqual([["change", "Deployment api rolled out revision 2: image nginx:1.27 → nginx:1.28 by kubectl set"]]);
  });

  it("describes status changes, oldest first when merged", () => {
    const evs = statusEvents(before, { v: 1, type: "delta", seq: 2, ts: "2026-10-23T14:00:05Z", ops: [{ op: "upsertNode", node: { ...dep("nginx:1.28"), status: { ready: false, replicas: { desired: 1, ready: 0, updated: 1 } } } }] });
    expect(evs.map((e) => e.text)).toEqual(["api: 0/1 ready"]);
    const merged = mergeEvents(evs, changeEvents([{ at: T, object: "ConfigMap/shop/c", kind: "ConfigMap", name: "c", namespace: "shop", summary: "edited (contents not read)" }]));
    expect(merged.map((e) => e.kind)).toEqual(["change", "info"]);
  });

  it("flags failing pods", () => {
    const pod: Node = { id: "Pod/shop/api-x", kind: "Pod", ns: "shop", name: "api-x", created: T, status: { ready: false, reason: "CrashLoopBackOff" } };
    const evs = statusEvents(before, { v: 1, type: "delta", seq: 2, ts: T, ops: [{ op: "upsertNode", node: pod }] });
    expect(evs[0]).toMatchObject({ kind: "crit", text: "Pod api-x started" });
  });

  it("leaves out system namespaces and noise", () => {
    const sys: Node = { id: "Pod/kube-system/x", kind: "Pod", ns: "kube-system", name: "x", created: T, status: { ready: true } };
    expect(statusEvents(before, { v: 1, type: "delta", seq: 2, ts: T, ops: [{ op: "upsertNode", node: sys }] })).toEqual([]);
  });
});
