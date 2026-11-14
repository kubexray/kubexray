import type { Group, Incident } from "../gen/cause";
import { clock, duration, hm } from "../lib/format";
import { CauseLine, DiagnosisLine, type Entry, entries, SignalTag, stateText } from "./IncidentCard";

interface Props {
  incidents: Incident[];
  groups: Group[];
  /** The moment shown: now when live, else the rewound moment or playhead. */
  now: number;
  /** Live and not rewound: "now" means now. */
  live: boolean;
  query: string;
  /** How many changes broke something up to the moment shown. */
  broke: number;
  onShow: (app: string) => void;
  /** Opens Changes, filtered to the changes that broke something. */
  onBroke: () => void;
}

/** Whether an entry mentions every word of a search. */
function matches(e: Entry, query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const text = [e.group?.cause.summary ?? "", ...e.incidents.flatMap((i) => [i.name, i.namespace, i.symptom.summary, stateText(i), i.causes?.[0]?.summary ?? ""])]
    .join(" ")
    .toLowerCase();
  return words.every((w) => text.includes(w));
}

/** What's broken at the moment shown: now, or where the timeline is. */
export function IncidentsView({ incidents, groups, now, live, query, broke, onShow, onBroke }: Props) {
  const all = entries(incidents, groups);
  const list = all.filter((e) => matches(e, query));
  const when = clock(new Date(now).toISOString());
  return (
    <main className="feed">
      <section aria-label={live ? "Broken now" : `Broken at ${when}`} className="feed-list">
        <h2 className="feed-heading">{live ? "Broken now" : `Broken at ${when}`}</h2>
        {all.length === 0 && <div className="feed-empty">{live ? "Nothing is broken. Every app is running as expected." : `Nothing was broken at ${when}.`}</div>}
        {all.length > 0 && list.length === 0 && <div className="feed-empty">No incident matches “{query}”.</div>}
        {list.map((e) => {
          const first = e.incidents[0];
          if (!first) return null;
          const cause = e.group?.cause ?? first.causes?.[0];
          const since = e.incidents.map((i) => i.since).filter((s): s is string => !!s).sort()[0];
          return (
            <article key={e.group ? `g-${e.group.cause.object}` : first.app} className="feed-card broke">
              <time dateTime={since}>{since ? hm(since) : ""}</time>
              <div className="feed-main">
                <div className="feed-title">
                  <span className="feed-ns">{e.group ? "cluster" : first.namespace}</span>
                  <span className="feed-name">{e.group ? `${e.group.cause.summary}: ${e.incidents.length} apps` : `${first.name} ${stateText(first)}`}</span>
                  {!e.group && <SignalTag inc={first} />}
                </div>
                <div className="feed-by">
                  {e.incidents.map((i) => `${i.name}: ${i.symptom.summary}`).join(" · ")}
                  {since && ` · for ${duration(since, now)}`}
                </div>
                {!e.group && first.diagnosis && <DiagnosisLine diagnosis={first.diagnosis} />}
                {cause && <CauseLine cause={cause} />}
              </div>
              <div className="feed-side">
                <div className="feed-buttons">
                  <button type="button" onClick={() => onShow(first.app)}>
                    Show on map
                  </button>
                </div>
              </div>
            </article>
          );
        })}
        {broke > 0 && (
          <p className="feed-more">
            <button type="button" className="link" onClick={onBroke}>
              {broke} change{broke === 1 ? "" : "s"} broke something{live ? "" : ` up to ${when}`} →
            </button>
          </p>
        )}
      </section>
    </main>
  );
}
