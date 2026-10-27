import { describe, expect, it } from "vitest";
import type { Entry, Set as ChangeSet } from "../gen/feed";
import { applyFilter, author, buckets, causeDiff, filterCounts, fittingRange, matches, searchView, summarize } from "./feed";
import type { NamespaceView } from "./view";

const T = (m: number) => new Date(Date.UTC(2026, 9, 24, 10, m)).toISOString();

const entry = (over: Partial<Entry> = {}): Entry => ({
  at: T(0), object: "ConfigMap/shop/checkout-config", kind: "ConfigMap", name: "checkout-config", namespace: "shop",
  what: "edited", summary: "edited (contents not read)", ...over,
});

const set = (over: Partial<ChangeSet> = {}): ChangeSet => ({
  id: "1", at: T(0), last: T(0), title: "ConfigMap checkout-config edited", namespace: "shop",
  by: { at: T(0), by: "kubectl-patch", tool: "kubectl", via: "patch" }, entries: [entry()], verdict: "healthy", ...over,
});

const sets: ChangeSet[] = [
  set({ id: "a", verdict: "broke", at: T(30), last: T(30) }),
  set({ id: "b", title: "checkout scaled 3 → 5 replicas", by: undefined, entries: [entry({ kind: "Deployment", name: "checkout", what: "replicas", summary: "scaled 3 → 5 replicas" })], at: T(20), last: T(20) }),
  set({ id: "c", title: "Deployment api: resources", entries: [entry({ kind: "Deployment", name: "api", what: "resources", drift: "managed by Helm" })], by: { at: T(10), by: "kubectl-edit", tool: "kubectl", via: "edit" }, at: T(10), last: T(10) }),
  set({ id: "d", title: "Argo CD sync of payments", verdict: "settling", by: { at: T(5), by: "argocd-controller", tool: "argocd" }, namespace: "payments", at: T(5), last: T(5),
    entries: [entry({ object: "Deployment/payments/payments-api", kind: "Deployment", name: "payments-api", namespace: "payments", what: "image", summary: "image 3.2.0 → 3.3.0" })] }),
];

describe("change feed", () => {
  it("names who made a change, even when Kubernetes doesn't record it", () => {
    expect(author(sets[0]!)).toBe("kubectl patch");
    expect(author(sets[1]!)).toBe("an autoscaler or kubectl scale");
    expect(author(sets[3]!)).toBe("argocd");
  });

  it("filters and searches", () => {
    expect(applyFilter(sets, "broke", "").map((s) => s.id)).toEqual(["a"]);
    expect(applyFilter(sets, "drift", "").map((s) => s.id)).toEqual(["c"]);
    expect(applyFilter(sets, "people", "").map((s) => s.id)).toEqual(["a", "c", "d"]);
    expect(applyFilter(sets, "settling", "").map((s) => s.id)).toEqual(["d"]);
    expect(applyFilter(sets, "all", "payments argo").map((s) => s.id)).toEqual(["d"]);
    expect(matches(sets[2]!, "API kubectl")).toBe(true);
    expect(filterCounts(sets, "checkout")).toEqual({ all: 2, people: 1, broke: 1, drift: 0, settling: 0 });
  });

  it("summarizes a day", () => {
    expect(summarize(sets, Date.parse(T(8)))).toEqual({ changes: 3, authors: 3, broke: 1, healthy: 2, settling: 0, drift: 1 });
  });

  it("buckets changes for the histogram, by what they did", () => {
    const bars = buckets(sets, "1h", Date.parse(T(45)));
    expect(bars).toHaveLength(60);
    expect(bars.at(-1)!.start).toBe(Date.parse(T(45))); // the bar now is in comes last
    expect(buckets([set({ at: T(45) })], "1h", Date.parse(T(45))).at(-1)!.sets).toHaveLength(1);
    const at = (m: number) => bars.find((b) => b.start <= Date.parse(T(m)) && Date.parse(T(m)) < b.end)!;
    expect(at(30).counts).toMatchObject({ broke: 1 });
    expect(at(10).counts).toMatchObject({ drift: 1 });
    expect(at(5).counts).toMatchObject({ settling: 1 });
    expect(at(20).sets.map((s) => s.id)).toEqual(["b"]);
    expect(buckets(sets, "7d", Date.parse(T(45)))).toHaveLength(42);
  });

  it("filters to a window picked on the histogram", () => {
    expect(applyFilter(sets, "all", "", [Date.parse(T(9)), Date.parse(T(25))]).map((s) => s.id)).toEqual(["b", "c"]);
    expect(filterCounts(sets, "", [Date.parse(T(29)), Date.parse(T(31))]).all).toBe(1);
  });

  it("narrows the map to matching apps", () => {
    const node = (name: string) => ({ id: `Deployment/shop/${name}`, kind: "Deployment", ns: "shop", name, created: T(0), status: { ready: true } });
    const view = [
      { name: "shop", system: false, rows: [{ key: "api", workload: { node: node("api"), pods: [], uses: new Map() }, services: [], ingresses: [] }, { key: "checkout", workload: { node: node("checkout"), pods: [], uses: new Map() }, services: [], ingresses: [] }] },
      { name: "payments", system: false, rows: [{ key: "pay", workload: { node: node("pay"), pods: [], uses: new Map() }, services: [], ingresses: [] }] },
    ] as unknown as NamespaceView[];
    expect(searchView(view, "check").map((ns) => [ns.name, ns.rows.map((r) => r.key)])).toEqual([["shop", ["checkout"]]]);
    expect(searchView(view, "payments")).toHaveLength(1);
    expect(searchView(view, "")).toBe(view);
  });
});

describe("fittingRange", () => {
  const now = Date.parse(T(40));
  it("picks the smallest range that shows every change", () => {
    expect(fittingRange(sets, now)).toBe("1h"); // 35 minutes of changes
    expect(fittingRange([set({ at: new Date(now - 5 * 3_600_000).toISOString() })], now)).toBe("24h");
    expect(fittingRange([set({ at: new Date(now - 3 * 86_400_000).toISOString() })], now)).toBe("7d");
    expect(fittingRange([set({ at: new Date(now - 30 * 86_400_000).toISOString() })], now)).toBe("7d");
  });
  it("defaults to a day with nothing to show", () => {
    expect(fittingRange([], now)).toBe("24h");
  });
});

describe("causeDiff", () => {
  const edit = set({
    entries: [entry({ at: T(30), diff: [{ path: "LOG_LEVEL", op: "changed", from: "info", to: "verbose" }] })],
  });
  const cause = { object: "ConfigMap/shop/checkout-config" };

  it("finds the diff of the change a cause names", () => {
    expect(causeDiff([edit], { ...cause, at: new Date(Date.parse(T(30)) - 2000).toISOString() })).toEqual([
      { path: "LOG_LEVEL", op: "changed", from: "info", to: "verbose" },
    ]);
  });

  it("finds nothing for another change of the same object, or without a time", () => {
    expect(causeDiff([edit], { ...cause, at: T(20) })).toEqual([]);
    expect(causeDiff([edit], cause)).toEqual([]);
  });
});
