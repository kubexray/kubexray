import { useContext, useRef, useState } from "react";
import type { Incident } from "../gen/cause";
import type { Compare } from "./Workspace";
import { clock, duration, who } from "../lib/format";
import { FeedSets } from "../lib/feedContext";
import { verdictNote } from "../lib/feed";
import { type Mark, model, moments, RANGES, type Range, type Span, step, ticks, windowFor } from "../lib/timeline";
import { useNow } from "../lib/useNow";
import { stateText } from "./IncidentCard";

interface Props {
  bounds: { from: string; to: string } | null;
  /** The moment shown; null means live (live mode only). */
  at: string | null;
  /** Live: the right edge is now and there's a Back to live button.
   * Replay: the right edge is the end of the file and there's Play. */
  mode: "live" | "replay";
  /** The incidents at the moment shown. */
  incidents: Incident[];
  onSeek: (ts: string | null) => void;
  /** Jump to an incident: its app selected, at the moment shown. */
  onFocus?: (app: string, ts: string) => void;
  playing?: boolean;
  onPlay?: () => void;
  /** Diff mode (Workspace). */
  compare?: Compare;
}

type Hover = { left: number; mark?: Mark; span?: Span } | null;

const iso = (t: number) => new Date(t).toISOString();
const hhmm = (t: number) => new Date(t).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
const RANGE_LABEL: Record<Range, string> = { hour: "Hour", day: "Day", week: "Week" };
/** Incidents show once they've lasted the cause engine's grace period. */
const SHOWN_AFTER = 35_000;

