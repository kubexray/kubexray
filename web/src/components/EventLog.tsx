import { useEffect, useState } from "react";
import type { ClusterEvent } from "../lib/events";
import { type LogFilter, logCounts, logRows, type Scope } from "../lib/eventlog";
import { clock } from "../lib/format";

interface Props {
  events: ClusterEvent[];
  at: string | null;
  /** The selected app, which the log can narrow to. */
  scope: Scope | null;
  scopeName?: string;
  /** An event clicked: show its object at that moment. */
  onPick: (id: string, ts: string) => void;
  /** The moment of the event pointed at, for the timeline to mark. */
  onHover: (ts: string | null) => void;
  full: boolean;
  onFull: (full: boolean) => void;
}

const FILTERS: { id: LogFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "problems", label: "Problems" },
  { id: "changes", label: "Changes" },
];

/** "What happened": changes since kubexray started, newest first. Narrow,
 * it's a list; widened, a table of what changed and who changed it. */
export function EventLog({ events, at, scope, scopeName, onPick, onHover, full, onFull }: Props) {
  const [filter, setFilter] = useState<LogFilter>("all");
  const [narrowed, setNarrowed] = useState(true);
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const active = narrowed ? scope : null;
  const rows = logRows(events, filter, active);
  const counts = logCounts(events, active);
  const cut = at ? Date.parse(at) : Infinity;

  useEffect(() => {
    if (!full) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onFull(false);
    addEventListener("keydown", esc);
    return () => removeEventListener("keydown", esc);
  }, [full, onFull]);

  const row = (e: ClusterEvent, child = false) => (
    <li key={e.id} className={Date.parse(e.ts) > cut ? "future" : undefined}>
      <button
        type="button"
        className={`log-row${child ? " child" : ""}`}
        onClick={() => onPick(e.what === "removed" ? (e.app ?? e.object ?? "") : (e.object ?? ""), e.ts)}
        onPointerEnter={() => onHover(e.ts)}
        onPointerLeave={() => onHover(null)}
        onFocus={() => onHover(e.ts)}
        onBlur={() => onHover(null)}
      >
        <i className={`dot ${e.kind}`} aria-hidden="true" />
        <time dateTime={e.ts}>{clock(e.ts)}</time>
        <span className="line">
          {e.label && <span className="obj">{e.label}</span>} <span className={`what ${e.kind}`}>{e.what ?? e.text}</span> {e.who && <span className="who">{e.who}</span>}
        </span>
        <span className="go" aria-hidden="true">
          Rewind
        </span>
      </button>
    </li>
  );

  return (
    <section className={`panel log${full ? " full" : ""}`} aria-label="What happened">
      <div className="log-head">
        <h2>What happened</h2>
        <div className="log-filters" role="group" aria-label="Show">
          {FILTERS.map((f) => (
            <button key={f.id} type="button" className={`chip${filter === f.id ? " on" : ""}`} aria-pressed={filter === f.id} onClick={() => setFilter(f.id)}>
              {f.label} <em>{counts[f.id]}</em>
            </button>
          ))}
        </div>
        {scope && scopeName && (
          <button type="button" className={`chip scope${narrowed ? " on" : ""}`} aria-pressed={narrowed} onClick={() => setNarrowed(!narrowed)} title={narrowed ? "Show every app" : `Only ${scopeName} and what it uses`}>
            Only {scopeName}
            {narrowed && <span aria-hidden="true"> ✕</span>}
          </button>
        )}
        <button type="button" className="icon" aria-label={full ? "Exit full screen" : "Open full screen"} aria-pressed={full} onClick={() => onFull(!full)}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
            {full ? <path d="M4 14h6v6M20 10h-6V4M14 10l7-7M3 21l7-7" /> : <path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7" />}
          </svg>
        </button>
      </div>
      {rows.length === 0 ? (
        <p className="empty">{events.length === 0 ? "Changes will appear here as they happen." : "Nothing here with these filters."}</p>
      ) : (
        <ol>
          <li className="log-th" aria-hidden="true">
            <span />
            <span>Time</span>
            <span>Object</span>
            <span>What</span>
            <span>Who</span>
          </li>
          {rows.map((r) => {
            if (r.type === "event") return row(r.event);
            const isOpen = open.has(r.id);
            return (
              <li key={r.id} className={Date.parse(r.ts) > cut ? "future" : undefined}>
                <button
                  type="button"
                  className="log-row group"
                  aria-expanded={isOpen}
                  onClick={() =>
                    setOpen((o) => {
                      const next = new Set(o);
                      if (!next.delete(r.id)) next.add(r.id);
                      return next;
                    })
                  }
                  onPointerEnter={() => onHover(r.ts)}
                  onPointerLeave={() => onHover(null)}
                >
                  <i className="dot info" aria-hidden="true" />
                  <time dateTime={r.ts}>{clock(r.ts)}</time>
                  <span className="line">
                    <span className="obj">{r.label}</span> <span className="what">{r.what}</span>
                  </span>
                </button>
                {isOpen && <ol>{r.events.map((e) => row(e, true))}</ol>}
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}
