// "What happened": readable events from each delta, computed in the browser
// from the state before and after it.
import type { Message, Node } from "../gen/graph";
import { nodeSummary, podHealth } from "./health";
import type { ClusterState } from "./state";
import { SYSTEM_NAMESPACES } from "./view";

export interface ClusterEvent {
  id: number;
  ts: string;
  text: string;
  kind: "change" | "crit" | "info";
}

const shown = (n: Node) => !!n.ns && !SYSTEM_NAMESPACES.has(n.ns) && n.kind !== "ReplicaSet" && n.name !== "kube-root-ca.crt";

const images = (n: Node) => (n.spec?.containers ?? []).filter((c) => !c.init && !c.ephemeral).map((c) => c.image).join(", ");

let nextId = 1;

export function eventsFor(before: ClusterState, msg: Message): ClusterEvent[] {
  const out: ClusterEvent[] = [];
  const add = (text: string, kind: ClusterEvent["kind"]) => out.push({ id: nextId++, ts: msg.ts, text, kind });
  for (const op of msg.ops ?? []) {
    if (op.op === "upsertNode" && op.node && shown(op.node)) {
      const n = op.node;
      const prev = before.nodes.get(n.id);
      const crit = n.kind === "Pod" && podHealth(n) === "crit";
      if (!prev) {
        add(`${n.kind} ${n.name} started`, crit ? "crit" : "info");
        continue;
      }
      const [a, b] = [images(prev), images(n)];
      if (a && b && a !== b) add(`${n.name}: image ${a} → ${b}${who(n)}`, "change");
      if ((n.kind === "ConfigMap" || n.kind === "Secret") && n.change && n.change.at !== prev.change?.at) {
        add(`${n.kind} ${n.name} edited${who(n)}`, "change");
      }
      if (nodeSummary(prev) !== nodeSummary(n)) add(`${n.name}: ${nodeSummary(n)}`, crit ? "crit" : "info");
    } else if (op.op === "removeNode" && op.id) {
      const prev = before.nodes.get(op.id);
      if (prev && shown(prev)) add(`${prev.kind} ${prev.name} removed`, "info");
    }
  }
  return out;
}

function who(n: Node): string {
  const c = n.change;
  if (!c) return "";
  if (c.tool === "kubectl" && c.via) return ` (kubectl ${c.via})`;
  return c.tool === "other" || c.tool === "controller" ? ` (${c.by})` : ` (${c.tool})`;
}
