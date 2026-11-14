// Performance budgets at 1,000 pods (ROADMAP v0.1: "smooth with 1,000 pods").
// jsdom is much slower than a browser, so passing here leaves headroom. The
// cluster shape mirrors internal/synthetic (Go), which can't be shared.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { MapView, MAX_PODS } from "./components/MapView";
import { Workspace } from "./components/Workspace";
import type { Edge, Message, Node } from "./gen/graph";
import { applyDelta, type ClusterState } from "./lib/state";
import { deltaBatcher } from "./lib/useStream";
import { buildView, chainOf, viewKeeper } from "./lib/view";

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
    const props = { view, open: new Map<string, boolean>(), causeOf: () => null, onToggle: () => {}, onSelect: () => {}, onHover: () => {} };
    renderToString(<MapView {...props} />); // warm up
    let html = "";
    const ms = time(() => (html = renderToString(<MapView {...props} />)));
    expect(html.match(/class="pod /g)?.length).toBe(1000);
    expect(ms).toBeLessThan(1000 * slack);
    console.info(`1,000 pods: map rendered in ${ms.toFixed(0)} ms (jsdom)`);
  });
});

/** A delta restarting one pod, as the stream sends them. */
function restart(state: ClusterState, pod: Node, n: number): Message {
  return { v: 1, type: "delta", seq: state.seq + 1, ts: T, ops: [{ op: "upsertNode", node: { ...pod, status: { ...pod.status, restarts: n } } }] };
}

describe("1,000 pods, updating", () => {
  // The live page: the whole workspace, every namespace open, in a DOM.
  const workspace = (state: ClusterState) => (
    <Workspace header={() => null} state={state} incidents={[]} groups={[]} events={[]} at={null} timeline={() => null} canRewindTo={() => false} onRewind={() => {}} />
  );
  const env = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean; ResizeObserver?: unknown };
  const was = { act: env.IS_REACT_ACT_ENVIRONMENT, ro: env.ResizeObserver };
  beforeAll(() => {
    env.IS_REACT_ACT_ENVIRONMENT = true;
    env.ResizeObserver ??= class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  });
  afterAll(() => {
    env.IS_REACT_ACT_ENVIRONMENT = was.act;
    env.ResizeObserver = was.ro;
  });

  const mount = () => {
    const el = document.createElement("div");
    document.body.append(el);
    const root = createRoot(el);
    let state = cluster();
    act(() => root.render(workspace(state)));
    const pods = [...state.nodes.values()].filter((n) => n.kind === "Pod");
    return {
      el,
      pods,
      get state() {
        return state;
      },
      show(next: ClusterState) {
        state = next;
        act(() => root.render(workspace(state)));
      },
      unmount: () => {
        act(() => root.unmount());
        el.remove();
      },
    };
  };

  it("keeps every app it didn't touch, so their cards skip rendering", () => {
    const keep = viewKeeper();
    const state = cluster();
    const before = keep(buildView(state));
    const pod = [...state.nodes.values()].find((n) => n.kind === "Pod" && n.ns === "team-3") as Node;
    const after = keep(buildView(applyDelta(state, restart(state, pod, 1))));
    const changed = after.filter((ns, i) => ns !== before[i]).map((ns) => ns.name);
    expect(changed).toEqual(["team-3"]);
    const rows = (v: typeof before) => v.flatMap((ns) => ns.rows);
    expect(rows(after).filter((r, i) => r !== rows(before)[i])).toHaveLength(1);
    // Nothing changed: the very same view.
    expect(keep(buildView(state))).not.toBe(after);
    expect(keep(buildView(state))).toBe(keep(buildView(state)));
  });

  it("shows a delta in under 25 ms", () => {
    const page = mount();
    expect(page.el.querySelectorAll(".pod")).toHaveLength(1000);
    const n = 50;
    const ms = time(() => {
      for (let i = 0; i < n; i++) page.show(applyDelta(page.state, restart(page.state, page.pods[(i * 37) % page.pods.length] as Node, i + 1)));
    });
    expect(page.state.seq).toBe(1 + n);
    page.unmount();
    console.info(`1,000 pods: a one-pod delta shown in ${(ms / n).toFixed(1)} ms (jsdom, mean of ${n})`);
    expect(ms / n).toBeLessThan(25 * slack);
  });

  it("shows 200 deltas arriving in one frame in under 50 ms", () => {
    const page = mount();
    const ms = time(() => {
      const batch = deltaBatcher(page.state);
      for (let i = 0; i < 200; i++) batch.apply(restart({ ...page.state, seq: page.state.seq + i }, page.pods[(i * 13) % page.pods.length] as Node, i + 1));
      page.show(batch.take().state);
    });
    expect(page.state.seq).toBe(201);
    page.unmount();
    console.info(`1,000 pods: 200 deltas in a frame shown in ${ms.toFixed(0)} ms (jsdom)`);
    expect(ms).toBeLessThan(50 * slack);
  });
});

describe("a 500-replica DaemonSet", () => {
  it("draws a few pods and counts the rest, failing ones first", () => {
    const nodes = new Map<string, Node>();
    const ds = "DaemonSet/infra/agent";
    nodes.set(ds, { id: ds, kind: "DaemonSet", ns: "infra", name: "agent", created: T, status: { ready: false, replicas: { desired: 500, ready: 499, updated: 500 } } });
    const edges = new Map<string, Edge>();
    for (let p = 0; p < 500; p++) {
      const id = `Pod/infra/agent-${String(p).padStart(3, "0")}`;
      const failing = p === 437;
      nodes.set(id, { id, kind: "Pod", ns: "infra", name: `agent-${p}`, created: T, status: failing ? { ready: false, phase: "Running", reason: "CrashLoopBackOff", restarts: 9 } : { ready: true, phase: "Running" } });
      edges.set(`owns:${id}`, { id: `owns:${id}`, type: "owns", from: ds, to: id });
    }
    const view = buildView({ seq: 1, ts: T, nodes, edges });
    const html = renderToString(<MapView view={view} open={new Map()} causeOf={() => null} onToggle={() => {}} onSelect={() => {}} onHover={() => {}} />);
    expect(html.match(/class="pod /g)).toHaveLength(MAX_PODS);
    expect(html).toContain(`+${500 - MAX_PODS}`);
    expect(html).toContain("agent-437"); // the failing pod is drawn
  });
});
