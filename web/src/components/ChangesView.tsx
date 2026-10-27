import { useEffect, useMemo, useRef, useState } from "react";
import type { Set as ChangeSet } from "../gen/feed";
import { applyFilter, author, FILTERS, type Filter, filterCounts, fittingRange, justBefore, type Range, shortName, rangeWindow, summarize, tone, verdictLabel, verdictNote, type Window } from "../lib/feed";
import { clock, hm } from "../lib/format";
import { ActivityHistogram } from "./ActivityHistogram";
import { DiffView, DriftView, hasDiff } from "./DiffView";
import { SafetyRecommendation } from "./SafetyRecommendation";

interface Props {
  sets: ChangeSet[];
  now: number;
  query: string;
  /** Live mode: remembers what you've seen ("since you last looked"). */
  live: boolean;
  canRewindTo: (ts: string) => boolean;
  /** Opens the map at a moment, with an object selected. */
  onRewind: (ts: string, select: string) => void;
  /** The model safety recommendations are drafted with, when they're on. */
  recommend?: string;
}

const SEEN = "kubexray.feed.seen";

function readSeen(): number | null {
  try {
    const v = Number(localStorage.getItem(SEEN));
    return Number.isFinite(v) && v > 0 ? v : null;
  } catch {
    return null; // storage blocked: no banner
  }
}

function writeSeen(t: number) {
  try {
    localStorage.setItem(SEEN, String(t));
  } catch {
    // storage blocked: nothing to remember
  }
}

/** The lines under a set's title: each entry, unless the title already says it. */
function details(s: ChangeSet): string[] {
  if (s.entries.length === 1) {
    const e = s.entries[0];
    return e && !s.title.includes(e.summary) ? [e.summary] : [];
  }
  return s.entries.map((e) => `${e.kind} ${e.name}: ${e.summary}`);
}

function touches(s: ChangeSet): string {
  const apps = (s.apps ?? []).filter((a) => !s.entries.some((e) => e.object === a)).map(shortName);
  return apps.length ? `used by ${apps.join(", ")}` : "";
}