/** The rewind timeline: what changed, what broke, what fixed it, over time. */
export function Timeline({ bounds, at, mode, incidents, onSeek, onFocus, playing, onPlay, compare }: Props) {
  const now = useNow();
  const sets = useContext(FeedSets);
  const [range, setRange] = useState<Range>("hour");
  const [hover, setHover] = useState<Hover>(null);
  const plot = useRef<HTMLDivElement>(null);
  const dragging = useRef(false);

  const live = mode === "live";
  const first = bounds ? Date.parse(bounds.from) : 0;
  const last = bounds ? (live ? Math.max(now, Date.parse(bounds.to)) : Date.parse(bounds.to)) : 0;
  const value = at ? Date.parse(at) : last;
  const [from, to] = windowFor(live ? RANGES[range] : null, first, last, value);
  const span = Math.max(1, to - from);
  const pct = (t: number) => Math.min(100, Math.max(0, ((t - from) / span) * 100));

  const m = model(sets, incidents, from, to, value);
  const all = moments(sets, m.spans);
  const peak = Math.max(1, ...m.bars.map((b) => b.count));

  if (!bounds) {
    return (
      <footer className="timeline">
        <span className="hint">Recording… the timeline appears once there's history to rewind.</span>
      </footer>
    );
  }

  const seek = (t: number) => onSeek(live && t >= last - 1000 ? null : iso(Math.min(last, Math.max(first, t))));
  const fromX = (clientX: number) => {
    const r = plot.current?.getBoundingClientRect();
    return r ? from + ((clientX - r.left) / r.width) * span : value;
  };
  const go = (dir: -1 | 1) => {
    const t = step(all, value, dir);
    if (t !== undefined) seek(t);
    else if (dir > 0 && live) onSeek(null);
  };
  const toIncident = (s: Span) => {
    const t = Math.min(s.start + SHOWN_AFTER, (s.fix ?? s.end ?? last) - 1000);
    if (onFocus) onFocus(s.app, iso(t));
    else seek(t);
  };

  const current = incidents.find((i) => !i.healthy);
  const ongoing = live && !at;
  const comparing = compare?.from ? Date.parse(compare.from) : null;

  return (
    <footer className="timeline">
      <div className="tl-bar">
        <div className="tl-transport">
          <button type="button" aria-label="Previous change or incident" onClick={() => go(-1)}>
            ‹
          </button>
          {live ? (
            <button type="button" className={`tl-live ${at ? "" : "on"}`} onClick={() => onSeek(null)} disabled={!at}>
              {at ? "● Back to live" : "● Live"}
            </button>
          ) : (
            <button type="button" className="tl-play" onClick={onPlay}>
              {playing ? "❚❚ Pause" : "▶ Play"}
            </button>
          )}
          <button type="button" aria-label="Next change or incident" onClick={() => go(1)}>
            ›
          </button>
        </div>
        <span className="tl-time">{clock(iso(value))}</span>
        <span className={`tl-now ${current ? "bad" : "ok"}`}>
          <i aria-hidden="true" />
          {current
            ? `${current.name} ${stateText(current)}${current.since ? ` · ${duration(current.since, value)} in` : ""}${current.causes?.[0] ? ` · likely ${current.causes[0].summary}` : ""}`
            : "All apps healthy"}
        </span>
        <span className="tl-spacer" />
        <span className="clock">
          {live ? (at ? "rewound" : "live") : `${duration(bounds.from, value)} of ${duration(bounds.from, last)}`}
        </span>
        {live && (
          <div className="tl-range" role="group" aria-label="How much to show">
            {(Object.keys(RANGES) as Range[]).map((r) => (
              <button key={r} type="button" aria-pressed={range === r} className={range === r ? "on" : ""} onClick={() => setRange(r)}>
                {RANGE_LABEL[r]}
              </button>
            ))}
          </div>
        )}
        {compare && (
          <button type="button" className={`compare${compare.from ? " on" : ""}`} onClick={compare.toggle} aria-pressed={!!compare.from} disabled={compare.disabled}>
            {compare.from ? "Stop comparing" : "Compare"}
          </button>
        )}
      </div>

      <div
        className="tl-plot"
        ref={plot}
        onPointerDown={(e) => {
          if ((e.target as HTMLElement).closest(".tl-hit")) return;
          dragging.current = true;
          e.currentTarget.setPointerCapture(e.pointerId);
          seek(fromX(e.clientX));
        }}
        onPointerMove={(e) => dragging.current && seek(fromX(e.clientX))}
        onPointerUp={() => (dragging.current = false)}
        onPointerLeave={() => setHover(null)}
      >
        {first > from && (
          <div className="tl-unrecorded" style={{ width: `${pct(first)}%` }}>
            {pct(first) > 12 && <span>Not recorded yet</span>}
          </div>
        )}
        {comparing !== null && comparing >= from && (
          <div className="tl-compare" style={{ left: `${pct(comparing)}%`, width: `${Math.max(0, pct(value) - pct(comparing))}%` }}>
            <span>Compared from {hhmm(comparing)}</span>
          </div>
        )}

        {m.spans.map((s) => {
          const fix = s.fix ?? s.end ?? value;
          const end = s.end ?? (s.fix !== undefined ? value : fix);
          return (
            <div key={`${s.app}@${s.start}`}>
              <button
                type="button"
                className={`tl-hit tl-broken${s.end === undefined && s.fix === undefined && ongoing ? " ongoing" : ""}`}
                style={{ left: `${pct(s.start)}%`, width: `${Math.max(0.3, pct(fix) - pct(s.start))}%` }}
                aria-label={`${s.label}: jump to when it broke`}
                onClick={() => toIncident(s)}
                onPointerEnter={() => setHover({ left: pct(s.start), span: s })}
              />
              {s.fix !== undefined && end > s.fix && (
                <div className={`tl-recovering${s.end === undefined && ongoing ? " ongoing" : ""}`} style={{ left: `${pct(s.fix)}%`, width: `${pct(end) - pct(s.fix)}%` }} />
              )}
              <span className={`tl-span-label${pct(s.start) > 80 ? " end" : ""}`} style={pct(s.start) > 80 ? { right: `${100 - pct(end)}%` } : { left: `${pct(s.start)}%` }}>
                {s.label}
              </span>
            </div>
          );
        })}

        <div className="tl-bars" aria-hidden="true">
          {m.bars.map((b) => (
            <i key={b.start} className={b.start + m.bucket <= first ? "off" : undefined} style={{ height: b.count ? `${6 + (b.count / peak) * 34}px` : "2px" }} />
          ))}
        </div>

        {m.marks.map((mk) => (
          <button
            key={`${mk.kind}-${mk.set.id}`}
            type="button"
            className={`tl-hit tl-mark ${mk.kind}${pct(mk.at) < 8 ? " start" : pct(mk.at) > 92 ? " end" : ""}`}
            style={{ left: `${pct(mk.at)}%` }}
            aria-label={`${mk.kind === "cause" ? "Broke something" : "Fixed"}: ${mk.set.title}. Rewind to just before it`}
            onClick={() => seek(mk.at - 1000)}
            onPointerEnter={() => setHover({ left: pct(mk.at), mark: mk })}
          >
            <span>{mk.label}</span>
          </button>
        ))}

        <div className="tl-head" style={{ left: `${pct(value)}%` }} aria-hidden="true" />

        <div className="tl-axis" aria-hidden="true">
          {ticks(from, to).map((t) => (
            <span key={t} style={{ left: `${pct(t)}%` }}>
              {hhmm(t)}
            </span>
          ))}
        </div>

        {hover && <HoverCard hover={hover} value={value} />}
      </div>

      <div className="track">
        <input
          type="range"
          aria-label={live ? "Rewind the cluster" : "Move through the replay"}
          aria-keyshortcuts="ArrowLeft ArrowRight"
          min={first}
          max={last}
          step={1000}
          value={value}
          onChange={(e) => seek(Number(e.target.value))}
          onKeyDown={(e) => {
            if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
              e.preventDefault();
              go(e.key === "ArrowLeft" ? -1 : 1);
            }
          }}
        />
      </div>
    </footer>
  );
}

