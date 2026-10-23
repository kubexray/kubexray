import { useMemo } from "react";
import { diffStates, type ObjectDiff } from "../lib/diff";
import { clock } from "../lib/format";
import type { ClusterState } from "../lib/state";

interface Props {
  from: { at: string; state: ClusterState };
  to: { at: string; state: ClusterState };
  onSelect: (id: string) => void;
  onClose: () => void;
}

const LABEL = { added: "Added", removed: "Removed", changed: "Changed" } as const;

/** Diff mode: what changed between the moment compared from and the one shown. */
export function DiffPanel({ from, to, onSelect, onClose }: Props) {
  // Earlier moment first, whichever way the user moved.
  const [a, b] = Date.parse(from.at) <= Date.parse(to.at) ? [from, to] : [to, from];
  const diffs = useMemo(() => diffStates(a.state, b.state), [a.state, b.state]);
  const byNs = new Map<string, ObjectDiff[]>();
  for (const d of diffs) {
    const list = byNs.get(d.ns ?? "");
    if (list) list.push(d);
    else byNs.set(d.ns ?? "", [d]);
  }

  return (
    <section className="panel compare-panel" aria-label="Changes between two moments">
      <div className="diff-head">
        <h2>Changes</h2>
        <button type="button" onClick={onClose}>
          Done
        </button>
      </div>
      <p className="diff-span">
        {clock(a.at)} → {clock(b.at)}
      </p>
      <p className="empty">{diffs.length === 0 ? "Nothing changed between these moments. Move the timeline to compare with another." : "Move the timeline to compare with another moment."}</p>
      {[...byNs].map(([ns, list]) => (
        <div key={ns} className="diff-ns">
          <h3>{ns || "Cluster"}</h3>
          <ul>
            {list.map((d) => (
              <li key={d.id}>
                <button type="button" className={`diff-row ${d.change}`} onClick={() => onSelect(d.id)} disabled={!to.state.nodes.has(d.id)}>
                  <span className="tag">{LABEL[d.change]}</span>
                  <span className="what">
                    <span className="kind">{d.kind}</span> {d.name}
                  </span>
                </button>
                {d.details.length > 0 && (
                  <ul className="diff-details">
                    {d.details.map((x) => (
                      <li key={x}>{x}</li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        </div>
      ))}
    </section>
  );
}
