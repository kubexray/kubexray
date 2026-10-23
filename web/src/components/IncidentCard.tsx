import type { Cause, Incident } from "../gen/cause";
import { clock, duration, who } from "../lib/format";

export function down(inc: Incident): boolean {
  return inc.symptom.desired > 0 && inc.symptom.ready === 0;
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
  now: number;
  canRewindTo: (ts: string) => boolean;
  onShow: (id: string) => void;
  onRewind: (ts: string) => void;
}

export function IncidentCard({ incidents, now, canRewindTo, onShow, onRewind }: Props) {
  const [inc, ...more] = rankIncidents(incidents);
  if (!inc) return null;
  const top = inc.causes?.[0];
  const changedAt = top?.at;
  return (
    <section className="incident" aria-live="polite">
      <div className="inc-head">
        <span className="badge">Incident</span>
        <h2>
          {inc.name} {down(inc) ? "is down" : "is degraded"}
        </h2>
        {inc.since && (
          <span className="since">
            for {duration(inc.since, now)} · since {clock(inc.since)} · {inc.symptom.summary}
          </span>
        )}
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
            Also:{" "}
            {more.map((m) => (
              <button type="button" key={m.app} className="chip-btn" onClick={() => onShow(m.app)}>
                {m.name} <span>{m.symptom.summary}</span>
              </button>
            ))}
          </span>
        )}
      </div>
    </section>
  );
}
