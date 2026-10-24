// The change feed's view logic (ADR-0012): filters, search, the summary
// cards and the hourly strip. The engine (Go) builds and judges the sets;
// this only arranges them.
import type { Set as ChangeSet } from "../gen/feed";
import { hm, who } from "./format";
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
  return who(s.by) || (s.entries.some((e) => e.what === "replicas") ? "an autoscaler or kubectl scale" : "Kubernetes");
}

export const hasDrift = (s: ChangeSet) => s.entries.some((e) => !!e.drift);

/** The name in a node ID: "Deployment/shop/api" → "api". */
export const shortName = (id: string) => id.slice(id.lastIndexOf("/") + 1);

const VERDICT: Record<string, string> = {
  healthy: "Healthy",
  fixed: "Fixed it",
  settling: "Settling",
  stuck: "Didn't settle",
  none: "No effect",
  "": "Judging…",
};

export function verdictLabel(s: ChangeSet): string {
  if (s.verdict === "broke") return `Broke ${s.incident ? shortName(s.incident) : "an app"}`;
  return VERDICT[s.verdict] ?? s.verdict;
}

/** The verdict's note, with when an incident it caused ended. */
export function verdictNote(s: ChangeSet): string {
  return [s.note, s.ended ? `over at ${hm(s.ended)}` : ""].filter(Boolean).join(" · ");
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

export function applyFilter(sets: ChangeSet[], filter: Filter, query: string): ChangeSet[] {
  return sets.filter((s) => tests[filter](s) && matches(s, query));
}

export function filterCounts(sets: ChangeSet[], query: string): Record<Filter, number> {
  const found = sets.filter((s) => matches(s, query));
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

export interface Hour {
  start: number;
  count: number;
  broke: boolean;
  drift: boolean;
}

/** Changes per hour over the last day, oldest first, ending with the current hour. */
export function hourly(sets: ChangeSet[], now: number): Hour[] {
  const hour = 3_600_000;
  const end = Math.floor(now / hour) * hour;
  const hours: Hour[] = Array.from({ length: 24 }, (_, i) => ({ start: end - (23 - i) * hour, count: 0, broke: false, drift: false }));
  for (const s of sets) {
    const i = 23 - Math.floor((end - Math.floor(Date.parse(s.at) / hour) * hour) / hour);
    const h = hours[i];
    if (!h) continue;
    h.count++;
    h.broke ||= s.verdict === "broke";
    h.drift ||= hasDrift(s);
  }
  return hours;
}

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
