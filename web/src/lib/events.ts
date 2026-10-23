// "What happened": who changed what (from the server, which keeps the
// history), plus how pods and apps reacted (from the live stream).
import type { Change } from "../gen/cause";
import type { Message, Node } from "../gen/graph";
import { who } from "./format";
import { nodeSummary, podHealth } from "./health";
import type { ClusterState } from "./state";
import { SYSTEM_NAMESPACES } from "./view";

export interface ClusterEvent {
  id: string;
  ts: string;
  text: string;
  kind: "change" | "crit" | "info";
}

const shown = (n: Node) => !!n.ns && !SYSTEM_NAMESPACES.has(n.ns) && n.kind !== "ReplicaSet" && n.name !== "kube-root-ca.crt";

let nextId = 1;

/** Status events from one delta: apps and pods starting, failing, recovering. */
export function statusEvents(before: ClusterState, msg: Message): ClusterEvent[] {
  const out: ClusterEvent[] = [];
  const add = (text: string, kind: ClusterEvent["kind"]) => out.push({ id: `s${nextId++}`, ts: msg.ts, text, kind });
  for (const op of msg.ops ?? []) {
    if (op.op === "upsertNode" && op.node && shown(op.node)) {
      const n = op.node;
      const prev = before.nodes.get(n.id);
      const crit = n.kind === "Pod" && podHealth(n) === "crit";
      if (!prev) add(`${n.kind} ${n.name} started`, crit ? "crit" : "info");
      else if (nodeSummary(prev) !== nodeSummary(n)) add(`${n.name}: ${nodeSummary(n)}`, crit ? "crit" : "info");
    } else if (op.op === "removeNode" && op.id) {
      const prev = before.nodes.get(op.id);
      if (prev && shown(prev)) add(`${prev.kind} ${prev.name} removed`, "info");
    }
  }
  return out;
}

/** The server's change list as events. */
export function changeEvents(changes: Change[]): ClusterEvent[] {
  return changes
    .filter((c) => !c.namespace || !SYSTEM_NAMESPACES.has(c.namespace))
    .map((c) => ({
      id: `c${c.object}@${c.at}`,
      ts: c.at,
      text: `${c.kind} ${c.name} ${c.summary}${c.by ? ` by ${who(c.by)}` : ""}`,
      kind: "change" as const,
    }));
}

/** Both kinds of events, oldest first. */
export function mergeEvents(status: ClusterEvent[], changes: ClusterEvent[]): ClusterEvent[] {
  // Compare instants, not strings: the server and the stream can use different
  // time zone offsets.
  return [...status, ...changes].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts) || a.id.localeCompare(b.id));
}
