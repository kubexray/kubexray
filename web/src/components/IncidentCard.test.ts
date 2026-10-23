import { describe, expect, it } from "vitest";
import type { Incident } from "../gen/cause";
import { rankIncidents, stateText } from "./IncidentCard";

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
  });

  it("shows apps that are down first, then the oldest", () => {
    const ranked = rankIncidents([inc("degraded", 2, 3, "2026-10-23T14:00:00Z"), inc("down", 0, 1, "2026-10-23T15:00:00Z"), inc("older", 1, 3, "2026-10-23T13:00:00Z")]);
    expect(ranked.map((i) => i.app)).toEqual(["down", "older", "degraded"]);
  });
});
