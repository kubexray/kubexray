// "What happened": who changed what (from the server, which keeps the
// history), plus how pods and apps reacted (from the live stream).
import type { Change } from "../gen/cause";
import type { Edge, Message, Node } from "../gen/graph";
import { who } from "./format";
import { nodeSummary, podHealth } from "./health";
import type { ClusterState } from "./state";
import { SYSTEM_NAMESPACES } from "./view";

export interface ClusterEvent {
  id: string;
  ts: string;
  text: string;
  kind: "change" | "crit" | "info";
  ns?: string; // the object's namespace; none for cluster-scoped objects
  object?: string; // the object's ID
  label?: string; // the object as people say it: "Pod api-64bd-x"
  what?: string; // what happened to it: "started", "ErrImagePull"
  who?: string; // who did it, for changes
  app?: string; // the app it belongs to: a pod's workload, or the workload itself
}

const APP_KINDS = new Set(["Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob"]);

/** The app an object belongs to, following ownerReferences up (a pod to its
 * ReplicaSet to its Deployment). edges may add owners not in the state yet:
 * a new pod arrives with its owner edge. */
export function appOf(state: ClusterState, id: string, edges: Edge[] = []): string | undefined {
  const owner = (child: string) =>
    edges.find((e) => e.type === "owns" && e.to === child)?.from ?? [...state.edges.values()].find((e) => e.type === "owns" && e.to === child)?.from;
  let cur: string | undefined = id;
  for (let i = 0; cur && i < 4; i++) {
    const kind = cur.slice(0, cur.indexOf("/"));
    if (APP_KINDS.has(kind)) {
      const up = owner(cur); // a CronJob's Job belongs to the CronJob
      return up && APP_KINDS.has(up.slice(0, up.indexOf("/"))) ? up : cur;
    }
    cur = owner(cur);
  }
  return undefined;
}

const shown = (n: Node) => !!n.ns && !SYSTEM_NAMESPACES.has(n.ns) && n.kind !== "ReplicaSet" && n.name !== "kube-root-ca.crt";

let nextId = 1;

/** Status events from one delta: apps and pods starting, failing, recovering. */
export function statusEvents(before: ClusterState, msg: Message): ClusterEvent[] {
  const out: ClusterEvent[] = [];
  const edges = (msg.ops ?? []).flatMap((op) => (op.op === "upsertEdge" && op.edge ? [op.edge] : []));
  const add = (n: Node, text: string, what: string, kind: ClusterEvent["kind"]) =>
    out.push({ id: `s${nextId++}`, ts: msg.ts, text, kind, ns: n.ns, object: n.id, label: `${n.kind} ${n.name}`, what, app: appOf(before, n.id, edges) });
  for (const op of msg.ops ?? []) {
    if (op.op === "upsertNode" && op.node && shown(op.node)) {
      const n = op.node;
      const prev = before.nodes.get(n.id);
      const crit = n.kind === "Pod" && podHealth(n) === "crit";
      if (!prev) add(n, `${n.kind} ${n.name} started`, "started", crit ? "crit" : "info");
      else if (nodeSummary(prev) !== nodeSummary(n)) add(n, `${n.name}: ${nodeSummary(n)}`, nodeSummary(n), crit ? "crit" : "info");
    } else if (op.op === "removeNode" && op.id) {
      const prev = before.nodes.get(op.id);
      if (prev && shown(prev)) add(prev, `${prev.kind} ${prev.name} removed`, "removed", "info");
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
      ns: c.namespace,
      object: c.object,
      label: `${c.kind} ${c.name}`,
      what: c.summary,
      who: c.by ? who(c.by) : undefined,
      app: APP_KINDS.has(c.kind) ? c.object : undefined,
    }));
}

/** Both kinds of events, oldest first. */
export function mergeEvents(status: ClusterEvent[], changes: ClusterEvent[]): ClusterEvent[] {
  // Compare instants, not strings: the server and the stream can use different
  // time zone offsets.
  return [...status, ...changes].sort((a, b) => Date.parse(a.ts) - Date.parse(b.ts) || a.id.localeCompare(b.id));
}
