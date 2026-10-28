import { describe, expect, it } from "vitest";
import type { ClusterEvent } from "./events";
import { labelOf, logCounts, logRows } from "./eventlog";

const T0 = Date.UTC(2026, 9, 28, 21, 40);
const at = (s: number) => new Date(T0 + s * 1000).toISOString();
let n = 0;
const ev = (s: number, over: Partial<ClusterEvent>): ClusterEvent => ({ id: `e${n++}`, ts: at(s), text: "x", kind: "info", ...over });

const API = "Deployment/shop/api";
const events = [
  ev(0, { kind: "change", object: API, app: API, what: "image nginx → ninx", who: "kubectl set" }),
  ev(10, { kind: "crit", object: "Pod/shop/api-1", app: API, what: "ErrImagePull" }),
  ev(100, { object: "Pod/shop/api-2", app: API, what: "started" }),
  ev(110, { object: "Pod/shop/api-1", app: API, what: "removed" }),
  ev(120, { object: API, app: API, what: "3/3 ready" }),
  ev(130, { object: "Pod/shop/db-0", app: "StatefulSet/shop/db", what: "started" }),
  ev(140, { kind: "change", object: "ConfigMap/shop/api-config", what: "edited" }),
];

describe("the what happened log", () => {
  it("folds a burst of status events on one app, newest first", () => {
    const rows = logRows(events, "all", null);
    expect(rows.map((r) => (r.type === "group" ? `group ${r.label}: ${r.what}` : r.event.what))).toEqual([
      "edited",
      "started", // db's: another app
      "group Deployment api: 3 events: 1 started, 1 removed, 1 status change",
      "ErrImagePull", // problems never fold
      "image nginx → ninx",
    ]);
  });

  it("narrows to an app and what it connects to", () => {
    const scope = { app: API, related: new Set(["ConfigMap/shop/api-config"]) };
    const rows = logRows(events, "all", scope);
    expect(rows).toHaveLength(4); // db's event is out
    expect(logCounts(events, scope)).toEqual({ all: 6, problems: 1, changes: 2 });
  });

  it("filters to problems or changes", () => {
    expect(logRows(events, "problems", null).map((r) => r.type === "event" && r.event.what)).toEqual(["ErrImagePull"]);
    expect(logRows(events, "changes", null)).toHaveLength(2);
  });

  it("names an app the way people do", () => {
    expect(labelOf(API)).toBe("Deployment api");
  });
});
