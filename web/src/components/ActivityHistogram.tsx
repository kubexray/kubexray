import { useEffect, useMemo, useState } from "react";
import type { Set as ChangeSet } from "../gen/feed";
import { author, buckets, type Bucket, type Outcome, outcome, type Range, RANGES, type Window } from "../lib/feed";
import { hm } from "../lib/format";

interface Props {
  sets: ChangeSet[];
  now: number;
  range: Range;
  onRange: (r: Range) => void;
  /** The time picked by clicking or dragging across bars. */
  picked: Window | null;
  onPick: (w: Window | null) => void;
}

const ORDER: Outcome[] = ["broke", "drift", "settling", "healthy", "other"];
const LABEL: Record<Outcome, string> = { broke: "Broke something", drift: "Drift", settling: "Settling", healthy: "Healthy", other: "Other" };
const TIPS = 5; // changes listed in a bar's tooltip

function when(t: number, range: Range): string {
  const iso = new Date(t).toISOString();
  return range === "7d" ? `${new Date(t).toLocaleDateString([], { weekday: "short" })} ${hm(iso)}` : hm(iso);
}

/** Changes over time, Kibana style: hover a bar to see its changes, click it
 * to show only those, drag across bars to pick a range. */
export function ActivityHistogram({ sets, now, range, onRange, picked, onPick }: Props) {
  const bars = useMemo(() => buckets(sets, range, now), [sets, range, now]);
  const [hover, setHover] = useState<number | null>(null);
  const [drag, setDrag] = useState<{ from: number; to: number } | null>(null);
  const peak = Math.max(1, ...bars.map((b) => b.sets.length));
  const total = bars.reduce((n, b) => n + b.sets.length, 0);

  const inPick = (i: number, b: Bucket) =>
    drag ? i >= Math.min(drag.from, drag.to) && i <= Math.max(drag.from, drag.to) : !!picked && b.start < picked[1] && b.end > picked[0];
  // A drag ends wherever the button is released, inside the plot or not.
  useEffect(() => {
    if (!drag) return;
    const finish = () => {
      const lo = bars[Math.min(drag.from, drag.to)];
      const hi = bars[Math.max(drag.from, drag.to)];
      setDrag(null);
      if (lo && hi) onPick([lo.start, hi.end]);
    };
    addEventListener("pointerup", finish);
    return () => removeEventListener("pointerup", finish);
  }, [drag, bars, onPick]);
  const ticks = [0, Math.floor(bars.length / 4), Math.floor(bars.length / 2), Math.floor((bars.length * 3) / 4)];
  const tip = hover !== null && !drag ? bars[hover] : undefined;

  return (
    <section className="hist" aria-label="Changes over time">
      <div className="hist-head">
        <div className="hist-title">
          <span>
            {total} change{total === 1 ? "" : "s"} in the last {RANGES.find((r) => r.id === range)?.label.toLowerCase()}
          </span>
          {picked && (
            <span className="hist-window">
              Showing {when(picked[0], range)} – {when(picked[1], range)}
              <button type="button" onClick={() => onPick(null)}>
                Clear
              </button>
            </span>
          )}
        </div>
        <div className="hist-controls">
          <span className="feed-legend">
            {ORDER.slice(0, 4).map((o) => (
              <span key={o}>
                <i className={`sw ${o}`} />
                {LABEL[o]}
              </span>
            ))}
          </span>
          <div className="hist-ranges" role="group" aria-label="Range">
            {RANGES.map((r) => (
              <button
                key={r.id}
                type="button"
                aria-pressed={range === r.id}
                className={range === r.id ? "on" : ""}
                onClick={() => {
                  onRange(r.id);
                  onPick(null);
                }}
              >
                {r.label}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="hist-plot" onPointerLeave={() => setHover(null)}>
        <div className={`hist-bars${picked || drag ? " picking" : ""}`}>
          {bars.map((b, i) => (
            <button
              key={b.start}
              type="button"
              className={`hist-bar${inPick(i, b) ? " picked" : ""}`}
              aria-label={`${when(b.start, range)} to ${when(b.end, range)}: ${b.sets.length} change${b.sets.length === 1 ? "" : "s"}`}
              onPointerDown={(e) => {
                if (e.button !== 0) return; // only the main button picks
                e.currentTarget.releasePointerCapture(e.pointerId); // let the drag reach the other bars
                setDrag({ from: i, to: i });
              }}
              onPointerEnter={() => {
                setHover(i);
                if (drag) setDrag({ ...drag, to: i });
              }}
              onFocus={() => setHover(i)}
              onBlur={() => setHover(null)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  onPick([b.start, b.end]);
                }
              }}
            >
              <span className="hist-stack" style={{ height: b.sets.length ? `${Math.max(8, (b.sets.length / peak) * 100)}%` : "2px" }}>
                {ORDER.map((o) => (b.counts[o] ? <span key={o} className={`seg ${o}`} style={{ flexGrow: b.counts[o] }} /> : null))}
              </span>
            </button>
          ))}
        </div>
        {tip && (
          <div
            className={`hist-tip ${(hover ?? 0) < bars.length * 0.25 ? "start" : (hover ?? 0) > bars.length * 0.75 ? "end" : ""}`}
            aria-hidden="true" // the bar's label says it; a live region would announce every hover
            style={{ left: `${((hover ?? 0) + 0.5) * (100 / bars.length)}%` }}
          >
            <div className="hist-tip-time">
              {when(tip.start, range)} – {when(tip.end, range)}
            </div>
            {tip.sets.length === 0 ? (
              <div className="hist-tip-none">No changes</div>
            ) : (
              <ul>
                {tip.sets.slice(0, TIPS).map((s) => (
                  <li key={s.id}>
                    <i className={`sw ${outcome(s)}`} />
                    <span>
                      <strong>{s.title}</strong> · {author(s)}
                    </span>
                  </li>
                ))}
                {tip.sets.length > TIPS && <li className="hist-tip-more">and {tip.sets.length - TIPS} more · click to see them</li>}
              </ul>
            )}
          </div>
        )}
        <div className="hist-axis" aria-hidden="true">
          {ticks.map((i) => (
            <span key={i} style={{ left: `${(i / bars.length) * 100}%` }}>
              {bars[i] ? when(bars[i].start, range) : ""}
            </span>
          ))}
          <span className="end">now</span>
        </div>
      </div>
    </section>
  );
}
