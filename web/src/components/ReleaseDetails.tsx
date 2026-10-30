import { useContext } from "react";
import { FeedSets } from "../lib/feedContext";
import { verdictNote } from "../lib/feed";
import { ago, clock, who } from "../lib/format";
import { Helm, ownerOf, parseReleaseId, releaseObjects, revisionSet, TOOL_NAME } from "../lib/helm";
import type { ClusterState } from "../lib/state";
import { useNow } from "../lib/useNow";

const KIND: Record<string, string> = { PersistentVolumeClaim: "Volume" };

/** The Selected panel for a release outlined on the map: its chart, the
 * revision running and who made it, what it manages, and (for Helm) its
 * revisions with a way back to just before each. */
export function ReleaseDetails({ id, state, onSelect, height }: { id: string; state: ClusterState; onSelect: (id: string) => void; height?: number }) {
  const { revisions, onRewind } = useContext(Helm);
  const sets = useContext(FeedSets);
  const now = useNow();
  const sized = height ? { height, maxHeight: "none", flex: "none" } : undefined;
  const rel = parseReleaseId(id);
  const objects = rel ? releaseObjects(state, rel.tool, rel.ns, rel.name) : [];
  const owner = objects.map(ownerOf).find((o) => o?.chart) ?? (objects[0] && ownerOf(objects[0]));
  if (!rel || !owner) {
    return (
      <section className="panel details" aria-live="polite" style={sized}>
        <h2>Selected</h2>
        <p className="empty">This release doesn&apos;t exist at this point in time.</p>
      </section>
    );
  }

  const helm = owner.tool === "helm";
  const revs = helm ? revisions(owner.name, owner.ns ?? rel.ns) : [];
  const current = revs.find((r) => r.status === "deployed") ?? revs[0];
  const chart = objects.map((n) => n.labels?.["helm.sh/chart"]).find(Boolean);
  const app = objects.map((n) => n.labels?.["app.kubernetes.io/version"]).find(Boolean);
  const madeBy = current && revisionSet(sets, owner, current)?.by;

  const facts: [string, string][] = [];
  if (chart) facts.push(["Chart", `${owner.chart ? `${chart.slice(0, -owner.chart.length - 1)} ${owner.chart}` : chart}${app ? ` · app ${app}` : ""}`]);
  if (current) facts.push(["Revision", `${current.version}, ${current.version === 1 ? "installed" : "upgraded"} ${ago(current.at, now)}${madeBy ? ` by ${who(madeBy)}` : ""}`]);
  facts.push(["Namespace", owner.ns && owner.ns !== rel.ns ? `${rel.ns} (release in ${owner.ns})` : rel.ns]);

  return (
    <section className="panel details release-details" aria-live="polite" style={sized}>
      <h2>Selected</h2>
      <div className="kind">{helm ? "Helm release" : `${TOOL_NAME[owner.tool]} app`}</div>
      <div className="name">
        {owner.name}
        {current && <span className={`rel-status ${current.status === "deployed" ? "ok" : current.status === "failed" ? "bad" : ""}`}>{current.status}</span>}
      </div>
      <dl className="facts">
        {facts.map(([k, v]) => (
          <div key={k} className="fact">
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      <ul className="rel-objects" aria-label="What it manages">
        {objects.map((n) => (
          <li key={n.id}>
            <button type="button" onClick={() => onSelect(n.id)}>
              {KIND[n.kind] ?? n.kind} {n.name}
            </button>
          </li>
        ))}
      </ul>
      {helm && (
        <>
          <h3 className="rel-head">Revisions</h3>
          {revs.length === 0 ? (
            <p className="empty">Helm&apos;s revision records aren&apos;t visible to kubexray.</p>
          ) : (
            <ol className="revisions">
              {revs.slice(0, 8).map((r) => {
                const set = revisionSet(sets, owner, r);
                const note = set && (set.verdict === "broke" || set.verdict === "fixed") ? verdictNote(set) : "";
                return (
                  <li key={r.version} className={r.status === "failed" ? "failed" : undefined}>
                    <b>#{r.version}</b>
                    <span className="what">
                      <span>{set ? (r.version === 1 ? set.title.replace(/^Helm upgrade/, "Helm install") : set.title) : `revision ${r.version}`}</span>
                      <small>
                        {clock(r.at)}
                        {note && ` · ${note}`}
                      </small>
                    </span>
                    <span className={`rel-status ${r.status === "deployed" ? "ok" : r.status === "failed" ? "bad" : ""}`}>{r.status}</span>
                    {onRewind && (
                      <button type="button" onClick={() => onRewind(new Date(Date.parse(r.at) - 2000).toISOString())}>
                        Rewind to just before ›
                      </button>
                    )}
                  </li>
                );
              })}
            </ol>
          )}
        </>
      )}
    </section>
  );
}
