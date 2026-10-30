// Lays the cluster out the approved way: each namespace a section, each app
// a row reading left to right (way in → load balancer → app → config and
// storage).
import type { Node } from "../gen/graph";
import { ReasonNodeNotReady } from "../gen/cause";
import { activePod, type Health, podHealth } from "./health";
import type { ClusterState } from "./state";

export const SYSTEM_NAMESPACES = new Set(["kube-system", "kube-public", "kube-node-lease", "local-path-storage"]);
const APP_KINDS = new Set(["Deployment", "StatefulSet", "DaemonSet", "Job", "ReplicaSet"]);
const AUTOMATIC = "kube-root-ca.crt"; // mounted into every pod: noise on the map

export interface Workload {
  node: Node;
  pods: Node[];
  /** What the pods use, by ID; missing ones are placeholders. */
  uses: Map<string, Node>;
}

export interface Row {
  key: string;
  workload?: Workload;
  services: Node[];
  ingresses: Node[];
}

/** A connection between two cards; `to` may be "<row>|<id>" for per-row chips. */
export type Wire = [from: string, to: string];

export interface NamespaceView {
  name: string;
  system: boolean;
  rows: Row[];
  wires: Wire[];
  /** Service ID → app IDs it sends traffic to. */
  serviceTargets: Map<string, Set<string>>;
  workloadCount: number;
  serviceCount: number;
  problems: number;
  allHealthy: boolean;
}

export function workloadHealth(w: Workload): Health {
  const hs = w.pods.map(podHealth);
  const r = w.node.status.replicas;
  if (hs.includes("crit") || (r && r.desired > 0 && r.ready === 0)) return "crit";
  if (hs.includes("warn") || (r && !w.node.status.ready)) return "warn";
  return "ok";
}

export function workloadProblem(w: Workload): string {
  const bad = w.pods.filter((p) => podHealth(p) === "crit");
  const first = bad[0];
  if (first) {
    const restarts = first.status.restarts ? ` · ${first.status.restarts} restarts` : "";
    return `${first.status.reason || "Failing"}${restarts}${bad.length > 1 ? ` (${bad.length} pods)` : ""}`;
  }
  const r = w.node.status.replicas;
  if (r && r.desired > 0 && r.ready === 0) return "No pods ready";
  const starting = w.pods.filter((p) => podHealth(p) === "warn").length;
  return starting ? `${starting} pod${starting > 1 ? "s" : ""} starting` : "";
}

/** The ID a wire end points at: wires to per-row chips are "<row>|<id>". */
export const wireTarget = (end: string) => end.slice(end.lastIndexOf("|") + 1);

function push<V>(m: Map<string, V[]>, key: string, v: V) {
  const list = m.get(key);
  if (list) list.push(v);
  else m.set(key, [v]);
}