/** The change feed (ADR-0012): every change, and what it did. */
export function ChangesView({ sets, now, query, live, canRewindTo, onRewind, recommend }: Props) {
  const [filter, setFilter] = useState<Filter>("all");
  // Changes whose diff is open, by set ID.
  const [diffs, setDiffs] = useState<Set<string>>(() => new Set());
  const toggleDiff = (id: string) =>
    setDiffs((d) => {
      const next = new Set(d);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  const [range, setRange] = useState<Range>(() => fittingRange(sets, now));
  const [picked, setPicked] = useState<Window | null>(null);
  const [openId, setOpenId] = useState<string | null>(null);
  const open = sets.find((s) => s.id === openId) ?? null; // the live set: its verdict updates
  const [seen, setSeen] = useState<number | null>(() => (live ? readSeen() : null));
  // First visit: remember now (the server's clock, like the sets' times), so
  // the next one has something to compare with.
  useEffect(() => {
    if (live && readSeen() === null) writeSeen(now);
  }, [live, now]);

  // Like Kibana's time picker, the histogram's range (or the bars picked in
  // it) scopes the list too; the histogram counts follow the filter and search.
  const span = useMemo(() => picked ?? rangeWindow(range, now), [picked, range, now]);
  const shown = useMemo(() => applyFilter(sets, filter, query, span), [sets, filter, query, span]);
  const counts = useMemo(() => filterCounts(sets, query, span), [sets, query, span]);
  const charted = useMemo(() => applyFilter(sets, filter, query), [sets, filter, query]);
  const midnight = new Date(now).setHours(0, 0, 0, 0);
  const today = useMemo(() => summarize(sets, midnight), [sets, midnight]);
  const unseen = useMemo(() => (seen === null ? null : summarize(sets, seen)), [sets, seen]);

  const markSeen = () => {
    writeSeen(now);
    setSeen(now);
  };
  const rewind = (s: ChangeSet) => {
    setOpenId(null);
    onRewind(justBefore(s), s.incident ?? s.apps?.[0] ?? s.entries[0]?.object ?? "");
  };

  return (
    <main className="feed">
      {live && unseen && unseen.changes > 0 && seen !== null && (
        <section className="feed-banner" aria-label="Since you last looked">
          <div>
            <div className="feed-banner-title">Since you last looked, {hm(new Date(seen).toISOString())}</div>
            <div>
              {unseen.changes} change{unseen.changes === 1 ? "" : "s"} by {unseen.authors} {unseen.authors === 1 ? "person or tool" : "people and tools"}.
              {unseen.broke > 0 && ` ${unseen.broke} broke something.`}
              {unseen.drift > 0 && ` ${unseen.drift} hand edit${unseen.drift === 1 ? "" : "s"} on managed objects.`}
            </div>
          </div>
          <div className="feed-actions">
            {(unseen.broke > 0 || unseen.drift > 0) && (
              <button type="button" className="primary" onClick={() => setFilter(unseen.broke > 0 ? "broke" : "drift")}>
                Show what mattered
              </button>
            )}
            <button type="button" onClick={markSeen}>
              Mark as seen
            </button>
          </div>
        </section>
      )}

      <section className="feed-stats" aria-label="Today at a glance">
        <div className="feed-stat">
          <div className="feed-stat-label">Changes today</div>
          <div className="feed-stat-value">{today.changes}</div>
          <div className="feed-stat-note">by {today.authors} {today.authors === 1 ? "person or tool" : "people and tools"}</div>
        </div>
        <div className="feed-stat">
          <div className="feed-stat-label">Broke something</div>
          <div className={`feed-stat-value${today.broke ? " crit" : ""}`}>{today.broke}</div>
          <div className="feed-stat-note">{today.broke ? "filter by Broke something to see them" : "nothing broke today"}</div>
        </div>
        <div className="feed-stat">
          <div className="feed-stat-label">Settled healthy</div>
          <div className="feed-stat-value ok">{today.healthy}</div>
          <div className="feed-stat-note">{today.settling ? `${today.settling} still settling` : "none still settling"}</div>
        </div>
        <div className="feed-stat">
          <div className="feed-stat-label">Drift</div>
          <div className={`feed-stat-value${today.drift ? " warn" : ""}`}>{today.drift}</div>
          <div className="feed-stat-note">hand edits on managed objects</div>
        </div>
      </section>

      <ActivityHistogram sets={charted} now={now} range={range} onRange={setRange} picked={picked} onPick={setPicked} />

      <div className="feed-filters" role="group" aria-label="Show">
        {FILTERS.map((f) => (
          <button key={f.id} type="button" aria-pressed={filter === f.id} className={filter === f.id ? "on" : ""} onClick={() => setFilter(f.id)}>
            {f.label} <span>{counts[f.id]}</span>
          </button>
        ))}
        {query && <span className="feed-query">matching “{query}”</span>}
      </div>

      <section className="feed-list" aria-label="Changes">
        {shown.length === 0 && (
          <div className="feed-empty">
            {sets.length === 0 ? "No changes recorded yet. They appear here as they happen." : "No changes match in this time. Pick a longer range, or clear the search and filters."}
          </div>
        )}
        {shown.map((s) => (
          <article key={s.id} className={`feed-card ${tone(s)}`}>
            <time dateTime={s.at}>{hm(s.at)}</time>
            <div className="feed-main">
              <div className="feed-title">
                <span className="feed-ns">{s.namespace || "cluster"}</span>
                <span className="feed-name">{s.title}</span>
              </div>
              <div className="feed-by">
                by <strong>{author(s)}</strong>
                {s.entries.length > 1 && ` · ${s.entries.length} changes`}
                {touches(s) && ` · ${touches(s)}`}
              </div>
              {details(s).length > 0 && (
                <ul className="feed-details">
                  {details(s).map((d, i) => (
                    <li key={`${i}-${d}`}>{d}</li>
                  ))}
                </ul>
              )}
              {s.entries
                .filter((e) => e.drift)
                .slice(0, 1)
                .map((e) => (
                  <DriftView key={e.object} entry={e} label />
                ))}
              {diffs.has(s.id) && <DiffView entries={s.entries} />}
            </div>
            <div className="feed-side">
              <span className={`verdict ${tone(s)}`}>{verdictLabel(s)}</span>
              <span className="feed-note">{verdictNote(s)}</span>
              <div className="feed-buttons">
                {canRewindTo(justBefore(s)) && (
                  <button type="button" onClick={() => rewind(s)}>
                    Rewind
                  </button>
                )}
                {hasDiff(s.entries) && (
                  <button type="button" aria-expanded={diffs.has(s.id)} onClick={() => toggleDiff(s.id)}>
                    {diffs.has(s.id) ? "Hide diff" : "See diff"}
                  </button>
                )}
                <button type="button" onClick={() => setOpenId(s.id)}>
                  Details
                </button>
              </div>
            </div>
          </article>
        ))}
      </section>

      {open && (
        <ChangeDetail set={open} canRewind={canRewindTo(justBefore(open))} onRewind={() => rewind(open)} onClose={() => setOpenId(null)} recommend={recommend} />
      )}
    </main>
  );
}

function ChangeDetail({
  set: s,
  canRewind,
  onRewind,
  onClose,
  recommend,
}: {
  set: ChangeSet;
  canRewind: boolean;
  onRewind: () => void;
  onClose: () => void;
  recommend?: string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = dialog.current;
    d?.showModal();
    return () => d?.close();
  }, []);
  const apps = (s.apps ?? []).map(shortName);
  return (
    <dialog ref={dialog} className="feed-drawer" onClose={onClose} aria-labelledby="feed-drawer-title">
      <div className="feed-drawer-head">
        <div>
          <div className="feed-drawer-meta">
            {clock(s.at)} · {s.namespace || "cluster"}
          </div>
          <h2 id="feed-drawer-title">{s.title}</h2>
          <div className="feed-by">
            by <strong>{author(s)}</strong>
            {s.by && s.by.by !== author(s) && ` (field manager ${s.by.by})`}
          </div>
        </div>
        <button type="button" className="icon" aria-label="Close" onClick={onClose}>
          <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
            <path d="M6 6l12 12M18 6 6 18" />
          </svg>
        </button>
      </div>

      <div className={`feed-verdict-box ${tone(s)}`}>
        <span className={`verdict ${tone(s)}`}>{verdictLabel(s)}</span>
        <span>{verdictNote(s) || "kubexray is still watching what it does."}</span>
      </div>

      {apps.length > 0 && (
        <section>
          <h3>What it touches</h3>
          <p>{apps.join(", ")}</p>
        </section>
      )}

      <section>
        <h3>What changed</h3>
        <ul className="feed-drawer-list">
          {s.entries.map((e, i) => (
            <li key={`${e.object}-${i}`}>
              <span className="feed-ns">{e.kind}</span> <strong>{e.name}</strong> {e.summary}
              {e.drift && <DriftView entry={e} />}
              {hasDiff([e]) && <DiffView entries={[e]} />}
            </li>
          ))}
        </ul>
      </section>

      {recommend && s.verdict === "broke" && s.incident && s.ended && <SafetyRecommendation key={s.id} set={s.id} app={s.incident} model={recommend} />}

      <div className="feed-actions">
        {canRewind && (
          <button type="button" className="primary" onClick={onRewind}>
            Rewind to just before
          </button>
        )}
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
    </dialog>
  );
}
