import { useContext, useEffect, useRef, useState } from "react";
import type { Incident } from "../gen/cause";
import type { Compare } from "./Workspace";
import { clock, duration, who } from "../lib/format";
import { FeedSets } from "../lib/feedContext";
import { verdictNote } from "../lib/feed";
import { keepLabels, labelWidth, type Mark, model, moments, RANGES, type Range, type Span, step, ticks, windowFor } from "../lib/timeline";
import { useNow } from "../lib/useNow";
import { DiffView, hasDiff } from "./DiffView";
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
  // The change a click opened, by set ID: it stays until closed.
  const [pinned, setPinned] = useState<string | null>(null);
  const plot = useRef<HTMLDivElement>(null);
  const dragging = useRef(false);
  // The plot's width, to know which labels fit.
  const [width, setWidth] = useState(1300);
  const shown = bounds !== null;
  useEffect(() => {
    const el = plot.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => e && setWidth(e.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, [shown]);
  useEffect(() => {
    if (!pinned) return;
    const close = (e: KeyboardEvent) => e.key === "Escape" && setPinned(null);
    addEventListener("keydown", close);
    return () => removeEventListener("keydown", close);
  }, [pinned]);

  const live = mode === "live";
  const first = bounds ? Date.parse(bounds.from) : 0;
  const last = bounds ? (live ? Math.max(now, Date.parse(bounds.to)) : Date.parse(bounds.to)) : 0;
  const value = at ? Date.parse(at) : last;
  const [from, to] = windowFor(live ? RANGES[range] : null, first, last, value);
  const span = Math.max(1, to - from);
  const pct = (t: number) => Math.min(100, Math.max(0, ((t - from) / span) * 100));

  const m = model(sets, incidents, from, to, value);
  const all = moments(sets, m.spans);
  // Labels that fit: newest first, the rest a hover away.
  const spanEnd = (s: Span) => s.end ?? (s.fix !== undefined ? value : (s.fix ?? value));
  const spanLabels = keepLabels(
    m.spans.map((s) => {
      const w = labelWidth(s.label.length, width);
      return pct(s.start) > 80 ? { from: pct(spanEnd(s)) - w, to: pct(spanEnd(s)) } : { from: pct(s.start), to: pct(s.start) + w };
    }),
  );
  const markLabels = (kind: Mark["kind"]) =>
    keepLabels(
      m.marks.map((mk) => {
        if (mk.kind !== kind) return { from: -1e9, to: -1e9 };
        const w = labelWidth(mk.label.length, width);
        const x = pct(mk.at);
        return x < 8 ? { from: x, to: x + w } : x > 92 ? { from: x - w, to: x } : { from: x - w / 2, to: x + w / 2 };
      }),
    );
  const causeLabels = markLabels("cause");
  const fixLabels = markLabels("fix");
  const peak = Math.max(1, ...m.bars.map((b) => b.count));
  const open = m.marks.find((mk) => mk.set.id === pinned); // the live set: its verdict updates

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
          if ((e.target as HTMLElement).closest(".tl-hit, .tl-card")) return;
          setPinned(null);
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

        <div className="tl-bars" aria-hidden="true">
          {m.bars.map((b) => (
            <i key={b.start} className={b.start + m.bucket <= first ? "off" : undefined} style={{ height: b.count ? `${6 + (b.count / peak) * 34}px` : "2px" }} />
          ))}
        </div>

        {m.spans.map((s, i) => {
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
              {spanLabels[i] && (
                <span className={`tl-span-label${pct(s.start) > 80 ? " end" : ""}`} style={pct(s.start) > 80 ? { right: `${100 - pct(end)}%` } : { left: `${pct(s.start)}%` }}>
                  {s.label}
                </span>
              )}
            </div>
          );
        })}

        {m.marks.map((mk, i) => (
          <button
            key={`${mk.kind}-${mk.set.id}`}
            type="button"
            className={`tl-hit tl-mark ${mk.kind}${pct(mk.at) < 8 ? " start" : pct(mk.at) > 92 ? " end" : ""}`}
            style={{ left: `${pct(mk.at)}%` }}
            aria-label={`${mk.kind === "cause" ? "Broke something" : "Fixed"}: ${mk.set.title}. See the change`}
            aria-expanded={pinned === mk.set.id}
            onClick={() => setPinned((p) => (p === mk.set.id ? null : mk.set.id))}
            onPointerEnter={() => setHover({ left: pct(mk.at), mark: mk })}
          >
            {(mk.kind === "cause" ? causeLabels[i] : fixLabels[i]) && <span>{mk.label}</span>}
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

        {open ? (
          <ChangeCard
            mark={open}
            left={pct(open.at)}
            onRewind={() => seek(open.at - 1000)}
            onClose={() => setPinned(null)}
          />
        ) : (
          hover && <HoverCard hover={hover} value={value} />
        )}
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

const sideOf = (left: number) => (left > 70 ? { right: `${100 - left}%` } : { left: `${Math.max(0, left - 4)}%` });

/** A change, opened from its mark: who made it, what it did, and its diff. */
function ChangeCard({ mark, left, onRewind, onClose }: { mark: Mark; left: number; onRewind: () => void; onClose: () => void }) {
  const s = mark.set;
  return (
    <section className="tl-card pinned" style={sideOf(left)} aria-label="The change">
      <div className="tl-card-head">
        <span className="meta">
          {clock(s.at)}
          {s.by && ` · ${who(s.by)}`}
        </span>
        <button type="button" className="tl-close" aria-label="Close" onClick={onClose}>
          ×
        </button>
      </div>
      <strong>{s.title}</strong>
      <span className={`verdict-line ${mark.kind}`}>{verdictNote(s) || (mark.kind === "fix" ? "Fixed an incident" : "Broke something")}</span>
      {hasDiff(s.entries) ? <DiffView entries={s.entries} /> : <span className="muted">No diff recorded for this change.</span>}
      <div className="tl-card-actions">
        <button type="button" onClick={onRewind}>
          Rewind to just before
        </button>
      </div>
    </section>
  );
}

/** What a mark or an incident was, on hover. */
function HoverCard({ hover, value }: { hover: NonNullable<Hover>; value: number }) {
  const side = sideOf(hover.left);
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
        <span className="muted">Click to see the change</span>
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
