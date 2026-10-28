// What the "What happened" log shows: events filtered, narrowed to the
// selected app, and bursts of pod churn (a rollout) folded into one row.
import type { ClusterEvent } from "./events";

export type LogFilter = "all" | "problems" | "changes";

export type LogRow = { type: "event"; event: ClusterEvent } | { type: "group"; id: string; ts: string; app: string; label: string; what: string; events: ClusterEvent[] };

/** An app narrowing the log: its events, and those of what it connects to. */
export interface Scope {
  app: string;
  related: ReadonlySet<string>;
}

const BURST = 60_000; // events this close together, on one app, are one burst
const FOLD = 3; // a burst this long is folded

export const inScope = (e: ClusterEvent, scope: Scope | null) => !scope || e.app === scope.app || (!!e.object && scope.related.has(e.object));

const passes = (e: ClusterEvent, f: LogFilter) => f === "all" || (f === "problems" ? e.kind === "crit" : e.kind === "change");

/** How many events each filter shows, in scope. */
export function logCounts(events: ClusterEvent[], scope: Scope | null): Record<LogFilter, number> {
  const scoped = events.filter((e) => inScope(e, scope));
  return { all: scoped.length, problems: scoped.filter((e) => e.kind === "crit").length, changes: scoped.filter((e) => e.kind === "change").length };
}

/** "Deployment/shop/api" → "Deployment api". */
export const labelOf = (id: string) => `${id.slice(0, id.indexOf("/"))} ${id.slice(id.lastIndexOf("/") + 1)}`;

/** The rows to show, newest first. Status events (not changes, not
 * problems) on one app in quick succession fold into a group. */
export function logRows(events: ClusterEvent[], filter: LogFilter, scope: Scope | null): LogRow[] {
  const shown = events.filter((e) => inScope(e, scope) && passes(e, filter)).sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts));
  const rows: LogRow[] = [];
  let run: ClusterEvent[] = [];
  const flush = () => {
    if (run.length >= FOLD && run[0]?.app) rows.push(group(run));
    else rows.push(...run.map((event) => ({ type: "event" as const, event })));
    run = [];
  };
  for (const e of shown) {
    const last = run[run.length - 1];
    const joins = e.kind === "info" && !!e.app && !!last && last.app === e.app && Date.parse(last.ts) - Date.parse(e.ts) <= BURST;
    if (!joins) flush();
    if (e.kind === "info" && e.app) run.push(e);
    else rows.push({ type: "event", event: e });
  }
  flush();
  return rows;
}

function group(events: ClusterEvent[]): LogRow {
  const first = events[0] as ClusterEvent;
  const app = first.app as string;
  const started = events.filter((e) => e.what === "started").length;
  const removed = events.filter((e) => e.what === "removed").length;
  const other = events.length - started - removed;
  const parts = [started && `${started} started`, removed && `${removed} removed`, other && `${other} status change${other === 1 ? "" : "s"}`].filter(Boolean);
  return { type: "group", id: `g${first.id}`, ts: first.ts, app, label: labelOf(app), what: `${events.length} events: ${parts.join(", ")}`, events };
}
