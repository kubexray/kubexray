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
