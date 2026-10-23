import { describe, expect, it } from "vitest";
import type { Edge, Node } from "../gen/graph";
import { diffStates } from "./diff";
import type { ClusterState } from "./state";

const T = (m: number) => new Date(Date.UTC(2026, 9, 23, 14, m)).toISOString();

function state(nodes: Node[], edges: Edge[] = []): ClusterState {
  return { seq: 1, ts: T(0), nodes: new Map(nodes.map((n) => [n.id, n])), edges: new Map(edges.map((e) => [e.id, e])) };
}
const owns = (from: string, to: string): Edge => ({ id: `owns:${from}:${to}`, type: "owns", from, to });

const deploy = (over: Partial<Node> = {}): Node => ({
  id: "Deployment/shop/api",
  kind: "Deployment",
  ns: "shop",
  name: "api",
  created: T(0),
  status: { ready: true, replicas: { desired: 3, ready: 3, updated: 3 } },
  spec: { revision: "1", containers: [{ name: "api", image: "nginx:1.27", limits: { memory: "64Mi" } }] },
  ...over,
});
const pod = (name: string, ready = true, reason?: string): Node => ({
  id: `Pod/shop/${name}`,
  kind: "Pod",
  ns: "shop",
  name,
  created: T(0),
  status: { ready, reason },
});
const rs: Node = { id: "ReplicaSet/shop/api-1", kind: "ReplicaSet", ns: "shop", name: "api-1", created: T(0), status: { ready: true } };
const cm = (change?: Node["change"]): Node => ({ id: "ConfigMap/shop/cfg", kind: "ConfigMap", ns: "shop", name: "cfg", created: T(0), status: { ready: true }, change });

describe("diffStates", () => {
  it("is empty when nothing changed", () => {
    const s = state([deploy(), cm()]);
    expect(diffStates(s, s)).toEqual([]);
  });

  it("describes a rollout: image, resources, probes, revision, pods", () => {
    const before = state([deploy(), rs, pod("api-1-a"), pod("api-1-b")], [owns(deploy().id, rs.id), owns(rs.id, "Pod/shop/api-1-a"), owns(rs.id, "Pod/shop/api-1-b")]);
    const rs2: Node = { ...rs, id: "ReplicaSet/shop/api-2", name: "api-2" };
    const after = state(
      [
        deploy({
          status: { ready: false, replicas: { desired: 3, ready: 1, updated: 1 } },
          spec: { revision: "2", containers: [{ name: "api", image: "nginx:1.72", limits: { memory: "32Mi" }, probes: { readiness: true } }] },
          change: { at: T(5), by: "kubectl-set", tool: "kubectl", via: "set" },
        }),
        rs,
        rs2,
        pod("api-1-a"),
        pod("api-2-a", false, "ImagePullBackOff"),
      ],
      [owns(deploy().id, rs.id), owns(deploy().id, rs2.id), owns(rs.id, "Pod/shop/api-1-a"), owns(rs2.id, "Pod/shop/api-2-a")],
    );
    const [d, ...rest] = diffStates(before, after);
    expect(rest).toEqual([]); // ReplicaSets and pods aren't listed on their own
    expect(d?.change).toBe("changed");
    expect(d?.details).toEqual([
      "api image nginx:1.27 → nginx:1.72",
      "api resources memory limit 64Mi → memory limit 32Mi",
      "api readiness probe added",
      "revision 1 → 2",
      "ready 3/3 → 1/3",
      "pods: 1 started, 1 gone",
      "failing pods 0 → 1",
      "changed by kubectl set",
    ]);
  });

  it("counts pods the way the map does: an evicted, replaced pod isn't failing", () => {
    const edges = [owns(deploy().id, rs.id), owns(rs.id, "Pod/shop/api-1-a")];
    const evicted: Node = { ...pod("api-1-b", false, "Evicted"), status: { ready: false, reason: "Evicted", phase: "Failed" } };
    const before = state([deploy(), rs, pod("api-1-a")], edges);
    const after = state([deploy(), rs, pod("api-1-a"), evicted, pod("api-1-c")], [...edges, owns(rs.id, evicted.id), owns(rs.id, "Pod/shop/api-1-c")]);
    expect(diffStates(before, after)[0]?.details).toEqual(["pods: 1 started"]);
  });

  it("ignores label order", () => {
    const a = state([cm(), { ...deploy(), labels: { a: "1", b: "2" } }]);
    const b = state([cm(), { ...deploy(), labels: { b: "2", a: "1" } }]);
    expect(diffStates(a, b)).toEqual([]);
  });

  it("says who edited a ConfigMap, without its contents", () => {
    const d = diffStates(state([cm({ at: T(0), by: "helm", tool: "helm" })]), state([cm({ at: T(5), by: "kubectl-patch", tool: "kubectl", via: "patch" })]));
    expect(d).toEqual([{ id: "ConfigMap/shop/cfg", kind: "ConfigMap", ns: "shop", name: "cfg", change: "changed", details: ["edited by kubectl patch (contents not read)"] }]);
  });

  it("lists added and removed objects, apps before what they use, and skips system namespaces", () => {
    const sys: Node = { ...cm(), id: "ConfigMap/kube-system/x", ns: "kube-system", name: "x" };
    const d = diffStates(state([cm(), sys]), state([deploy({ change: { at: T(1), by: "helm", tool: "helm" } })]));
    expect(d.map((x) => `${x.change} ${x.kind} ${x.name} ${x.details.join(",")}`)).toEqual(["added Deployment api by helm", "removed ConfigMap cfg "]);
  });

  it("notices nodes going down and scaling", () => {
    const node = (ready: boolean): Node => ({ id: "Node/w2", kind: "Node", name: "w2", created: T(0), status: { ready, reason: ready ? undefined : "NodeStatusUnknown" } });
    const d = diffStates(state([node(true), deploy()]), state([node(false), deploy({ status: { ready: true, replicas: { desired: 5, ready: 5, updated: 5 } } })]));
    expect(d.map((x) => `${x.name}: ${x.details.join(", ")}`)).toEqual(["w2: stopped being ready (NodeStatusUnknown)", "api: replicas 3 → 5, ready 3/3 → 5/5"]);
  });
});
