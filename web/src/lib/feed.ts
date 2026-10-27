// The change feed's view logic (ADR-0012): filters, search, the summary
// cards and the histogram. The engine (Go) builds and judges the sets;
// this only arranges them.
import type { Set as ChangeSet, DiffLine } from "../gen/feed";
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
  if (s.entries.some((e) => e.what === "replicas")) return "an autoscaler or kubectl scale";
  // An edit to an object Kubernetes keeps no record of writes for.
  if (s.entries.some((e) => e.what === "changed")) return "someone (Kubernetes didn't record who)";
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

/** Whether a change broke something that has since been fixed. */
export const resolved = (s: ChangeSet) => s.verdict === "broke" && !!s.ended;

/** The look of a set's card and badge: a resolved breakage is green, like
 * anything healthy, so only what's still broken is red. */
export function tone(s: ChangeSet): string {
  return resolved(s) ? "resolved" : s.verdict || "judging";
}

export function verdictLabel(s: ChangeSet): string {
  if (resolved(s)) return "Resolved";
  if (s.verdict === "broke") return "Broke something"; // the note names the app
  return VERDICT[s.verdict] ?? s.verdict;
}

/** The verdict's note; a resolved one says what broke, for how long and
 * when it was fixed. */
export function verdictNote(s: ChangeSet): string {
  if (resolved(s) && s.ended) {
    const app = s.incident ? shortName(s.incident) : "an app";
    return `broke ${app} for ${elapsed(Date.parse(s.ended) - Date.parse(s.at))} · resolved at ${hm(s.ended)}`;
  }
  return s.note ?? "";
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
  return (best?.lines ?? []).filter((l) => l.path !== "replicas" && l.path !== "release version");
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

/** The map narrowed to apps and Services whose names match a search. */
export function searchView(view: NamespaceView[], query: string): NamespaceView[] {
  const q = query.trim().toLowerCase();
  if (!q) return view;
  return view
    .map((ns) => {
      if (ns.name.toLowerCase().includes(q)) return ns;
      const rows = ns.rows.filter((r) => [r.workload?.node.name, ...r.services.map((s) => s.name), ...r.ingresses.map((i) => i.name)].some((n) => n?.toLowerCase().includes(q)));
      return { ...ns, rows };
    })
    .filter((ns) => ns.rows.length > 0);
}
