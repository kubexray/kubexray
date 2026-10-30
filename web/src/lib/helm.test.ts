import { describe, expect, it } from "vitest";
import type { Set as ChangeSet } from "../gen/feed";
import type { Node } from "../gen/graph";
import { chartVersion, driftOf, ownerOf, ownerText, parseReleaseId, releaseId, releaseObjects, revisions, revisionSet } from "./helm";
import { fromSnapshot } from "./state";

const T = (m: number) => new Date(Date.UTC(2026, 9, 28, 20, m)).toISOString();
const dep: Node = {
  id: "Deployment/shop/storefront", kind: "Deployment", ns: "shop", name: "storefront", created: T(0), status: { ready: true },
  labels: { "helm.sh/chart": "storefront-1.2.1" }, origin: { tool: "helm", name: "storefront", namespace: "shop" },
};
const secret = (v: number, status: string, m: number): Node => ({
  id: `Secret/shop/sh.helm.release.v1.storefront.v${v}`, kind: "Secret", ns: "shop", name: `sh.helm.release.v1.storefront.v${v}`, created: T(m), status: { ready: true },
  labels: { owner: "helm", name: "storefront", status, version: String(v) },
});

describe("helm on the map", () => {
  it("names who manages an app, with the chart version", () => {
    expect(chartVersion(dep)).toBe("1.2.1");
    expect(ownerText(ownerOf(dep) ?? { tool: "helm", name: "?" })).toBe("storefront 1.2.1");
    expect(ownerText({ tool: "argocd", name: "shop" })).toBe("argo · shop");
    expect(ownerOf({ ...dep, origin: undefined })).toBeUndefined();
  });

  it("reads a release's revisions from Helm's records, newest first", () => {
    const st = fromSnapshot({ v: 1, type: "snapshot", seq: 1, ts: T(9), nodes: [dep, secret(1, "superseded", 1), secret(3, "deployed", 5), secret(2, "failed", 3)], edges: [] });
    expect(revisions(st, "storefront", "shop").map((r) => [r.version, r.status])).toEqual([
      [3, "deployed"],
      [2, "failed"],
      [1, "superseded"],
    ]);
  });

  it("selects a release, and lists what it manages", () => {
    const pod: Node = { ...dep, id: "Pod/shop/storefront-1", kind: "Pod", name: "storefront-1" };
    const st = fromSnapshot({ v: 1, type: "snapshot", seq: 1, ts: T(9), nodes: [dep, pod, secret(1, "deployed", 1)], edges: [] });
    const id = releaseId({ tool: "helm", name: "storefront" }, "shop");
    expect(parseReleaseId(id)).toEqual({ tool: "helm", ns: "shop", name: "storefront" });
    expect(parseReleaseId("Deployment/shop/storefront")).toBeUndefined();
    expect(releaseObjects(st, "helm", "shop", "storefront").map((n) => n.id)).toEqual([dep.id]);
  });

  it("finds what a revision did, and a hand edit still in place", () => {
    const upgrade: ChangeSet = { id: "u", at: T(3), last: T(3), title: "Helm upgrade storefront", verdict: "broke", origin: { tool: "helm", name: "storefront" }, entries: [{ at: T(3), object: dep.id, kind: "Deployment", name: "storefront", what: "image", summary: "image" }] };
    const edit: ChangeSet = { id: "e", at: T(6), last: T(6), title: "edit", verdict: "healthy", entries: [{ at: T(6), object: dep.id, kind: "Deployment", name: "storefront", what: "replicas", summary: "replicas 2 → 3", drift: "edited by hand" }] };
    expect(revisionSet([edit, upgrade], { tool: "helm", name: "storefront" }, { version: 2, status: "failed", at: T(3) })?.id).toBe("u");
    // Two revisions a few seconds apart: each takes its own change.
    const install: ChangeSet = { ...upgrade, id: "i", at: new Date(Date.parse(T(3)) - 11_000).toISOString() };
    expect(revisionSet([upgrade, install], { tool: "helm", name: "storefront" }, { version: 1, status: "superseded", at: install.at })?.id).toBe("i");
    expect(driftOf([upgrade, edit], dep.id)?.drift).toBe("edited by hand");
    // Helm applied again after the edit: no longer drifted.
    const again: ChangeSet = { ...upgrade, id: "a", at: T(8), entries: [{ ...upgrade.entries[0], at: T(8) } as ChangeSet["entries"][number]] };
    expect(driftOf([upgrade, edit, again], dep.id)).toBeUndefined();
  });
});
