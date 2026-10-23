// Performance budgets at 1,000 pods (ROADMAP v0.1: "smooth with 1,000 pods").
// jsdom is much slower than a browser, so passing here leaves headroom. The
// cluster shape mirrors internal/synthetic (Go), which can't be shared.
import { renderToString } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { MapView } from "./components/MapView";
import type { Edge, Node } from "./gen/graph";
import { applyDelta, type ClusterState } from "./lib/state";
import { buildView, chainOf } from "./lib/view";

const T = "2026-10-23T14:00:00Z";

/** 10 namespaces × 10 apps × 10 pods, like internal/synthetic. */
function cluster(): ClusterState {
  const nodes = new Map<string, Node>();
  const edges = new Map<string, Edge>();
  const add = (n: Node) => nodes.set(n.id, n);
  const edge = (type: string, from: string, to: string) => edges.set(`${type}:${from}:${to}`, { id: `${type}:${from}:${to}`, type, from, to });
  for (let ns = 0; ns < 10; ns++) {
    const n = `team-${ns}`;
    add({ id: `Namespace/${n}`, kind: "Namespace", name: n, status: { ready: true }, created: T });
    for (let a = 0; a < 10; a++) {
      const app = `svc-${a}`;
      const dep = `Deployment/${n}/${app}`;
      add({ id: dep, kind: "Deployment", ns: n, name: app, created: T, status: { ready: true, replicas: { desired: 10, ready: 10, updated: 10 } } });
      add({ id: `ReplicaSet/${n}/${app}-1`, kind: "ReplicaSet", ns: n, name: `${app}-1`, created: T, status: { ready: true } });
      edge("owns", dep, `ReplicaSet/${n}/${app}-1`);
      add({ id: `Service/${n}/${app}`, kind: "Service", ns: n, name: app, created: T, status: { ready: true } });
      add({ id: `ConfigMap/${n}/${app}-config`, kind: "ConfigMap", ns: n, name: `${app}-config`, created: T, status: { ready: true } });
      for (let p = 0; p < 10; p++) {
        const pod = `Pod/${n}/${app}-1-${p}`;
        add({ id: pod, kind: "Pod", ns: n, name: `${app}-1-${p}`, created: T, status: { ready: true, phase: "Running" } });
        edge("owns", `ReplicaSet/${n}/${app}-1`, pod);
        edge("selects", `Service/${n}/${app}`, pod);
        edge("mounts", pod, `ConfigMap/${n}/${app}-config`);
      }
    }
  }
  return { seq: 1, ts: T, nodes, edges };
}

// Shared CI runners are slower and noisier than a laptop.
const slack = import.meta.env.CI ? 5 : 1;

const time = (f: () => void) => {
  const t = performance.now();
  f();
  return performance.now() - t;
};

describe("1,000 pods", () => {
  const state = cluster();

  it("lays out the map in under 50 ms", () => {
    buildView(state); // warm up
    expect(time(() => buildView(state))).toBeLessThan(50 * slack);
  });

  it("applies a delta of 50 pod changes in under 10 ms", () => {
    const ops = [...state.nodes.values()].filter((n) => n.kind === "Pod").slice(0, 50).map((n) => ({ op: "upsertNode", node: { ...n, status: { ...n.status, restarts: 1 } } }));
    expect(time(() => applyDelta(state, { v: 1, type: "delta", seq: 2, ts: T, ops }))).toBeLessThan(10 * slack);
  });

  it("traces a chain in under 20 ms", () => {
    const view = buildView(state);
    expect(time(() => chainOf(view, "Deployment/team-5/svc-5"))).toBeLessThan(20 * slack);
  });

  it("renders the whole map in under 1 s", () => {
    const view = buildView(state);
    const props = {
      view, open: new Map<string, boolean>(), causeOf: () => null, onToggle: () => {}, onSelect: () => {}, onHover: () => {},
      highlight: { lit: null, hovered: null, broken: null, selected: null },
    };
    renderToString(<MapView {...props} />); // warm up
    let html = "";
    const ms = time(() => (html = renderToString(<MapView {...props} />)));
    expect(html.match(/class="pod /g)?.length).toBe(1000);
    expect(ms).toBeLessThan(1000 * slack);
    console.info(`1,000 pods: map rendered in ${ms.toFixed(0)} ms (jsdom)`);
  });
});
