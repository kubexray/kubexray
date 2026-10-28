// The timeline's picture of a window of history (the redesign of the rewind
// bar): incidents as spans that are broken until the fix and recovering
// after it, the changes that broke and fixed them as marks, and every other
// change as bars of activity. Live and replays share it.
import type { Incident } from "../gen/cause";
import type { Set as ChangeSet } from "../gen/feed";
import { stateText } from "../components/IncidentCard";

/** An incident: broken from start, fixed at fix, healthy again at end. No
 * end: still going at the window's edge. */
export interface Span {
  app: string;
  label: string;
  start: number;
  fix?: number;
  end?: number;
  cause?: ChangeSet;
}

/** A change that broke something, or fixed it. */
export interface Mark {
  at: number;
  kind: "cause" | "fix";
  label: string;
  set: ChangeSet;
}

/** How many objects changed in a slice of time, apart from the marks. */
export interface Bar {
  start: number;
  count: number;
}

export interface Model {
  spans: Span[];
  marks: Mark[];
  bars: Bar[];
  bucket: number; // ms per bar
}

const MINUTE = 60_000;
const BUCKETS = [1, 5, 15, 60, 180, 360].map((m) => m * MINUTE);

/** The bar width for a window: about 60 bars across, at a round size. */
export function bucketFor(span: number): number {
  return BUCKETS.find((b) => span / b <= 90) ?? 360 * MINUTE;
}

const ms = (ts?: string) => (ts ? Date.parse(ts) : NaN);

/** "LOG_LEVEL → verbose", "targetPort → 8080": the value a change set,
 * from its diff, or its title. */
export function markLabel(s: ChangeSet): string {
  for (const e of s.entries) {
    const l = e.diff?.find((d) => d.to);
    if (l?.to) {
      const path = l.path.replace(/^env /, "").split(/[.[\]]+/).filter(Boolean).pop() ?? l.path;
      return clip(`${path} → ${l.to}`);
    }
  }
  return clip(s.title);
}

const clip = (s: string, n = 26) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

/** "checkout is degraded 59s later" → "checkout is degraded". */
const incidentLabel = (s: ChangeSet) => (s.note ?? s.incident ?? "").replace(/(?: \d+[hms])+ later$/, "");

/** The window's model: from and to in ms, edge the time incidents still
 * going are drawn to (now, or the moment shown). */
export function model(sets: ChangeSet[], current: Incident[], from: number, to: number, edge: number): Model {
  const bucket = bucketFor(to - from);
  const spans: Span[] = [];
  const marks: Mark[] = [];
  const special = new Set<string>();

  const fixes = sets.filter((s) => s.verdict === "fixed" && s.fixes?.length).sort((a, b) => ms(a.at) - ms(b.at));
  for (const s of sets) {
    if (s.verdict !== "broke" || !s.incident) continue;
    const start = ms(s.broke) || ms(s.at);
    const end = s.ended ? ms(s.ended) : undefined;
    const fix = fixes.find((f) => f.fixes?.includes(s.incident ?? "") && ms(f.at) >= start && (end === undefined || ms(f.at) <= end));
    spans.push({ app: s.incident, label: incidentLabel(s), start, fix: fix ? ms(fix.at) : undefined, end, cause: s });
    marks.push({ at: ms(s.at), kind: "cause", label: markLabel(s), set: s });
    special.add(s.id);
    if (fix && !special.has(fix.id)) {
      marks.push({ at: ms(fix.at), kind: "fix", label: "fixed", set: fix });
      special.add(fix.id);
    }
  }
  // Incidents going on now that no change in the feed explains.
  for (const inc of current) {
    if (inc.healthy || !inc.since) continue;
    const since = ms(inc.since);
    if (spans.some((s) => s.app === inc.app && s.start <= since + MINUTE && (s.end === undefined || s.end >= edge))) continue;
    spans.push({ app: inc.app, label: `${inc.name} ${stateText(inc)}`, start: since });
  }

  const counts = new Map<number, number>();
  for (const s of sets) {
    if (special.has(s.id)) continue;
    const at = ms(s.at);
    if (at < from || at > to) continue;
    const b = Math.floor((at - from) / bucket);
    counts.set(b, (counts.get(b) ?? 0) + s.entries.length);
  }
  const bars: Bar[] = [];
  for (let b = 0; from + b * bucket < to; b++) bars.push({ start: from + b * bucket, count: counts.get(b) ?? 0 });
  // Bars are drawn across the window evenly; a short recording leaves the
  // first ones empty rather than stretching the rest.

  return {
    spans: spans.filter((s) => (s.end ?? edge) >= from && s.start <= to),
    marks: marks.filter((m) => m.at >= from && m.at <= to),
    bars,
    bucket,
  };
}

/** Moments to step between: every change and every incident's start. */
export function moments(sets: ChangeSet[], spans: Span[]): number[] {
  const all = [...sets.map((s) => ms(s.at)), ...spans.map((s) => s.start)].filter((t) => !Number.isNaN(t));
  return [...new Set(all)].sort((a, b) => a - b);
}

/** The moment before or after t, or undefined. */
export function step(all: number[], t: number, dir: -1 | 1): number | undefined {
  if (dir > 0) return all.find((m) => m > t + 999);
  return [...all].reverse().find((m) => m < t - 999);
}

const TICKS = [1, 2, 5, 10, 15, 30, 60, 120, 180, 360, 720, 1440].map((m) => m * MINUTE);

/** Round times for the axis, at most about eight. */
export function ticks(from: number, to: number): number[] {
  const every = TICKS.find((t) => (to - from) / t <= 8) ?? 1440 * MINUTE;
  const out: number[] = [];
  for (let t = Math.ceil(from / every) * every; t <= to; t += every) out.push(t);
  return out;
}

/** The live window for a range: the last hour, day or week, within what was
 * recorded. */
export const RANGES = { hour: 60 * MINUTE, day: 24 * 60 * MINUTE, week: 7 * 24 * 60 * MINUTE } as const;
export type Range = keyof typeof RANGES;

/** The window shown: a replay (range null) all of it; live a whole hour, day
 * or week ending now, at the same scale however long kubexray has been
 * recording. A moment rewound to outside it brings the window along. */
export function windowFor(range: number | null, first: number, last: number, value: number): [number, number] {
  if (range === null) return [first, last];
  const from = last - range;
  if (value >= from) return [from, last];
  return [value - range / 2, value + range / 2];
}
