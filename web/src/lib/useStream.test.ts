import { describe, expect, it } from "vitest";
import type { Message, Node } from "../gen/graph";
import { fromSnapshot, GapError } from "./state";
import { deltaBatcher } from "./useStream";

const T = "2026-10-23T14:00:00Z";
const pod = (name: string, reason?: string): Node => ({ id: `Pod/shop/${name}`, kind: "Pod", ns: "shop", name, status: reason ? { ready: false, phase: "Running", reason } : { ready: true, phase: "Running" }, created: T });
const snapshot = fromSnapshot({ v: 1, type: "snapshot", seq: 1, ts: T, nodes: [pod("a")], edges: [] });
const delta = (seq: number, node: Node): Message => ({ v: 1, type: "delta", seq, ts: T, ops: [{ op: "upsertNode", node }] });

describe("deltaBatcher", () => {
  it("shows many deltas as one state, copied once", () => {
    const batch = deltaBatcher(snapshot);
    for (let i = 2; i <= 51; i++) batch.apply(delta(i, pod(`p${i}`)));
    expect(batch.pending()).toBe(true);
    const { state, events } = batch.take();
    expect(state.seq).toBe(51);
    expect(state.nodes.size).toBe(51);
    expect(events).toHaveLength(50);
    expect(batch.pending()).toBe(false);
    // The snapshot handed out before is untouched.
    expect(snapshot.nodes.size).toBe(1);
  });

  it("never changes a state it handed out", () => {
    const batch = deltaBatcher(snapshot);
    batch.apply(delta(2, pod("b")));
    const first = batch.take().state;
    batch.apply(delta(3, pod("c")));
    const second = batch.take().state;
    expect([...first.nodes.keys()]).toEqual(["Pod/shop/a", "Pod/shop/b"]);
    expect(second.nodes.size).toBe(3);
  });

  it("ignores deltas already included and stops at a gap", () => {
    const batch = deltaBatcher(snapshot);
    batch.apply(delta(1, pod("old")));
    expect(batch.pending()).toBe(false);
    expect(() => batch.apply(delta(3, pod("c")))).toThrow(GapError);
  });

  it("reports status changes against the state before each delta", () => {
    const batch = deltaBatcher(snapshot);
    batch.apply(delta(2, pod("a", "CrashLoopBackOff")));
    batch.apply(delta(3, pod("a")));
    batch.apply(delta(4, pod("a")));
    expect(batch.take().events.map((e) => e.kind)).toEqual(["crit", "info"]);
  });
});
