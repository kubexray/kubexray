import { describe, expect, it } from "vitest";
import type { Entry, Set as ChangeSet } from "../gen/feed";
import { applyFilter, author, filterCounts, hourly, matches, searchView, summarize } from "./feed";
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

  it("counts changes per hour, flagging what broke", () => {
    const hours = hourly(sets, Date.parse(T(45)));
    expect(hours).toHaveLength(24);
    expect(hours[23]).toMatchObject({ count: 4, broke: true, drift: true });
    expect(hours.slice(0, 23).every((h) => h.count === 0)).toBe(true);
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
