import { describe, expect, it } from "vitest";
import type { Incident } from "../gen/cause";
import type { Set as ChangeSet } from "../gen/feed";
import { bucketFor, keepLabels, lastKey, markLabel, model, moments, step, ticks } from "./timeline";

const T0 = Date.UTC(2026, 9, 28, 11, 0);
const T = (m: number) => new Date(T0 + m * 60_000).toISOString();

const set = (over: Partial<ChangeSet>): ChangeSet => ({
  id: over.id ?? "s", at: T(0), last: T(0), title: "a change", entries: [{ at: T(0), object: "ConfigMap/shop/c", kind: "ConfigMap", name: "c", what: "edited", summary: "edited" }],
  verdict: "healthy", ...over,
});

describe("timeline model", () => {
  const broke = set({
    id: "b", at: T(10), verdict: "broke", incident: "Deployment/shop/checkout", note: "checkout is degraded 1m 0s later", broke: T(11), ended: T(15),
    entries: [{ at: T(10), object: "ConfigMap/shop/c", kind: "ConfigMap", name: "c", what: "edited", summary: "edited", diff: [{ path: "LOG_LEVEL", op: "changed", from: "info", to: "verbose" }] }],
  });
  const fixed = set({ id: "f", at: T(14), verdict: "fixed", fixes: ["Deployment/shop/checkout"] });
  const other = set({ id: "o", at: T(2) });

  it("draws an incident broken until its fix, and recovering after", () => {
    const m = model([broke, fixed, other], [], T0, T0 + 30 * 60_000, T0 + 30 * 60_000);
    expect(m.spans).toEqual([expect.objectContaining({ label: "checkout is degraded", start: T0 + 11 * 60_000, fix: T0 + 14 * 60_000, end: T0 + 15 * 60_000 })]);
    expect(m.marks.map((k) => [k.kind, k.label])).toEqual([["cause", "LOG_LEVEL → verbose"], ["fix", "fixed"]]);
    // Every change is activity, the marks' too.
    expect(m.bars.reduce((n, b) => n + b.count, 0)).toBe(3);
  });

  it("marks Helm revisions: on the change they broke with, else on their own", () => {
    const chart = (from: string, to: string) => [{ at: T(0), object: "Deployment/shop/web", kind: "Deployment", name: "web", what: "template", summary: "template", diff: [{ path: 'metadata.labels["helm.sh/chart"]', op: "changed", from: `web-${from}`, to: `web-${to}` }] }];
    const helm = { tool: "helm", name: "web" };
    const up2 = set({ id: "u2", at: T(5), origin: helm, entries: chart("1.0.0", "1.1.0") });
    const up3 = set({ ...broke, id: "u3", origin: helm, entries: [...broke.entries, ...chart("1.1.0", "1.2.0")] });
    const up4 = set({ id: "u4", at: T(20), origin: helm, entries: chart("1.2.0", "1.1.0") });
    const rev = (version: number, m: number) => ({ release: "web", ns: "shop", version, status: "superseded", at: T(m) });
    const m = model([up2, up3, up4], [], T0 - 10 * 60_000, T0 + 30 * 60_000, T0 + 30 * 60_000, [rev(1, -5), rev(2, 5), rev(3, 10), rev(4, 20)]);
    // Revision 3 broke something: its mark says which revision it was.
    expect(m.marks.find((k) => k.kind === "cause")?.helm).toMatchObject({ version: 3, chart: { from: "1.1.0", to: "1.2.0" } });
    expect(m.helm.map((h) => [h.label, !!h.set])).toEqual([["web #1", false], ["web #2", true], ["web #4", true]]);
    // 4 went back to the chart revision 2 installed: a rollback.
    expect(m.helm.find((h) => h.rev.version === 4)?.rev.back).toBe(2);
    expect(m.helm.find((h) => h.rev.version === 2)?.rev.back).toBeUndefined();
  });

  it("knows a rollback of the last upgrade by its change undone", () => {
    const helm = { tool: "helm", name: "web" };
    const replicas = (from: string, to: string, m: number) => set({ id: `r${m}`, at: T(m), origin: helm, entries: [{ at: T(m), object: "Deployment/shop/web", kind: "Deployment", name: "web", what: "replicas", summary: "replicas", diff: [{ path: "replicas", op: "changed", from, to }] }] });
    const rev = (version: number, m: number) => ({ release: "web", ns: "shop", version, status: "superseded", at: T(m) });
    const m = model([replicas("1", "2", 5), replicas("2", "1", 10), replicas("1", "3", 15)], [], T0, T0 + 30 * 60_000, T0 + 30 * 60_000, [rev(2, 5), rev(3, 10), rev(4, 15)]);
    expect(m.helm.map((h) => h.rev.back)).toEqual([undefined, 1, undefined]);
  });

  it("draws one incident once, however many changes are credited with it", () => {
    const also = set({ ...broke, id: "b2", at: T(10.5), title: "another edit" });
    const late = set({ id: "f2", at: T(14.2), verdict: "fixed", fixes: ["Deployment/shop/checkout"], broke: T(11), ended: T(15) });
    const m = model([broke, also, fixed, late], [], T0, T0 + 30 * 60_000, T0 + 30 * 60_000);
    expect(m.spans).toHaveLength(1);
    expect(m.marks.filter((k) => k.kind === "cause")).toHaveLength(1);
  });

  it("draws an incident whose cause wasn't recorded from its fix", () => {
    const fix = set({ id: "f", at: T(14), verdict: "fixed", fixes: ["Deployment/shop/api"], broke: T(-5), ended: T(15) });
    const m = model([fix], [], T0, T0 + 30 * 60_000, T0 + 30 * 60_000);
    expect(m.spans).toEqual([expect.objectContaining({ app: "Deployment/shop/api", label: "api was broken", start: T0 - 5 * 60_000, fix: T0 + 14 * 60_000, end: T0 + 15 * 60_000 })]);
    expect(m.marks.map((k) => [k.kind, k.label])).toEqual([["fix", "fixed"]]);
  });

  it("ends an incident the feed can't explain at the moment shown, never past it", () => {
    const inc = { app: "Deployment/shop/api", kind: "Deployment", namespace: "shop", name: "api", healthy: false, since: T(20), symptom: { summary: "", ready: 0, desired: 1 } } as Incident;
    const m = model([], [inc], T0, T0 + 60 * 60_000, T0 + 25 * 60_000);
    expect(m.spans).toEqual([expect.objectContaining({ app: "Deployment/shop/api", label: "api is down", start: T0 + 20 * 60_000 })]);
    expect(m.spans[0]?.end).toBeUndefined(); // drawn to the moment shown
  });

  it("labels marks with the value that changed", () => {
    expect(markLabel(broke)).toBe("LOG_LEVEL → verbose");
    expect(markLabel(set({ title: "Helm upgrade storefront 1.0.0 → 1.1.0 and a very long title" }))).toHaveLength(26);
  });

  it("steps between changes and incidents", () => {
    const all = moments([broke, fixed, other], model([broke, fixed], [], T0, T0 + 30 * 60_000, T0).spans);
    expect(step(all, T0 + 12 * 60_000, -1)).toBe(T0 + 11 * 60_000);
    expect(step(all, T0 + 12 * 60_000, 1)).toBe(T0 + 14 * 60_000);
    expect(step(all, T0 + 14 * 60_000, 1)).toBeUndefined();
  });

  it("picks bar sizes and ticks for the window", () => {
    expect(bucketFor(60 * 60_000)).toBe(60_000);
    expect(bucketFor(24 * 60 * 60_000)).toBe(60 * 60_000);
    expect(ticks(T0, T0 + 60 * 60_000).length).toBeLessThanOrEqual(8);
  });
});

describe("keepLabels", () => {
  it("keeps the newest label when they crowd, and every label with room", () => {
    expect(keepLabels([{ from: 10, to: 20 }, { from: 15, to: 25 }, { from: 40, to: 50 }])).toEqual([false, true, true]);
  });
});

describe("lastKey", () => {
  it("names a field as people do", () => {
    expect(lastKey("spec.ports[0].targetPort")).toBe("targetPort");
    expect(lastKey('metadata.labels["app.kubernetes.io/version"]')).toBe("version");
    expect(lastKey("env LOG_LEVEL")).toBe("LOG_LEVEL");
    expect(lastKey("LOG_LEVEL")).toBe("LOG_LEVEL");
  });
});