export function buildView(state: ClusterState): NamespaceView[] {
  const ins = new Map<string, { from: string; type: string }[]>();
  const outs = new Map<string, { to: string; type: string }[]>();
  for (const e of state.edges.values()) {
    push(outs, e.from, { to: e.to, type: e.type });
    push(ins, e.to, { from: e.from, type: e.type });
  }

  // A pod's app is its topmost owner we know about (Pod ← ReplicaSet ← Deployment).
  const appOf = (id: string): string => {
    let cur = id;
    for (let i = 0; i < 4; i++) {
      const up = (ins.get(cur) ?? []).find((e) => e.type === "owns" && APP_KINDS.has(state.nodes.get(e.from)?.kind ?? ""));
      if (!up) break;
      cur = up.from;
    }
    return cur;
  };

  interface NsEntry { workloads: Map<string, Workload>; services: Node[]; ingresses: Node[] }
  const namespaces = new Map<string, NsEntry>();
  const ns = (name: string): NsEntry => {
    let e = namespaces.get(name);
    if (!e) namespaces.set(name, (e = { workloads: new Map(), services: [], ingresses: [] }));
    return e;
  };
  const workload = (entry: NsEntry, node: Node): Workload => {
    let w = entry.workloads.get(node.id);
    if (!w) entry.workloads.set(node.id, (w = { node, pods: [], uses: new Map() }));
    return w;
  };

  // Pods on a NotReady node report nothing themselves; say why they're down.
  // Mirrors the cause engine (internal/cause/cause.go, explain).
  const deadNodes = new Set([...state.nodes.values()].filter((n) => n.kind === "Node" && !n.status.ready).map((n) => n.id));
  const withNodeReason = (p: Node): Node =>
    p.parent && deadNodes.has(p.parent) && !p.status.ready && !p.status.reason ? { ...p, status: { ...p.status, reason: ReasonNodeNotReady } } : p;

  for (const raw of state.nodes.values()) {
    const n = raw.kind === "Pod" ? withNodeReason(raw) : raw;
    if (!n.ns) continue;
    if (n.kind === "Pod") {
      const app = state.nodes.get(appOf(n.id)) ?? n;
      if (!activePod(app.kind, n)) continue;
      const w = workload(ns(n.ns), app);
      w.pods.push(n);
      for (const e of outs.get(n.id) ?? []) {
        if (e.type !== "mounts" || e.to.endsWith(`/${AUTOMATIC}`)) continue;
        const [kind = "", , name = e.to] = e.to.split("/");
        w.uses.set(e.to, state.nodes.get(e.to) ?? { id: e.to, kind, name, ns: n.ns, status: { ready: false }, created: "" });
      }
    } else if (APP_KINDS.has(n.kind) && appOf(n.id) === n.id && n.kind !== "ReplicaSet") {
      workload(ns(n.ns), n);
    } else if (n.kind === "Service") ns(n.ns).services.push(n);
    else if (n.kind === "Ingress") ns(n.ns).ingresses.push(n);
  }

  const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name);
  const out: NamespaceView[] = [];
  for (const [name, entry] of namespaces) {
    const workloads = [...entry.workloads.values()].sort((a, b) => byName(a.node, b.node));
    for (const w of workloads) w.pods.sort(byName);
    const rowOf = new Map<string, Row>();
    const rows: Row[] = workloads.map((w) => {
      const row: Row = { key: w.node.id, workload: w, services: [], ingresses: [] };
      rowOf.set(w.node.id, row);
      return row;
    });
    const wires: Wire[] = [];
    const serviceTargets = new Map<string, Set<string>>();

    for (const svc of entry.services.sort(byName)) {
      const targets = new Set<string>();
      for (const e of outs.get(svc.id) ?? []) {
        if (e.type !== "selects") continue;
        const app = appOf(e.to);
        if (rowOf.has(app)) targets.add(app);
      }
      serviceTargets.set(svc.id, targets);
      const first = [...targets].sort()[0];
      // One that selects nothing (a broken selector, say) sits beside the app
      // it was meant for, unwired: the app of its name, else of its release.
      const meant = first ? undefined : rows.find((r) => r.workload && !r.services.length && r.workload.node.name === svc.name) ?? rows.find((r) => r.workload && !r.services.length && sameOrigin(r.workload.node, svc));
      let row = first ? rowOf.get(first) : meant;
      if (!row) rows.push((row = { key: svc.id, services: [], ingresses: [] }));
      row.services.push(svc);
      rowOf.set(svc.id, row);
      for (const t of targets) wires.push([svc.id, t]);
    }
    for (const ing of entry.ingresses.sort(byName)) {
      const routes = (outs.get(ing.id) ?? []).filter((e) => e.type === "routes").map((e) => e.to);
      let row = routes.map((s) => rowOf.get(s)).find(Boolean);
      if (!row) rows.push((row = { key: ing.id, services: [], ingresses: [] }));
      row.ingresses.push(ing);
      for (const s of routes) if (rowOf.has(s)) wires.push([ing.id, s]);
    }
    for (const r of rows) {
      if (r.workload) for (const id of r.workload.uses.keys()) wires.push([r.workload.node.id, `${r.key}|${id}`]);
    }

    const health = rows.flatMap((r) => (r.workload ? [workloadHealth(r.workload)] : []));
    out.push({
      name,
      system: SYSTEM_NAMESPACES.has(name),
      rows,
      wires,
      serviceTargets,
      workloadCount: health.length,
      serviceCount: entry.services.length,
      problems: health.filter((h) => h === "crit").length,
      allHealthy: health.every((h) => h === "ok"),
    });
  }
  return out.sort((a, b) => Number(a.system) - Number(b.system) || b.workloadCount - a.workloadCount || a.name.localeCompare(b.name));
}

/** Every app in the view, with its namespace. */
/** Whether two objects come from the same Helm release or GitOps app. */
const sameOrigin = (a: Node, b: Node) => !!a.origin?.name && a.origin.tool === b.origin?.tool && a.origin.name === b.origin.name;

export function allWorkloads(view: NamespaceView[]): { ns: string; w: Workload }[] {
  return view.flatMap((ns) => ns.rows.flatMap((r) => (r.workload ? [{ ns: ns.name, w: r.workload }] : [])));
}

/** The IDs connected to `id` through the map's wires: upstream and downstream. */
export function chainOf(view: NamespaceView[], id: string, direction: "both" | "up" = "both"): Set<string> {
  const all = view.flatMap((ns) => ns.wires.map(([f, t]) => [f, wireTarget(t)] as const));
  const up = new Set([id]);
  const down = new Set([id]);
  for (let grew = true; grew; ) {
    grew = false;
    for (const [f, t] of all) {
      if (up.has(t) && !up.has(f)) {
        up.add(f);
        grew = true;
      }
      if (direction === "both" && down.has(f) && !down.has(t)) {
        down.add(t);
        grew = true;
      }
    }
  }
  return new Set([...up, ...down]);
}
