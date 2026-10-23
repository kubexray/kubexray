import { describe, expect, it } from "vitest";
import type { Message, Node } from "../gen/graph";
import { applyDelta, fromSnapshot, GapError } from "./state";

const node = (id: string): Node => ({ id, kind: "Pod", name: id, status: { ready: true }, created: "2026-10-23T14:00:00Z" });
const snapshot = (seq: number, ...ids: string[]): Message => ({ v: 1, type: "snapshot", seq, ts: "t", nodes: ids.map(node), edges: [] });
const delta = (seq: number, ops: NonNullable<Message["ops"]>): Message => ({ v: 1, type: "delta", seq, ts: "t", ops });

describe("cluster state", () => {
  it("applies deltas in order", () => {
    let s = fromSnapshot(snapshot(1, "a"));
    s = applyDelta(s, delta(2, [{ op: "upsertNode", node: node("b") }, { op: "upsertEdge", edge: { id: "owns:a:b", type: "owns", from: "a", to: "b" } }]));
    s = applyDelta(s, delta(3, [{ op: "removeNode", id: "a" }, { op: "removeEdge", id: "owns:a:b" }]));
    expect([...s.nodes.keys()]).toEqual(["b"]);
    expect(s.edges.size).toBe(0);
    expect(s.seq).toBe(3);
  });

  it("ignores deltas already in the snapshot", () => {
    // A client can join between a flush and its broadcast.
    const s = fromSnapshot(snapshot(5, "a"));
    expect(applyDelta(s, delta(5, [{ op: "removeNode", id: "a" }]))).toBe(s);
  });

  it("detects gaps", () => {
    expect(() => applyDelta(fromSnapshot(snapshot(1)), delta(3, []))).toThrow(GapError);
  });

  it("never mutates the previous state", () => {
    const before = fromSnapshot(snapshot(1, "a"));
    applyDelta(before, delta(2, [{ op: "removeNode", id: "a" }]));
    expect(before.nodes.has("a")).toBe(true);
  });

  it("rejects the wrong message types", () => {
    expect(() => fromSnapshot(delta(1, []))).toThrow();
    expect(() => applyDelta(fromSnapshot(snapshot(1)), snapshot(2))).toThrow();
  });
});