/** What a mark or an incident was, on hover. */
function HoverCard({ hover, value }: { hover: NonNullable<Hover>; value: number }) {
  const side = hover.left > 70 ? { right: `${100 - hover.left}%` } : { left: `${Math.max(0, hover.left - 4)}%` };
  if (hover.mark) {
    const s = hover.mark.set;
    const line = s.entries.flatMap((e) => e.diff ?? []).find((l) => l.from || l.to);
    return (
      <div className="tl-card" style={side} role="tooltip">
        <span className="meta">
          {clock(s.at)}
          {s.by && ` · ${who(s.by)}`}
        </span>
        <strong>{s.title}</strong>
        {line && (line.from || line.to) && (
          <span className="value">
            <span className="muted">{line.path.replace(/^env /, "")}</span> {line.from && <s>{line.from}</s>} → <b>{line.to}</b>
          </span>
        )}
        <span className={`verdict-line ${hover.mark.kind}`}>{verdictNote(s) || (hover.mark.kind === "fix" ? "Fixed an incident" : "Broke something")}</span>
        <span className="muted">Click to rewind to just before it</span>
      </div>
    );
  }
  const sp = hover.span;
  if (!sp) return null;
  const end = sp.end ?? value;
  return (
    <div className="tl-card" style={side} role="tooltip">
      <span className="meta">
        {clock(new Date(sp.start).toISOString())} · {sp.end === undefined ? "still going" : `for ${duration(new Date(sp.start).toISOString(), end)}`}
      </span>
      <strong>{sp.label}</strong>
      {sp.cause && <span>Likely cause: {sp.cause.title}</span>}
      {sp.fix !== undefined && <span className="verdict-line fix">Fixed at {clock(new Date(sp.fix).toISOString())}</span>}
      <span className="muted">Click to jump to when it broke</span>
    </div>
  );
}
