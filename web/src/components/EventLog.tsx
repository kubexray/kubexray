import type { ClusterEvent } from "../lib/events";
import { clock } from "../lib/format";

/** "What happened": changes since kubexray started, newest first. */
export function EventLog({ events, at }: { events: ClusterEvent[]; at: string | null }) {
  const cut = at ? Date.parse(at) : Infinity;
  return (
    <section className="panel log">
      <h2>What happened</h2>
      {events.length === 0 ? (
        <p className="empty">Changes will appear here as they happen.</p>
      ) : (
        <ol>
          {[...events].reverse().map((e) => (
            <li key={e.id} className={Date.parse(e.ts) > cut ? "future" : ""}>
              <time dateTime={e.ts}>{clock(e.ts)}</time>
              <span className={e.kind}>{e.text}</span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
