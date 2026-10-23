import { describe, expect, it } from "vitest";
import type { Group, Incident } from "../gen/cause";
import { entries, rankIncidents, stateText } from "./IncidentCard";

const inc = (app: string, ready: number, desired: number, since: string): Incident => ({
  app, kind: "Deployment", namespace: "shop", name: app, healthy: false, since,
  symptom: { summary: "", ready, desired, badPods: ["x"] },
});

describe("incident card", () => {
  it("words the state like the terminal does", () => {
    expect(stateText(inc("a", 0, 3, "t"))).toBe("is down");
    expect(stateText(inc("a", 2, 3, "t"))).toBe("is degraded");
    expect(stateText(inc("a", 3, 3, "t"))).toBe("has failing pods");
    expect(stateText({ ...inc("a", 3, 3, "t"), healthy: true })).toBe("is healthy");
    const noBadPods = (ready: number, desired: number): Incident => ({ ...inc("a", ready, desired, "t"), symptom: { summary: "", ready, desired } });
    expect(stateText(noBadPods(0, 0))).toBe("is scaled to zero");
    expect(stateText(noBadPods(3, 3))).toBe("can't create new pods");
  });

  it("ranks like the engine: outages first, then larger groups, then the oldest", () => {
    const a = inc("a", 1, 2, "2026-10-23T14:00:00Z");
    const b = inc("b", 1, 2, "2026-10-23T14:00:00Z");
    const c = inc("c", 0, 1, "2026-10-23T15:00:00Z");
    const group: Group = { cause: { kind: "node", object: "Node/w2", summary: "node w2 is not ready", confidence: "very likely" }, apps: ["b", "a"] };
    // c is down, so it beats the degraded group despite being newer and alone.
    expect(entries([a, b, c], [group]).map((e) => e.incidents.map((i) => i.app))).toEqual([["c"], ["b", "a"]]);
    // With an outage in the group too, the larger entry wins.
    const b2 = inc("b", 0, 2, "2026-10-23T14:00:00Z");
    const list = entries([a, b2, c], [group]);
    expect(list.map((e) => e.incidents.map((i) => i.app))).toEqual([["b", "a"], ["c"]]);
    expect(list[0]!.group).toBe(group);
    // A group naming unknown apps is dropped.
    expect(entries([], [group])).toEqual([]);
  });

  it("shows apps that are down first, then the oldest", () => {
    const ranked = rankIncidents([inc("degraded", 2, 3, "2026-10-23T14:00:00Z"), inc("down", 0, 1, "2026-10-23T15:00:00Z"), inc("older", 1, 3, "2026-10-23T13:00:00Z")]);
    expect(ranked.map((i) => i.app)).toEqual(["down", "older", "degraded"]);
  });
});
