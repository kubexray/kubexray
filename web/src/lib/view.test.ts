import { describe, expect, it } from "vitest";
import type { Edge, Node } from "../gen/graph";
import { podHealth } from "./health";
import type { ClusterState } from "./state";
import { allWorkloads, buildView, chainOf, workloadHealth, workloadProblem } from "./view";

const T = "2026-10-23T14:00:00Z";
const n = (id: string, extra: Partial<Node> = {}): Node => {
  const [kind = "", ns, name = id] = id.split("/");
  return { id, kind, ns, name, status: { ready: true }, created: T, ...extra };
};
const e = (type: string, from: string, to: string): Edge => ({ id: `${type}:${from}:${to}`, type, from, to });

function cluster(nodes: Node[], edges: Edge[]): ClusterState {
  return { seq: 1, ts: T, nodes: new Map(nodes.map((x) => [x.id, x])), edges: new Map(edges.map((x) => [x.id, x])) };
}

// The shop app: Ingress web → Service api → Deployment api (2 pods) → api-config.
const shop = () =>
  cluster(
    [
      n("Namespace/shop"),
      n("Ingress/shop/web"),
      n("Service/shop/api"),
      n("Service/shop/orphan"),
      n("Deployment/shop/api", { status: { ready: false, replicas: { desired: 2, ready: 1, updated: 2 } } }),
      n("ReplicaSet/shop/api-1"),
      n("Pod/shop/api-1-a"),
      n("Pod/shop/api-1-b", { status: { ready: false, reason: "CrashLoopBackOff", restarts: 3 } }),
      n("Pod/shop/api-1-old", { status: { ready: false, reason: "Terminating" } }),
      n("ConfigMap/shop/api-config"),
      n("Deployment/kube-system/coredns", { status: { ready: true, replicas: { desired: 1, ready: 1, updated: 1 } } }),
    ],
    [
      e("routes", "Ingress/shop/web", "Service/shop/api"),
      e("owns", "Deployment/shop/api", "ReplicaSet/shop/api-1"),
      ...["a", "b", "old"].map((p) => e("owns", "ReplicaSet/shop/api-1", `Pod/shop/api-1-${p}`)),
      ...["a", "b"].map((p) => e("selects", "Service/shop/api", `Pod/shop/api-1-${p}`)),
      ...["a", "b"].map((p) => e("mounts", `Pod/shop/api-1-${p}`, "ConfigMap/shop/api-config")),
      e("mounts", "Pod/shop/api-1-a", "ConfigMap/shop/kube-root-ca.crt"),
      e("mounts", "Pod/shop/api-1-a", "Secret/shop/missing"),
    ],
  );

describe("buildView", () => {
  const view = buildView(shop());
  const ns = view[0]!;

  it("puts user namespaces first and system ones last", () => {
    expect(view.map((v) => v.name)).toEqual(["shop", "kube-system"]);
    expect(view[1]!.system).toBe(true);
  });

  it("lays an app out on one row: way in, load balancer, app, what it uses", () => {
    const row = ns.rows[0]!;
    expect(row.ingresses.map((x) => x.name)).toEqual(["web"]);
    expect(row.services.map((x) => x.name)).toEqual(["api"]);
    expect(row.workload?.node.name).toBe("api");
    // Terminating pods are gone; kube-root-ca.crt is noise; missing references show up.
    expect(row.workload?.pods.map((p) => p.name)).toEqual(["api-1-a", "api-1-b"]);
    expect([...row.workload!.uses.keys()]).toEqual(["ConfigMap/shop/api-config", "Secret/shop/missing"]);
    expect(row.workload!.uses.get("Secret/shop/missing")?.created).toBe("");
  });

  it("gives services without pods their own row", () => {
    expect(ns.rows[1]?.services.map((x) => x.name)).toEqual(["orphan"]);
    expect(ns.serviceTargets.get("Service/shop/orphan")?.size).toBe(0);
  });

  it("wires the chain together", () => {
    expect(ns.wires).toContainEqual(["Ingress/shop/web", "Service/shop/api"]);
    expect(ns.wires).toContainEqual(["Service/shop/api", "Deployment/shop/api"]);
    expect(ns.wires).toContainEqual(["Deployment/shop/api", "Deployment/shop/api|ConfigMap/shop/api-config"]);
  });

  it("counts problems", () => {
    expect(ns.problems).toBe(1);
    expect(ns.allHealthy).toBe(false);
    expect(view[1]!.allHealthy).toBe(true);
  });

  it("traces the path to an app", () => {
    expect([...chainOf(view, "Deployment/shop/api", "up")].sort()).toEqual(["Deployment/shop/api", "Ingress/shop/web", "Service/shop/api"]);
    expect(chainOf(view, "Service/shop/api").has("ConfigMap/shop/api-config")).toBe(true);
  });
});

describe("health", () => {
  it("summarizes a workload", () => {
    const w = allWorkloads(buildView(shop())).find(({ w }) => w.node.name === "api")!.w;
    expect(workloadHealth(w)).toBe("crit");
    expect(workloadProblem(w)).toBe("CrashLoopBackOff · 3 restarts");
  });

  it("treats a ready pod as fine even if it crashed before", () => {
    expect(podHealth(n("Pod/shop/x", { status: { ready: true, reason: "OOMKilled", restarts: 2 } }))).toBe("ok");
    expect(podHealth(n("Pod/shop/x", { status: { ready: false, reason: "ContainerCreating" } }))).toBe("warn");
    expect(podHealth(n("Pod/shop/x", { status: { ready: false, reason: "Terminating" } }))).toBe("dying");
  });

  it("calls an app with no ready pods down", () => {
    const w = { node: n("Deployment/shop/x", { status: { ready: false, replicas: { desired: 2, ready: 0, updated: 2 } } }), pods: [], uses: new Map() };
    expect(workloadHealth(w)).toBe("crit");
    expect(workloadProblem(w)).toBe("No pods ready");
  });
});
