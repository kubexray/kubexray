import { describe, expect, it } from "vitest";
import type { Node } from "../gen/graph";
import { byRelease } from "./releases";
import type { NamespaceView, Row } from "./view";

const T = new Date(Date.UTC(2026, 9, 30, 20, 0)).toISOString();
const dep = (name: string, helm?: string): Node => ({
  id: `Deployment/shop/${name}`, kind: "Deployment", ns: "shop", name, created: T, status: { ready: true },
  ...(helm ? { labels: { "helm.sh/chart": `${helm}-1.0.0` }, origin: { tool: "helm", name: helm, namespace: "shop" } } : {}),
});
const svc = (name: string): Node => ({ id: `Service/shop/${name}`, kind: "Service", ns: "shop", name, created: T, status: { ready: true } });
const row = (d: Node, s: Node): Row => ({ key: d.id, workload: { node: d, pods: [], uses: new Map() }, services: [s], ingresses: [] });

describe("the map by release", () => {
  const web = dep("web", "store");
  const api = dep("api");
  const ns: NamespaceView = {
    name: "shop", system: false, rows: [row(web, svc("web")), row(api, svc("api"))],
    wires: [[`Service/shop/web`, web.id], [`Service/shop/api`, api.id]], serviceTargets: new Map(), workloadCount: 2, serviceCount: 2, problems: 0, allHealthy: true,
  };

  it("gives each release its rows and their wires, and leaves the rest in its namespace", () => {
    const { releases, rest } = byRelease([ns, { ...ns, name: "kube-system", system: true }]);
    expect(releases.map((r) => [r.id, r.view.rows.map((x) => x.key), r.view.wires])).toEqual([["release:helm/shop/store", [web.id], [[`Service/shop/web`, web.id]]]]);
    expect(rest.map((v) => [v.name, v.rows.map((x) => x.key), v.workloadCount])).toEqual([["shop", [api.id], 1]]);
  });
});
