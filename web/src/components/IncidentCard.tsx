import { useState } from "react";
import type { Cause, Incident } from "../gen/cause";
import { clock, duration, who } from "../lib/format";
import { useNow } from "../lib/useNow";

export function down(inc: Incident): boolean {
  return inc.symptom.desired > 0 && inc.symptom.ready === 0;
}

/** Mirrors cause.Incident.State: down, degraded, or failing pods during an
 * otherwise healthy rollout. */
export function stateText(inc: Incident): string {
  if (inc.healthy) return "is healthy";
  if (down(inc)) return "is down";
  if (inc.symptom.ready < inc.symptom.desired) return "is degraded";
  return "has failing pods";
}

/** Most severe first: apps that are fully down, then the oldest. */
export function rankIncidents(incidents: Incident[]): Incident[] {
  return [...incidents].sort((a, b) => Number(down(b)) - Number(down(a)) || (a.since ?? "").localeCompare(b.since ?? ""));
}

const LABEL: Record<string, string> = { "very likely": "Very likely cause", likely: "Likely cause", possible: "Possible cause" };

export function CauseLine({ cause }: { cause: Cause }) {
  const author = who(cause.by);
  return (
    <div className="cause">
      <span className="lbl">{LABEL[cause.confidence] ?? "Cause"}</span>
      <span>
        {cause.summary}
        {author && <> by {author}</>}
        {cause.at && cause.kind !== "missing" && <> at {clock(cause.at)}</>}
      </span>
      {cause.from && cause.to && (
        <span className="diff">
          <span className="old">{cause.from}</span>→<span className="new">{cause.to}</span>
        </span>
      )}
    </div>
  );
}

interface Props {
  incidents: Incident[];
  /** Pinned time when rewound; otherwise the card ticks with the clock. */
  at: string | null;
  canRewindTo: (ts: string) => boolean;
  onShow: (id: string) => void;
  onRewind: (ts: string) => void;
}

/** How many other incidents to list before "+N more". */
const MORE_SHOWN = 4;

export function IncidentCard({ incidents, at, canRewindTo, onShow, onRewind }: Props) {
  const live = useNow();
  const [allShown, setAllShown] = useState(false);
  const now = at ? Date.parse(at) : live;
  const [inc, ...more] = rankIncidents(incidents);
  if (!inc) return null;
  const top = inc.causes?.[0];
  const changedAt = top?.at;
  return (
    <section className="incident" aria-live="polite">
      <div className="inc-head">
        <span className="badge">Incident</span>
        <h2>
          {inc.name} {stateText(inc)}
        </h2>
        <span className="since">
          in {inc.namespace}
          {inc.since && (
            <>
              {" "}· for {duration(inc.since, now)} · since {clock(inc.since)}
            </>
          )}{" "}
          · {inc.symptom.summary}
        </span>
      </div>
      {top ? (
        <CauseLine cause={top} />
      ) : (
        <div className="cause">
          <span className="lbl">No cause found</span>
          <span>{inc.noCause}. It may be outside the cluster: a dependency, the network, or traffic.</span>
        </div>
      )}
      <div className="inc-actions">
        <button type="button" className="primary" onClick={() => onShow(inc.app)}>
          Show on map
        </button>
        {changedAt && canRewindTo(changedAt) && (
          <button type="button" onClick={() => onRewind(changedAt)}>
            Rewind to the change
          </button>
        )}
        {more.length > 0 && (
          <span className="more">
            {more.length === 1 ? "Also:" : `${more.length} more incidents:`}{" "}
            {(allShown ? more : more.slice(0, MORE_SHOWN)).map((m) => (
              <button type="button" key={m.app} className="chip-btn" onClick={() => onShow(m.app)}>
                {m.namespace}/{m.name} <span>{m.symptom.summary}</span>
              </button>
            ))}
            {more.length > MORE_SHOWN && (
              <button type="button" className="chip-btn link" onClick={() => setAllShown(!allShown)}>
                {allShown ? "Show fewer" : `+${more.length - MORE_SHOWN} more`}
              </button>
            )}
          </span>
        )}
      </div>
    </section>
  );
}
