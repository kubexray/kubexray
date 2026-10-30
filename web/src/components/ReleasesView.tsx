import { useContext, useMemo, useState } from "react";
import type { Set as ChangeSet } from "../gen/feed";
import type { Node } from "../gen/graph";
import { ago, who } from "../lib/format";
import { allRevisions, driftOf, Helm, ownerOf, releaseId, type ReleaseRevision, revisionSet } from "../lib/helm";
import type { ClusterState } from "../lib/state";
import { allWorkloads, buildView, workloadHealth } from "../lib/view";
import { ReleaseDetails } from "./ReleaseDetails";

interface Props {
  state: ClusterState;
  sets: ChangeSet[];
  now: number;
  /** The release selected on the map. */
  onShow: (id: string) => void;
  /** The map at a moment, the release selected. */
  onRewind: (id: string, ts: string) => void;
}

interface Release {
  id: string;
  name: string;
  ns: string;
  chart?: string;
  latest?: ReleaseRevision;
  by?: string;
  apps: { id: string; name: string; health: string }[];
  edits: number;
}

const WEEK = 7 * 24 * 3600_000;
const OK = new Set(["deployed", "superseded", "uninstalled"]);

/** Every Helm release in the cluster: what's running, how it got there, and
 * which need a look (ADR-0019's hand edits, failed upgrades). */
export function ReleasesView({ state, sets, now, onShow, onRewind }: Props) {
  const releases = useMemo(() => list(state, sets), [state, sets]);
  const [picked, setPicked] = useState<string | null>(null);
  const [showEdits, setShowEdits] = useState(0);
  const selected = releases.find((r) => r.id === picked) ?? releases[0];
  const helm = useContext(Helm);

  if (releases.length === 0) {
    return (
      <main className="feed releases">
        <p className="feed-empty">
          No Helm releases found. kubexray finds them from the records Helm keeps in each release&apos;s namespace (it reads only their labels).
        </p>
      </main>
    );
  }
  const recent = releases.filter((r) => r.latest && now - Date.parse(r.latest.at) < WEEK).length;
  const failing = releases.filter((r) => r.latest && !OK.has(r.latest.status)).length;
  const edits = releases.reduce((n, r) => n + r.edits, 0);

  return (
    <main className="releases">
      <div className="rel-tiles">
        <div className="rel-tile">
          <span>Helm releases</span>
          <b>{releases.length}</b>
        </div>
        <div className="rel-tile">
          <span>Upgraded in the last 7 days</span>
          <b>{recent}</b>
        </div>
        <div className={`rel-tile${failing ? " bad" : ""}`}>
          <span>Failed or pending</span>
          <b>{failing}</b>
        </div>
        <div className={`rel-tile${edits ? " warn" : ""}`}>
          <span>Hand edits not in a chart</span>
          <b>{edits}</b>
        </div>
      </div>
      <div className="rel-body">
        <table className="rel-table">
          <thead>
            <tr>
              <th>Release</th>
              <th>Chart</th>
              <th>Revision</th>
              <th>Status</th>
              <th>Last upgrade</th>
              <th>Apps</th>
              <th>Hand edits</th>
            </tr>
          </thead>
          <tbody>
            {releases.map((r) => (
              <tr key={r.id} className={r.id === selected?.id ? "sel" : undefined} aria-selected={r.id === selected?.id} onClick={() => setPicked(r.id)}>
                <th scope="row">
                  <button type="button" onClick={() => setPicked(r.id)}>
                    {r.name}
                  </button>
                  <small>{r.ns}</small>
                </th>
                <td className="mono">{r.chart ? r.chart.replace(/-(v?\d[\w.+-]*)$/, " $1") : "–"}</td>
                <td>{r.latest?.version ?? "–"}</td>
                <td>{r.latest && <span className={`rel-status ${r.latest.status === "deployed" ? "ok" : OK.has(r.latest.status) ? "" : "bad"}`}>{r.latest.status}</span>}</td>
                <td>
                  {r.latest ? ago(r.latest.at, now) : "–"}
                  {r.by && <small>{r.by}</small>}
                </td>
                <td>
                  <span className="rel-apps" title={r.apps.map((a) => a.name).join(", ")}>
                    {r.apps.map((a) => (
                      <i key={a.id} className={a.health} />
                    ))}
                  </span>
                </td>
                <td>
                  {r.edits ? (
                    <button
                      type="button"
                      className="rel-status warn rel-edits-chip"
                      aria-label={`${r.name}: see ${r.edits === 1 ? "the hand edit" : `the ${r.edits} hand edits`}`}
                      onClick={(e) => {
                        e.stopPropagation(); // the row would select it too, without scrolling
                        setPicked(r.id);
                        setShowEdits((n) => n + 1);
                      }}
                    >
                      {r.edits === 1 ? "1 edit" : `${r.edits} edits`} ›
                    </button>
                  ) : (
                    <span className="muted">none</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {selected && (
          <aside className="rel-aside">
            <Helm.Provider value={{ ...helm, onRewind: (ts) => onRewind(selected.id, ts) }}>
              <ReleaseDetails id={selected.id} state={state} onSelect={onShow} at={now} showEdits={showEdits} />
            </Helm.Provider>
            <button type="button" className="rel-show" onClick={() => onShow(selected.id)}>
              Show on map
            </button>
          </aside>
        )}
      </div>
    </main>
  );
}

/** The cluster's Helm releases, failing first, then the latest upgraded. */
function list(state: ClusterState, sets: ChangeSet[]): Release[] {
  const objects = new Map<string, Node[]>(); // "ns/name" → what it manages, where it's drawn
  for (const n of state.nodes.values()) {
    const o = ownerOf(n);
    if (o?.tool !== "helm" || !n.ns || n.kind === "Pod" || n.kind === "ReplicaSet") continue;
    const key = `${n.ns}/${o.name}`;
    objects.set(key, [...(objects.get(key) ?? []), n]);
  }
  const revs = new Map<string, ReleaseRevision[]>();
  for (const r of allRevisions(state)) {
    // A release's records live in its namespace, which is usually where its objects are.
    const key = `${r.ns}/${r.release}`;
    revs.set(key, [...(revs.get(key) ?? []), r]);
  }
  const health = new Map(allWorkloads(buildView(state)).map(({ w }) => [w.node.id, workloadHealth(w)]));
  const out: Release[] = [];
  for (const key of new Set([...objects.keys(), ...revs.keys()])) {
    const [ns = "", name = ""] = key.split(/\/(.*)/);
    const objs = objects.get(key) ?? [];
    const latest = (revs.get(key) ?? []).sort((a, b) => b.version - a.version)[0];
    const set = latest && revisionSet(sets, { tool: "helm", name }, latest);
    out.push({
      id: releaseId({ tool: "helm", name }, ns),
      name,
      ns,
      chart: objs.map((n) => n.labels?.["helm.sh/chart"]).find(Boolean),
      latest,
      by: set?.by ? who(set.by) : undefined,
      apps: objs.filter((n) => health.has(n.id)).map((n) => ({ id: n.id, name: n.name, health: health.get(n.id) ?? "ok" })),
      edits: objs.filter((n) => driftOf(sets, n.id)).length,
    });
  }
  const failing = (r: Release) => (r.latest && !OK.has(r.latest.status) ? 1 : 0) + (r.apps.some((a) => a.health === "crit") ? 1 : 0);
  return out.sort((a, b) => failing(b) - failing(a) || Date.parse(b.latest?.at ?? "0") - Date.parse(a.latest?.at ?? "0") || a.name.localeCompare(b.name));
}
