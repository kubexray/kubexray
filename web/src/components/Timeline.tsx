import type { Incident } from "../gen/cause";
import type { HistoryResponse } from "../gen/server";
import type { ClusterEvent } from "../lib/events";
import { clock } from "../lib/format";

interface Props {
  history: HistoryResponse | null;
  at: string | null; // null: live
  now: number;
  events: ClusterEvent[];
  incidents: Incident[];
  onSeek: (ts: string | null) => void;
}

/** The rewind timeline: drag back through the recorded history. */
export function Timeline({ history, at, now, events, incidents, onSeek }: Props) {
  if (!history) {
    return (
      <footer className="timeline">
        <span className="hint">Recording… the timeline appears once there's history to rewind.</span>
      </footer>
    );
  }
  const from = Date.parse(history.from);
  const to = Math.max(now, Date.parse(history.to));
  const span = Math.max(1, to - from);
  const pos = (ts: string) => `${Math.min(100, Math.max(0, ((Date.parse(ts) - from) / span) * 100))}%`;
  const value = at ? Date.parse(at) : to;

  return (
    <footer className="timeline">
      <button type="button" className={`live ${at ? "" : "on"}`} onClick={() => onSeek(null)} disabled={!at}>
        {at ? "● Back to live" : "● Live"}
      </button>
      <div className="track">
        <div className="ticks" aria-hidden="true">
          {incidents.map((inc) =>
            inc.since ? (
              <span key={inc.app} className="band" style={{ left: pos(inc.since), right: 0 }} />
            ) : null,
          )}
          {events
            .filter((e) => e.kind !== "info")
            .map((e) => (
              <i key={e.id} className={e.kind} style={{ left: pos(e.ts) }} title={`${clock(e.ts)} ${e.text}`} />
            ))}
        </div>
        <input
          type="range"
          aria-label="Rewind the cluster"
          min={from}
          max={to}
          step={1000}
          value={value}
          onChange={(e) => {
            const t = Number(e.target.value);
            onSeek(t >= to - 1000 ? null : new Date(t).toISOString());
          }}
        />
      </div>
      <span className="clock">{at ? `${clock(at)} · rewound` : `${clock(new Date(now).toISOString())} · live`}</span>
    </footer>
  );
}
