import type { Incident } from "../gen/cause";
import type { ClusterEvent } from "../lib/events";
import type { Compare } from "./Workspace";
import { clock, duration } from "../lib/format";
import { useNow } from "../lib/useNow";

interface Props {
  bounds: { from: string; to: string } | null;
  /** The moment shown; null means live (live mode only). */
  at: string | null;
  /** Live: the right edge is now and there's a Back to live button.
   * Replay: the right edge is the end of the file and there's Play. */
  mode: "live" | "replay";
  events: ClusterEvent[];
  incidents: Incident[];
  onSeek: (ts: string | null) => void;
  playing?: boolean;
  onPlay?: () => void;
  /** Diff mode (Workspace). */
  compare?: Compare;
}

/** The rewind timeline: drag back through the recorded history. */
export function Timeline({ bounds, at, mode, events, incidents, onSeek, playing, onPlay, compare }: Props) {
  const now = useNow();
  if (!bounds) {
    return (
      <footer className="timeline">
        <span className="hint">Recording… the timeline appears once there's history to rewind.</span>
      </footer>
    );
  }
  const from = Date.parse(bounds.from);
  const to = mode === "live" ? Math.max(now, Date.parse(bounds.to)) : Date.parse(bounds.to);
  const span = Math.max(1, to - from);
  const pos = (ts: string) => `${Math.min(100, Math.max(0, ((Date.parse(ts) - from) / span) * 100))}%`;
  const value = at ? Date.parse(at) : to;

  return (
    <footer className="timeline">
      {mode === "live" ? (
        <button type="button" className={`live ${at ? "" : "on"}`} onClick={() => onSeek(null)} disabled={!at}>
          {at ? "● Back to live" : "● Live"}
        </button>
      ) : (
        <button type="button" className="live" onClick={onPlay}>
          {playing ? "❚❚ Pause" : "▶ Play"}
        </button>
      )}
      <div className="track">
        <div className="ticks" aria-hidden="true">
          {incidents.map((inc) => (inc.since ? <span key={inc.app} className="band" style={{ left: pos(inc.since), right: 0 }} /> : null))}
          {events
            .filter((e) => e.kind !== "info")
            .map((e) => (
              <i key={e.id} className={e.kind} style={{ left: pos(e.ts) }} title={`${clock(e.ts)} ${e.text}`} />
            ))}
          {compare?.from && Date.parse(compare.from) >= from && <b className="mark" style={{ left: pos(compare.from) }} title={`Comparing from ${clock(compare.from)}`} />}
        </div>
        <input
          type="range"
          aria-label={mode === "live" ? "Rewind the cluster" : "Move through the replay"}
          min={from}
          max={to}
          step={1000}
          value={value}
          onChange={(e) => {
            const t = Number(e.target.value);
            onSeek(mode === "live" && t >= to - 1000 ? null : new Date(t).toISOString());
          }}
        />
      </div>
      {compare && (
        <button type="button" className={`compare${compare.from ? " on" : ""}`} onClick={compare.toggle} aria-pressed={!!compare.from} disabled={compare.disabled}>
          {compare.from ? "Stop comparing" : "Compare"}
        </button>
      )}
      <span className="clock">
        {mode === "live"
          ? at
            ? `${clock(at)} · rewound`
            : `${clock(new Date(now).toISOString())} · live`
          : `${clock(new Date(value).toISOString())} · ${duration(bounds.from, value)} of ${duration(bounds.from, to)}`}
      </span>
    </footer>
  );
}
