import { useState } from "react";
import type { Cause, Group, Incident } from "../gen/cause";
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

/** One card entry: a group of incidents sharing a cause, or one incident. */
export interface Entry {
  group: Group | null;
  incidents: Incident[];
}

/** Mirrors cause.Entries: entries with an app that's down first, then larger
 * groups, then the oldest. Groups naming no known incident are dropped. */
export function entries(incidents: Incident[], groups: Group[]): Entry[] {
  const byApp = new Map(incidents.map((i) => [i.app, i]));
  const grouped = new Set<string>();
  const out: Entry[] = [];
  for (const g of groups) {
    const members = g.apps.flatMap((a) => byApp.get(a) ?? []);
    if (!members.length) continue;
    members.forEach((m) => grouped.add(m.app));
    out.push({ group: g, incidents: members });
  }
  for (const inc of rankIncidents(incidents)) if (!grouped.has(inc.app)) out.push({ group: null, incidents: [inc] });
  const since = (e: Entry) => e.incidents.map((i) => i.since ?? "").filter(Boolean).sort()[0] ?? "";
  const anyDown = (e: Entry) => Number(e.incidents.some(down));
  return out.sort((a, b) => anyDown(b) - anyDown(a) || b.incidents.length - a.incidents.length || since(a).localeCompare(since(b)));
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
  /** From entries(): the first is shown in full, the rest as chips. */
  list: Entry[];
  /** Pinned time when rewound; otherwise the card ticks with the clock. */
  at: string | null;
  canRewindTo: (ts: string) => boolean;
  onShow: (id: string) => void;
  onRewind: (ts: string) => void;
  /** Extra buttons for the shown entry (share, post-mortem). */
  actions?: (entry: Entry) => React.ReactNode;
}

/** How many other incidents to list before "+N more". */
const MORE_SHOWN = 4;

export function IncidentCard({ list, at, canRewindTo, onShow, onRewind, actions }: Props) {
  const live = useNow();
  const [allShown, setAllShown] = useState(false);
  const now = at ? Date.parse(at) : live;
  const [top, ...rest] = list;
  const first = top?.incidents[0];
  if (!top || !first) return null;

  const cause = top.group ? top.group.cause : first.causes?.[0];
  const since = top.incidents.map((i) => i.since).filter((s): s is string => !!s).sort()[0];
  const changedAt = cause?.at;
  const more = rest.flatMap((e) => e.incidents);

  return (
    <section className="incident" aria-live="polite">
      <div className="inc-head">
        <span className="badge">Incident</span>
        {top.group ? (
          <h2>
            {top.group.cause.summary}: {top.incidents.length} apps affected
          </h2>
        ) : (
          <h2>
            {first.name} {stateText(first)}
          </h2>
        )}
        <span className="since">
          {top.group ? "" : `in ${first.namespace} · `}
          {since && <>for {duration(since, now)} · since {clock(since)}</>}
          {!top.group && <> · {first.symptom.summary}</>}
        </span>
      </div>
      {top.group && (
        <div className="affected">
          {top.incidents.map((i) => (
            <button type="button" key={i.app} className="chip-btn" onClick={() => onShow(i.app)}>
              {i.namespace}/{i.name} <span>{stateText(i)}: {i.symptom.summary}</span>
            </button>
          ))}
        </div>
      )}
      {cause ? (
        <CauseLine cause={cause} />
      ) : (
        <div className="cause">
          <span className="lbl">No cause found</span>
          <span>{first.noCause}. It may be outside the cluster: a dependency, the network, or traffic.</span>
        </div>
      )}
      <div className="inc-actions">
        <button type="button" className="primary" onClick={() => onShow(first.app)}>
          Show on map
        </button>
        {changedAt && canRewindTo(changedAt) && (
          <button type="button" onClick={() => onRewind(changedAt)}>
            Rewind to the change
          </button>
        )}
        {actions?.(top)}
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
