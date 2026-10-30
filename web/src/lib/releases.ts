// The map grouped by Helm release (or Argo CD / Flux app) instead of by
// namespace: each release a section of its own, with the rows it manages,
// and what no tool manages left in its namespaces.
import { type Owner, ownerOf, releaseId } from "./helm";
import { type NamespaceView, type Row, workloadHealth } from "./view";

/** The tool managing a row: its app's, else its Service's or Ingress's. */
export function rowOwner(row: Row): Owner | undefined {
  const n = row.workload?.node ?? row.services[0] ?? row.ingresses[0];
  return n ? ownerOf(n) : undefined;
}

/** A release's section: a namespace view of its rows, and who it is. */
export interface ReleaseSection {
  id: string;
  owner: Owner;
  ns: string;
  view: NamespaceView;
}

/** Each release a section; the namespaces' other rows in rest. Kubernetes'
 * own namespaces are left as they are. */
export function byRelease(view: NamespaceView[]): { releases: ReleaseSection[]; rest: NamespaceView[] } {
  const releases: ReleaseSection[] = [];
  const rest: NamespaceView[] = [];
  for (const ns of view) {
    if (ns.system) continue;
    const groups = new Map<string, { owner: Owner; rows: Row[] }>();
    const loose: Row[] = [];
    for (const row of ns.rows) {
      const o = rowOwner(row);
      if (!o) {
        loose.push(row);
        continue;
      }
      const key = `${o.tool}/${o.name}`;
      const g = groups.get(key) ?? { owner: o, rows: [] };
      g.rows.push(row);
      groups.set(key, g);
    }
    for (const { owner, rows } of groups.values()) {
      releases.push({ id: releaseId(owner, ns.name), owner, ns: ns.name, view: part(ns, owner.name, rows) });
    }
    if (loose.length) rest.push(part(ns, ns.name, loose));
  }
  releases.sort((a, b) => b.view.problems - a.view.problems || a.owner.name.localeCompare(b.owner.name));
  return { releases, rest };
}

/** A namespace view of some of a namespace's rows, and the wires between them. */
function part(ns: NamespaceView, name: string, rows: Row[]): NamespaceView {
  const cards = new Set<string>();
  for (const r of rows) {
    for (const n of [...r.ingresses, ...r.services]) cards.add(n.id);
    if (r.workload) {
      cards.add(r.workload.node.id);
      for (const id of r.workload.uses.keys()) cards.add(`${r.key}|${id}`);
    }
  }
  const health = rows.flatMap((r) => (r.workload ? [workloadHealth(r.workload)] : []));
  return {
    ...ns,
    name,
    rows,
    wires: ns.wires.filter(([a, b]) => cards.has(a) && cards.has(b)),
    workloadCount: health.length,
    serviceCount: new Set(rows.flatMap((r) => r.services.map((s) => s.id))).size,
    problems: health.filter((h) => h === "crit").length,
    allHealthy: health.every((h) => h === "ok"),
  };
}
