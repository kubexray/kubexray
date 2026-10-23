// Diff mode: what changed between two moments of the cluster. Works on two
// cluster states, so the live UI and the replay player share it. Pods are
// summarised per app (they're replaced all the time); ReplicaSets are folded
// into their Deployment's revision.
import type { Container, Node } from "../gen/graph";
import { who } from "./format";
import { podHealth } from "./health";
import type { ClusterState } from "./state";
import { SYSTEM_NAMESPACES } from "./view";

export interface ObjectDiff {
  id: string;
  kind: string;
  ns?: string;
  name: string;
  change: "added" | "removed" | "changed";
  details: string[];
}

const APP_KINDS = new Set(["Deployment", "StatefulSet", "DaemonSet", "Job"]);
/** Kinds listed in their own right; pods count under their app. */
const LISTED = new Set([...APP_KINDS, "Service", "Ingress", "ConfigMap", "Secret", "PersistentVolumeClaim", "Node", "Namespace"]);
/** Order within a namespace: apps first, then what they use. */
const ORDER = ["Node", "Namespace", "Ingress", "Service", "Deployment", "StatefulSet", "DaemonSet", "Job", "ConfigMap", "Secret", "PersistentVolumeClaim"];

/** The app that runs a pod: its topmost owner of an app kind (Pod ← ReplicaSet ← Deployment). */
function appOf(s: ClusterState, owners: Map<string, string>, id: string): string | null {
  let cur = id;
  for (let i = 0; i < 3; i++) {
    const up = owners.get(cur);
    if (!up) break;
    cur = up;
  }
  return cur !== id && APP_KINDS.has(s.nodes.get(cur)?.kind ?? cur.split("/")[0] ?? "") ? cur : null;
}

function ownersOf(s: ClusterState): Map<string, string> {
  const m = new Map<string, string>();
  for (const e of s.edges.values()) if (e.type === "owns") m.set(e.to, e.from);
  return m;
}

/** Each app's active pods. */
function podsByApp(s: ClusterState): Map<string, Node[]> {
  const owners = ownersOf(s);
  const out = new Map<string, Node[]>();
  for (const n of s.nodes.values()) {
    if (n.kind !== "Pod" || n.status.reason === "Terminating" || n.status.phase === "Succeeded") continue;
    const app = appOf(s, owners, n.id);
    if (app) out.set(app, [...(out.get(app) ?? []), n]);
  }
  return out;
}

function resources(c: Container): string {
  const parts = [
    c.requests?.cpu && `cpu request ${c.requests.cpu}`,
    c.requests?.memory && `memory request ${c.requests.memory}`,
    c.limits?.cpu && `cpu limit ${c.limits.cpu}`,
    c.limits?.memory && `memory limit ${c.limits.memory}`,
  ].filter(Boolean);
  return parts.join(", ") || "none";
}

function probes(c: Container): string[] {
  const p = c.probes ?? {};
  return (["liveness", "readiness", "startup"] as const).filter((k) => p[k]);
}

function specDetails(a: Node, b: Node): string[] {
  const out: string[] = [];
  const before = new Map((a.spec?.containers ?? []).map((c) => [`${c.init ? "init:" : ""}${c.name}`, c]));
  for (const c of b.spec?.containers ?? []) {
    if (c.ephemeral) continue; // kubectl debug containers come and go
    const label = c.init ? `init container ${c.name}` : c.name;
    const p = before.get(`${c.init ? "init:" : ""}${c.name}`);
    before.delete(`${c.init ? "init:" : ""}${c.name}`);
    if (!p) {
      out.push(`${label} added (${c.image})`);
      continue;
    }
    if (p.image !== c.image) out.push(`${label} image ${p.image} → ${c.image}`);
    if (resources(p) !== resources(c)) out.push(`${label} resources ${resources(p)} → ${resources(c)}`);
    const was = probes(p);
    const now = probes(c);
    for (const k of now) if (!was.includes(k)) out.push(`${label} ${k} probe added`);
    for (const k of was) if (!now.includes(k)) out.push(`${label} ${k} probe removed`);
  }
  for (const [, c] of before) if (!c.ephemeral) out.push(`${c.init ? `init container ${c.name}` : c.name} removed`);
  const ra = a.spec?.revision;
  const rb = b.spec?.revision;
  if (ra && rb && ra !== rb) out.push(`revision ${ra} → ${rb}`);
  return out;
}

