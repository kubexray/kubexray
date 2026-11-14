import { useMemo } from "react";
import { diffStates, type ObjectDiff } from "../lib/diff";
import { clock } from "../lib/format";
import { useThrottled } from "../lib/hooks";
import type { ClusterState } from "../lib/state";
import { useNow } from "../lib/useNow";

interface Props {
  from: { at: string; state: ClusterState };
  /** The moment shown; its time is null when it's live. */
  to: { at: string | null; state: ClusterState };
  onSelect: (id: string) => void;
  onClose: () => void;
}

const LABEL = { added: "Added", removed: "Removed", changed: "Changed" } as const;

/** Diff mode: what changed between the moment compared from and the one shown. */
/** How often comparing with live diffs the cluster again. */
const LIVE_EVERY = 1000;

export function DiffPanel({ from, to: shown, onSelect, onClose }: Props) {
  // Live, the cluster changes many times a second; diffing it whole each
  // time would keep the page busy for a list read at a glance.
  const live = shown.at === null;
  const throttled = useThrottled(shown.state, LIVE_EVERY);
  const toState = live ? throttled : shown.state;
  const now = useNow();
  const to = { at: shown.at ?? new Date(now).toISOString(), state: toState };
  // Earlier moment first, whichever way the user moved.
  const forward = Date.parse(from.at) <= Date.parse(to.at);
  const [a, b] = forward ? [from, to] : [to, from];
  const fromState = from.state;
  const diffs = useMemo(() => (forward ? diffStates(fromState, toState) : diffStates(toState, fromState)), [forward, fromState, toState]);
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
                <button type="button" className={`diff-row ${d.change}`} onClick={() => onSelect(d.id)} disabled={!toState.nodes.has(d.id)}>
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
