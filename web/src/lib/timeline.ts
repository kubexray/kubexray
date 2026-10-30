// The timeline's picture of a window of history (the redesign of the rewind
// bar): incidents as spans that are broken until the fix and recovering
// after it, the changes that broke and fixed them as marks, and every other
// change as bars of activity (the marks too: each is a change). Live and
// replays share it.
import type { Incident } from "../gen/cause";
import type { Set as ChangeSet } from "../gen/feed";
import { stateText } from "../components/IncidentCard";
import { shortName } from "./feed";
import { chartChange, type ReleaseRevision, revisionSet } from "./helm";

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

/** A Helm revision: which, its chart move, and the earlier revision whose
 * chart it went back to, which a rollback does. */
export interface RevisionInfo {
  release: string;
  ns: string;
  version: number;
  status: string;
  chart?: { from?: string; to?: string };
  back?: number;
}

/** A change that broke something, or fixed it; a Helm revision's, if one. */
export interface Mark {
  at: number;
  kind: "cause" | "fix";
  label: string;
  set: ChangeSet;
  helm?: RevisionInfo;
}

/** A Helm install, upgrade or rollback that broke and fixed nothing. Set is
 * what it changed, when kubexray was recording. */
export interface HelmMark {
  at: number;
  label: string;
  rev: RevisionInfo;
  set?: ChangeSet;
}

/** How many objects changed in a slice of time, the marks' changes included. */
export interface Bar {
  start: number;
  count: number;
}

export interface Model {
  spans: Span[];
  marks: Mark[];
  helm: HelmMark[];
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
    if (l?.to) return clip(`${lastKey(l.path)} → ${l.to}`);
  }
  return clip(s.title);
}

/** The last key of a diff path, as people call it: "spec.ports[0].targetPort"
 * → "targetPort", 'metadata.labels["app.kubernetes.io/version"]' →
 * "version", "env LOG_LEVEL" → "LOG_LEVEL". */
export function lastKey(path: string): string {
  const quoted = /\["([^"]*)"\]$/.exec(path);
  if (quoted?.[1]) return quoted[1].split(/[/.]/).pop() || quoted[1];
  return path.replace(/^env /, "").split(/[.[\]]+/).filter(Boolean).pop() ?? path;
}

const clip = (s: string, n = 26) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

/** "checkout is degraded 59s later" → "checkout is degraded". */
const incidentLabel = (s: ChangeSet) => (s.note ?? s.incident ?? "").replace(/(?: \d+[hms])+ later$/, "");

/** The window's model: from and to in ms, edge the time incidents still
 * going are drawn to (now, or the moment shown). */
