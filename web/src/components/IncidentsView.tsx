import type { Group, Incident } from "../gen/cause";
import type { Set as ChangeSet } from "../gen/feed";
import { author, verdictNote } from "../lib/feed";
import { duration, hm } from "../lib/format";
import { CauseLine, DiagnosisLine, entries, SignalTag, stateText } from "./IncidentCard";

interface Props {
  incidents: Incident[];
  groups: Group[];
  sets: ChangeSet[];
  now: number;
  onShow: (app: string) => void;
}

/** What's broken now, and the changes that broke something recently. */
export function IncidentsView({ incidents, groups, sets, now, onShow }: Props) {
  const list = entries(incidents, groups);
  const broke = sets.filter((s) => s.verdict === "broke");
  return (
    <main className="feed">
      <section aria-label="Broken now" className="feed-list">
        <h2 className="feed-heading">Broken now</h2>
        {list.length === 0 && <div className="feed-empty">Nothing is broken. Every app is running as expected.</div>}
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
      </section>

      <section aria-label="Changes that broke something" className="feed-list">
        <h2 className="feed-heading">Changes that broke something</h2>
        {broke.length === 0 && <div className="feed-empty">No change has broken anything in the recorded history.</div>}
        {broke.map((s) => (
          <article key={s.id} className="feed-card">
            <time dateTime={s.at}>{hm(s.at)}</time>
            <div className="feed-main">
              <div className="feed-title">
                <span className="feed-ns">{s.namespace || "cluster"}</span>
                <span className="feed-name">{s.title}</span>
              </div>
              <div className="feed-by">
                by <strong>{author(s)}</strong> · {verdictNote(s)}
              </div>
            </div>
            <div className="feed-side">
              {s.incident && (
                <div className="feed-buttons">
                  <button type="button" onClick={() => onShow(s.incident ?? "")}>
                    Show on map
                  </button>
                </div>
              )}
            </div>
          </article>
        ))}
      </section>
    </main>
  );
}