function statusDetails(a: Node, b: Node): string[] {
  const out: string[] = [];
  const ra = a.status.replicas;
  const rb = b.status.replicas;
  if (ra && rb) {
    if (ra.desired !== rb.desired) out.push(`replicas ${ra.desired} → ${rb.desired}`);
    if (ra.ready !== rb.ready) out.push(`ready ${ra.ready}/${ra.desired} → ${rb.ready}/${rb.desired}`);
  } else if (a.status.ready !== b.status.ready) {
    out.push(b.status.ready ? "became ready" : `stopped being ready${b.status.reason ? ` (${b.status.reason})` : ""}`);
  }
  if (a.status.phase !== b.status.phase && a.status.phase && b.status.phase && !ra) out.push(`${a.status.phase} → ${b.status.phase}`);
  return out;
}

function podDetails(a: Node[] | undefined, b: Node[] | undefined): string[] {
  const before = new Set((a ?? []).map((p) => p.id));
  const after = new Set((b ?? []).map((p) => p.id));
  const replaced = [...before].filter((id) => !after.has(id)).length;
  const started = [...after].filter((id) => !before.has(id)).length;
  const failing = (ps: Node[] | undefined) => (ps ?? []).filter((p) => podHealth(p) === "crit").length;
  const out: string[] = [];
  if (replaced || started) {
    const parts = [started && `${started} started`, replaced && `${replaced} gone`].filter(Boolean);
    out.push(`pods: ${parts.join(", ")}`);
  }
  const fa = failing(a);
  const fb = failing(b);
  if (fa !== fb) out.push(`failing pods ${fa} → ${fb}`);
  return out;
}

/** What changed from state a to state b, outside system namespaces. */
export function diffStates(a: ClusterState, b: ClusterState): ObjectDiff[] {
  const podsA = podsByApp(a);
  const podsB = podsByApp(b);
  const shown = (n: Node) => LISTED.has(n.kind) && !(n.ns && SYSTEM_NAMESPACES.has(n.ns)) && !(n.kind === "Namespace" && SYSTEM_NAMESPACES.has(n.name)) && n.name !== "kube-root-ca.crt";
  const out: ObjectDiff[] = [];
  const entry = (n: Node, change: ObjectDiff["change"], details: string[]) => out.push({ id: n.id, kind: n.kind, ns: n.ns, name: n.name, change, details });

  for (const nb of b.nodes.values()) {
    if (!shown(nb)) continue;
    const na = a.nodes.get(nb.id);
    if (!na) {
      entry(nb, "added", nb.change ? [`by ${who(nb.change)}`] : []);
      continue;
    }
    const details = [...specDetails(na, nb), ...statusDetails(na, nb), ...(APP_KINDS.has(nb.kind) ? podDetails(podsA.get(nb.id), podsB.get(nb.id)) : [])];
    // A new write the model can't see into (ConfigMap and Secret contents
    // are never read): say who made it.
    const written = nb.change && (!na.change || Date.parse(nb.change.at) > Date.parse(na.change.at));
    if (written && nb.change) {
      const by = who(nb.change) || "someone";
      details.push(details.length ? `changed by ${by}` : nb.kind === "ConfigMap" || nb.kind === "Secret" ? `edited by ${by} (contents not read)` : `changed by ${by}`);
    }
    if (JSON.stringify(na.labels ?? {}) !== JSON.stringify(nb.labels ?? {}) && !written) details.push("labels changed");
    if (details.length) entry(nb, "changed", details);
  }
  for (const na of a.nodes.values()) if (shown(na) && !b.nodes.has(na.id)) entry(na, "removed", []);

  const rank = (k: string) => (ORDER.includes(k) ? ORDER.indexOf(k) : ORDER.length);
  return out.sort((x, y) => (x.ns ?? "").localeCompare(y.ns ?? "") || rank(x.kind) - rank(y.kind) || x.name.localeCompare(y.name));
}
