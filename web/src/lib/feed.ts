// The change feed's view logic (ADR-0012): filters, search, the summary
// cards and the histogram. The engine (Go) builds and judges the sets;
// this only arranges them.
import type { Set as ChangeSet, DiffLine, Entry } from "../gen/feed";
import { elapsed, hm, who } from "./format";
import type { NamespaceView } from "./view";

export type Filter = "all" | "people" | "broke" | "drift" | "settling";

export const FILTERS: { id: Filter; label: string }[] = [
  { id: "all", label: "All changes" },
  { id: "people", label: "People and tools" },
  { id: "broke", label: "Broke something" },
  { id: "drift", label: "Drift" },
  { id: "settling", label: "Still settling" },
];

/** Who made a set, as the UI says it. */
export function author(s: ChangeSet): string {
  if (who(s.by)) return who(s.by);
  // Neither kubectl scale nor an autoscaler leaves a write record.
  if (s.entries.some((e) => e.what === "replicas")) return "someone (Kubernetes doesn't record who scales)";
  // An edit Kubernetes keeps no record of: an object without write records,
  // or a kubectl replace, which drops the previous writer's.
  if (s.entries.some((e) => e.what === "changed" || e.what === "template")) return "someone (Kubernetes didn't record who)";
  return "Kubernetes";
}

export const hasDrift = (s: ChangeSet) => s.entries.some((e) => !!e.drift);

/** The name in a node ID: "Deployment/shop/api" → "api". */
export const shortName = (id: string) => id.slice(id.lastIndexOf("/") + 1);

const VERDICT: Record<string, string> = {
  healthy: "Healthy",
  fixed: "Fixed an incident",
  settling: "Settling",
  stuck: "Didn't settle",
  none: "No effect",
  "": "Judging…",
};

/** Whether a change broke something that had been fixed by `now` (a
 * replay's playhead: what's fixed later isn't known yet). */
export const resolved = (s: ChangeSet, now = Infinity) => s.verdict === "broke" && !!s.ended && Date.parse(s.ended) <= now;

/** The look of a set's card and badge: red while what it broke is still
 * broken, neutral once fixed (it still broke something: not good news). */
export function tone(s: ChangeSet, now = Infinity): string {
  if (resolved(s, now)) return "resolved";
  if (s.verdict === "fixed" && Date.parse(s.at) > now) return "judging";
  return s.verdict || "judging";
}

/** A length of time, as said in a sentence: "45s", "3m", "2h 5m". */
export function roughly(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return elapsed(ms);
}

export function verdictLabel(s: ChangeSet): string {
  if (s.verdict === "broke") return s.incident ? `Broke ${shortName(s.incident)}` : "Broke something";
  return VERDICT[s.verdict] ?? s.verdict;
}

/** The verdict's note; a breakage fixed since says when: "fixed 3m later
 * · at 13:28". Still broken at `now`, the engine's note ("checkout is
 * degraded 59s later"). */
export function verdictNote(s: ChangeSet, now = Infinity): string {
  if (resolved(s, now) && s.ended) return `fixed ${roughly(Date.parse(s.ended) - Date.parse(s.at))} later · at ${hm(s.ended)}`;
  return s.note ?? "";
}

/** The labels Helm and charts bump on every release: the set's title
 * already says the version, so they're noise in its details and diffs. */
const VERSION_LABEL = /^metadata\.labels\["(helm\.sh\/chart|app\.kubernetes\.io\/version)"\]$/;
export const versionLabel = (l: DiffLine) => VERSION_LABEL.test(l.path) || l.path === "release version";

/** The diff lines worth showing: version label bumps left out. */
export const meaningful = (lines: DiffLine[] | undefined) => (lines ?? []).filter((l) => !versionLabel(l));

/** An entry that only bumped version labels (a Service in a Helm upgrade). */
export const labelOnly = (e: Entry) => (e.diff?.length ?? 0) > 0 && meaningful(e.diff).length === 0 && e.what === "changed";

/** A set's entries worth listing: label-only bumps left out, unless that's
 * all there is. */
export function shownEntries(s: ChangeSet): Entry[] {
  const kept = s.entries.filter((e) => !labelOnly(e));
  return kept.length ? kept : s.entries;
}

/** How far a cause's time may be from the feed entry that recorded it: the
 * write's time, and when kubexray saw it. */
const SAME_CHANGE_MS = 10_000;

/** The diff of the change a cause names, from the feed (ADR-0017): what a
 * ConfigMap edit or a rollout changed, value by value. Empty when the feed
 * has no diff for it. Display only: alerts and post-mortems are written
 * from the cause, without values. */
export function causeDiff(sets: ChangeSet[], cause: { object: string; at?: string }): DiffLine[] {
  if (!cause.at) return [];
  const at = Date.parse(cause.at);
  let best: { lines: DiffLine[]; off: number } | null = null;
  for (const s of sets) {
    for (const e of s.entries) {
      if (e.object !== cause.object || !e.diff?.length) continue;
      const off = Math.abs(Date.parse(e.at) - at);
      if (off <= SAME_CHANGE_MS && (!best || off < best.off)) best = { lines: e.diff, off };
    }
  }
  return meaningful(best?.lines).filter((l) => l.path !== "replicas");
}

/** The moment just before a set, for rewinding to it. */
export const justBefore = (s: ChangeSet) => new Date(Date.parse(s.at) - 1000).toISOString();

/** Made by a person's tool or a deploy tool, not a controller (autoscalers, nodes). */
export const byPeople = (s: ChangeSet) => !!s.by && s.by.tool !== "controller";