export function model(sets: ChangeSet[], current: Incident[], from: number, to: number, edge: number, revs: ReleaseRevision[] = []): Model {
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
  // One incident, one area: changes credited with the same incident (two
  // edits at once, say) would draw it twice. The first cause names it; the
  // others are ordinary activity.
  merge(spans, marks);
  // Fixes of incidents whose cause isn't in the feed: one that began before
  // kubexray started recording, say. Drawn from when it began, which the
  // fix noted, unless an area already covers it.
  for (const f of fixes) {
    if (special.has(f.id)) continue;
    const at = ms(f.at);
    const apps = (f.fixes ?? []).filter((app) => !spans.some((s) => s.app === app && s.start <= at && (s.end ?? Infinity) >= at));
    if (!apps.length) continue;
    for (const app of apps) spans.push({ app, label: `${shortName(app)} was broken`, start: ms(f.broke) || at, fix: at, end: f.ended ? ms(f.ended) : undefined });
    marks.push({ at, kind: "fix", label: "fixed", set: f });
    special.add(f.id);
  }
  // Incidents going on now that no change in the feed explains.
  for (const inc of current) {
    if (inc.healthy || !inc.since) continue;
    const since = ms(inc.since);
    if (spans.some((s) => s.app === inc.app && s.start <= since + MINUTE && (s.end === undefined || s.end >= edge))) continue;
    spans.push({ app: inc.app, label: `${inc.name} ${stateText(inc)}`, start: since });
  }

  // Helm revisions: on the mark of the change they made when it broke or
  // fixed something, else marks of their own.
  const helm: HelmMark[] = [];
  const charts = new Map<string, Map<string, number>>(); // release → chart version → first revision
  const before = new Map<string, ChangeSet | undefined>(); // release → its previous revision's change
  for (const r of revs) {
    const set = revisionSet(sets, { tool: "helm", name: r.release }, r);
    const chart = set && chartChange(set);
    const key = `${r.ns}/${r.release}`;
    const seen = charts.get(key) ?? new Map<string, number>();
    charts.set(key, seen);
    // Back to an earlier revision: its chart again, or the previous
    // revision's change undone, which rolling back the last upgrade does.
    const prev = before.get(key);
    before.set(key, set);
    const back = chart?.to && chart.to !== chart.from ? seen.get(chart.to) : set && prev && r.version > 2 && undoes(set, prev) ? r.version - 2 : undefined;
    if (chart?.from && !seen.has(chart.from)) seen.set(chart.from, r.version - 1);
    if (chart?.to && !seen.has(chart.to)) seen.set(chart.to, r.version);
    const rev: RevisionInfo = { release: r.release, ns: r.ns, version: r.version, status: r.status, chart, back };
    const mark = set && marks.find((m) => m.set.id === set.id);
    if (mark) mark.helm = rev;
    else helm.push({ at: ms(r.at), label: `${r.release} #${r.version}`, rev, set });
  }

  const counts = new Map<number, number>();
  for (const s of sets) {
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
    helm: helm.filter((h) => h.at >= from && h.at <= to),
    bars,
    bucket,
  };
}

/** Whether change s puts back every value change prev made, and nothing else. */
function undoes(s: ChangeSet, prev: ChangeSet): boolean {
  const lines = (c: ChangeSet) => c.entries.flatMap((e) => (e.diff ?? []).map((d) => ({ ...d, object: e.object })));
  const mine = lines(s);
  const theirs = lines(prev);
  return (
    mine.some((l) => l.from !== undefined || l.to !== undefined) &&
    mine.length === theirs.length &&
    mine.every((l) => theirs.some((t) => t.object === l.object && t.path === l.path && t.from === l.to && t.to === l.from))
  );
}

/** Folds spans of one app that overlap into the first, in place, and drops
 * the cause marks of the ones folded. */
function merge(spans: Span[], marks: Mark[]) {
  spans.sort((a, b) => a.start - b.start);
  for (let i = 0; i < spans.length; i++) {
    const a = spans[i] as Span;
    for (let j = i + 1; j < spans.length; j++) {
      const b = spans[j] as Span;
      if (b.app !== a.app || b.start > (a.end ?? Infinity)) continue;
      a.end = a.end === undefined || b.end === undefined ? undefined : Math.max(a.end, b.end);
      a.fix = b.fix ?? a.fix;
      const k = marks.findIndex((m) => m.kind === "cause" && m.set === b.cause);
      if (k >= 0) marks.splice(k, 1);
      spans.splice(j--, 1);
    }
  }
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

/** Which labels fit without overlapping, newest first: when they crowd, the
 * latest wins, and the others are still a hover away. Each label is the
 * span of the row it would cover, in percent of the width. */
export function keepLabels(labels: { from: number; to: number }[]): boolean[] {
  const order = labels.map((_, i) => i).sort((a, b) => (labels[b]?.from ?? 0) - (labels[a]?.from ?? 0));
  const kept: { from: number; to: number }[] = [];
  const out = labels.map(() => false);
  for (const i of order) {
    const l = labels[i];
    if (!l || kept.some((k) => l.from < k.to + 0.5 && k.from < l.to + 0.5)) continue;
    kept.push(l);
    out[i] = true;
  }
  return out;
}

/** About how much of the width a label of n characters takes, in percent. */
export const labelWidth = (n: number, px: number) => ((n * 6.6 + 14) / Math.max(px, 1)) * 100;
