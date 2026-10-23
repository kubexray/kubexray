// Cluster state rebuilt from the stream, exactly as the Go graph.State does
// (ADR-0003): a snapshot, then deltas applied in sequence.
import type { Edge, Message, Node } from "../gen/graph";

export interface ClusterState {
  seq: number;
  ts: string;
  nodes: ReadonlyMap<string, Node>;
  edges: ReadonlyMap<string, Edge>;
}

/** A delta that doesn't follow the state: the stream must resync. */
export class GapError extends Error {
  constructor(at: number, got: number) {
    super(`sequence gap: at ${at}, got ${got}`);
    this.name = "GapError";
  }
}

export function fromSnapshot(msg: Message): ClusterState {
  if (msg.type !== "snapshot") throw new Error(`expected a snapshot, got ${msg.type}`);
  return {
    seq: msg.seq,
    ts: msg.ts,
    nodes: new Map((msg.nodes ?? []).map((n) => [n.id, n])),
    edges: new Map((msg.edges ?? []).map((e) => [e.id, e])),
  };
}

/**
 * Applies a delta and returns the new state. Deltas at or below the state's
 * seq are already included (a client can join between a flush and its
 * broadcast) and are ignored.
 */
export function applyDelta(state: ClusterState, msg: Message): ClusterState {
  if (msg.type !== "delta") throw new Error(`expected a delta, got ${msg.type}`);
  if (msg.seq <= state.seq) return state;
  if (msg.seq !== state.seq + 1) throw new GapError(state.seq, msg.seq);

  const nodes = new Map(state.nodes);
  const edges = new Map(state.edges);
  for (const op of msg.ops ?? []) {
    switch (op.op) {
      case "upsertNode":
        if (op.node) nodes.set(op.node.id, op.node);
        break;
      case "removeNode":
        if (op.id) nodes.delete(op.id);
        break;
      case "upsertEdge":
        if (op.edge) edges.set(op.edge.id, op.edge);
        break;
      case "removeEdge":
        if (op.id) edges.delete(op.id);
        break;
    }
  }
  return { seq: msg.seq, ts: msg.ts, nodes, edges };
}