const tests: Record<Filter, (s: ChangeSet) => boolean> = {
  all: () => true,
  people: byPeople,
  broke: (s) => s.verdict === "broke",
  drift: hasDrift,
  settling: (s) => s.verdict === "settling" || s.verdict === "",
};

/** Whether a set mentions every word of a search. */
export function matches(s: ChangeSet, query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return true;
  const text = [s.title, s.namespace ?? "", author(s), ...(s.apps ?? []).map(shortName), ...s.entries.flatMap((e) => [e.kind, e.name, e.summary])].join(" ").toLowerCase();
  return words.every((w) => text.includes(w));
}

export function applyFilter(sets: ChangeSet[], filter: Filter, query: string, window: Window | null = null): ChangeSet[] {
  return sets.filter((s) => tests[filter](s) && matches(s, query) && inWindow(s, window));
}

export function filterCounts(sets: ChangeSet[], query: string, window: Window | null = null): Record<Filter, number> {
  const found = sets.filter((s) => matches(s, query) && inWindow(s, window));
  return Object.fromEntries(FILTERS.map(({ id }) => [id, found.filter(tests[id]).length])) as Record<Filter, number>;
}

export interface Summary {
  changes: number;
  authors: number;
  broke: number;
  healthy: number;
  settling: number;
  drift: number;
}

/** The numbers on the summary cards, for sets since a time. */
export function summarize(sets: ChangeSet[], since: number): Summary {
  const recent = sets.filter((s) => Date.parse(s.last) >= since);
  return {
    changes: recent.length,
    authors: new Set(recent.map(author)).size,
    broke: recent.filter((s) => s.verdict === "broke").length,
    healthy: recent.filter((s) => s.verdict === "healthy" || s.verdict === "fixed").length,
    settling: recent.filter(tests.settling).length,
    drift: recent.filter(hasDrift).length,
  };
}

export type Range = "1h" | "24h" | "7d";

/** The histogram's ranges, each split into about fifty bars. */
export const RANGES: { id: Range; label: string; span: number; bar: number }[] = [
  { id: "1h", label: "Hour", span: 3_600_000, bar: 60_000 },
  { id: "24h", label: "Day", span: 86_400_000, bar: 1_800_000 },
  { id: "7d", label: "Week", span: 604_800_000, bar: 14_400_000 },
];

export function rangeOf(range: Range): { span: number; bar: number } {
  return RANGES.find((x) => x.id === range) ?? { span: 86_400_000, bar: 1_800_000 };
}

/** The smallest range that shows every change, so a short recording gets
 * minute bars rather than one tall one. */
export function fittingRange(sets: ChangeSet[], now: number): Range {
  const oldest = Math.min(...sets.map((s) => Date.parse(s.at)).filter(Number.isFinite));
  if (!Number.isFinite(oldest)) return "24h";
  return RANGES.find((r) => now - oldest <= r.span)?.id ?? "7d";
}

/** The time a range covers, ending with the bar now is in. */
export function rangeWindow(range: Range, now: number): Window {
  const r = rangeOf(range);
  const end = Math.floor(now / r.bar) * r.bar + r.bar;
  return [end - r.span, end];
}

/** What a set did, for the histogram's colors: the most notable first. */
export type Outcome = "broke" | "drift" | "settling" | "healthy" | "other";

export function outcome(s: ChangeSet): Outcome {
  if (s.verdict === "broke") return "broke";
  if (hasDrift(s)) return "drift";
  if (s.verdict === "settling" || s.verdict === "") return "settling";
  if (s.verdict === "healthy" || s.verdict === "fixed") return "healthy";
  return "other";
}

export interface Bucket {
  start: number;
  end: number;
  sets: ChangeSet[]; // newest first, like the feed
  counts: Record<Outcome, number>;
}

/** The histogram's bars for a range ending now, oldest first. */
export function buckets(sets: ChangeSet[], range: Range, now: number): Bucket[] {
  const r = rangeOf(range);
  const end = Math.floor(now / r.bar) * r.bar + r.bar; // the bar now is in is the last
  const n = Math.round(r.span / r.bar);
  const first = end - n * r.bar;
  const out: Bucket[] = Array.from({ length: n }, (_, i) => ({
    start: end - (n - i) * r.bar,
    end: end - (n - i - 1) * r.bar,
    sets: [],
    counts: { broke: 0, drift: 0, settling: 0, healthy: 0, other: 0 },
  }));
  for (const s of sets) {
    const b = out[Math.floor((Date.parse(s.at) - first) / r.bar)];
    if (!b) continue;
    b.sets.push(s);
    b.counts[outcome(s)]++;
  }
  return out;
}

/** A time window picked on the histogram: [from, to). */
export type Window = [from: number, to: number];

export const inWindow = (s: ChangeSet, w: Window | null) => !w || (Date.parse(s.at) >= w[0] && Date.parse(s.at) < w[1]);

/** The map narrowed to what matches a search: namespaces, apps, Services
 * and Ingresses by name, and apps by the images they run. */
export function searchView(view: NamespaceView[], query: string): NamespaceView[] {
  const q = query.trim().toLowerCase();
  if (!q) return view;
  const hit = (s: string | undefined) => !!s?.toLowerCase().includes(q);
  return view
    .map((ns) => {
      if (hit(ns.name)) return ns;
      const rows = ns.rows.filter((r) => {
        const n = r.workload?.node;
        return hit(n?.name) || (n?.spec?.containers ?? []).some((c) => hit(c.image)) || r.services.some((s) => hit(s.name)) || r.ingresses.some((i) => hit(i.name));
      });
      return { ...ns, rows };
    })
    .filter((ns) => ns.rows.length > 0);
}
