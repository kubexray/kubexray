import { describe, expect, it } from "vitest";
import demo from "../../pages-public/demo.kxr?inline";
import { safeReplayURL } from "../Player";
import { firstIncident, incidentsAt, loadReplay, recoveredAt, stateAt } from "./replay";

const T = (s: number) => new Date(Date.UTC(2026, 9, 23, 14, 0, s)).toISOString();
const pod = (name: string) => ({ id: `Pod/shop/${name}`, kind: "Pod", ns: "shop", name, status: { ready: true }, created: T(0) });

const lines = [
  { v: 1, type: "header", from: T(0), to: T(60), cluster: "demo", anonymized: false, kubexray: "dev" },
  { v: 1, type: "snapshot", seq: 1, ts: T(0), nodes: [pod("a")], edges: [] },
  { v: 1, type: "incidents", ts: T(0), incidents: [], groups: [] },
  { v: 1, type: "delta", seq: 2, ts: T(10), ops: [{ op: "upsertNode", node: pod("b") }] },
  { v: 1, type: "delta", seq: 3, ts: T(20), ops: [{ op: "removeNode", id: "Pod/shop/a" }] },
  { v: 1, type: "incidents", ts: T(30), incidents: [{ app: "Deployment/shop/x", kind: "Deployment", namespace: "shop", name: "x", healthy: false, symptom: { summary: "", ready: 0, desired: 1 } }], groups: [] },
  { v: 1, type: "changes", changes: [] },
];

/** A .kxr file from rows, gzipped with the standard CompressionStream. */
async function gzip(text: string): Promise<Blob> {
  const body = new Response(text).body;
  if (!body) throw new Error("no body");
  return new Blob([await new Response(body.pipeThrough(new CompressionStream("gzip"))).arrayBuffer()]);
}
const file = (rows: unknown[]) => gzip(rows.map((r) => (typeof r === "string" ? r : JSON.stringify(r))).join("\n"));

describe("loadReplay", () => {
  it("plays a replay back at any moment", async () => {
    const r = await loadReplay(await file(lines));
    expect(r.header.cluster).toBe("demo");
    expect([...stateAt(r, Date.parse(T(5))).nodes.keys()]).toEqual(["Pod/shop/a"]);
    expect([...stateAt(r, Date.parse(T(15))).nodes.keys()].sort()).toEqual(["Pod/shop/a", "Pod/shop/b"]);
    expect([...stateAt(r, Date.parse(T(60))).nodes.keys()]).toEqual(["Pod/shop/b"]);
    expect(incidentsAt(r, Date.parse(T(29)))?.incidents).toHaveLength(0);
    expect(incidentsAt(r, Date.parse(T(31)))?.incidents).toHaveLength(1);
    expect(firstIncident(r)).toBe(Date.parse(T(30)));
    expect(r.events.map((e) => e.text)).toEqual(["Pod b started", "Pod a removed"]);
  });

  // Each case must fail for its own reason, not an earlier one.
  const bad: [string, Promise<Blob>, RegExp][] = [
    ["not gzip", Promise.resolve(new Blob(["hello"])), /isn't a kubexray replay/],
    ["no header first", file(lines.slice(1)), /doesn't start with a replay header/],
    ["wrong version", file([{ ...lines[0], v: 2 }, ...lines.slice(1)]), /Unsupported replay version 2/],
    ["unknown type", file([...lines, { v: 1, type: "exploit" }]), /unknown message type "exploit"/],
    ["not JSON", file([lines[0], "{"]), /isn't valid JSON/],
    ["delta first", file([lines[0], lines[3]]), /expected a snapshot/],
    ["bad op", file([lines[0], lines[1], { v: 1, type: "delta", seq: 2, ts: T(1), ops: [{ op: "upsertNode" }] }]), /invalid upsertNode/],
    ["unknown op", file([lines[0], lines[1], { v: 1, type: "delta", seq: 2, ts: T(1), ops: [{ op: "dropTables", id: "x" }] }]), /invalid dropTables/],
    ["out of order", file([lines[0], lines[1], { ...lines[3], seq: 9 }]), /out of order/],
    ["no snapshot", file([lines[0]]), /no cluster snapshot/],
    ["no header times", file([{ v: 1, type: "header" }, ...lines.slice(1)]), /invalid start time/],
    ["a backwards window", file([{ ...lines[0], from: T(60), to: T(0) }, ...lines.slice(1)]), /ends before it starts/],
    ["a bad message time", file([lines[0], { ...lines[1], ts: "soon" }]), /invalid line 2 time/],
    ["two snapshots", file([lines[0], lines[1], { ...lines[1], seq: 2 }]), /only one snapshot/],
  ];
  for (const [name, blob, why] of bad) {
    it(`rejects a file with ${name}`, async () => {
      await expect(loadReplay(await blob)).rejects.toThrow(why);
    });
  }

  it("steps through a long replay without changing states already shown", async () => {
    const at = (s: number) => Date.parse(T(0)) + s * 1000;
    const deltas = Array.from({ length: 450 }, (_, i) => ({ v: 1, type: "delta", seq: i + 2, ts: new Date(at(1 + i / 10)).toISOString(), ops: [{ op: "upsertNode", node: pod(`p${i}`) }] }));
    const r = await loadReplay(await file([lines[0], lines[1], ...deltas]));
    expect(r.checkpoints.map((c) => c.index)).toEqual([0, 200, 400]);
    const want = (s: number) => 1 + Math.min(450, Math.floor(Math.round((s - 1) * 10)) + 1); // pod a, plus the deltas so far
    const early = stateAt(r, at(10));
    // Forward in small steps (playback), back (a seek), then forward again.
    for (const s of [10, 10.5, 20, 25, 30, 46, 5, 46]) expect(stateAt(r, at(s)).nodes.size).toBe(want(s));
    expect(early.nodes.size).toBe(want(10));
  });

  it("tolerates clocks that step back and missing incident lists", async () => {
    const r = await loadReplay(
      await file([lines[0], lines[1], { v: 1, type: "incidents", ts: T(30), incidents: null, groups: null }, { ...lines[5], ts: T(20) }]),
    );
    expect(r.incidents[0]?.incidents).toEqual([]);
    expect(incidentsAt(r, Date.parse(T(35)))?.incidents).toHaveLength(1);
    expect(firstIncident(r)).toBe(Date.parse(T(30)));
  });

  it("loads the demo published with the player", async () => {
    const bytes = Uint8Array.from(atob(demo.slice(demo.indexOf(",") + 1)), (c) => c.charCodeAt(0));
    const r = await loadReplay(new Blob([bytes]));
    const at = firstIncident(r);
    expect(at).not.toBeNull();
    expect(incidentsAt(r, at ?? 0)?.incidents[0]?.app).toBe("Deployment/shop/checkout");
  });

  it("finds when the apps recovered", async () => {
    const r = await loadReplay(await file([...lines.slice(0, 6), { v: 1, type: "incidents", ts: T(50), incidents: [], groups: [] }]));
    expect(recoveredAt(r, ["Deployment/shop/x"], Date.parse(T(35)))).toBe(Date.parse(T(50)));
    expect(recoveredAt(r, ["Deployment/shop/x"], Date.parse(T(55)))).toBeNull();
  });

  it("rejects files over the size limit", async () => {
    await expect(loadReplay(await file(lines), 100)).rejects.toThrow(/larger than/);
  });
});

describe("replay links", () => {
  const base = "https://kubexray.github.io/kubexray/";
  it("accepts https links and paths on the same site", () => {
    expect(safeReplayURL("https://example.com/incident.kxr", base)?.href).toBe("https://example.com/incident.kxr");
    expect(safeReplayURL("/api/replay", "http://127.0.0.1:5000/")?.href).toBe("http://127.0.0.1:5000/api/replay");
  });
  it("refuses everything else", () => {
    for (const raw of ["http://example.com/x.kxr", "javascript:alert(1)", "data:text/plain,x", "//evil.example/x.kxr", "file:///etc/passwd"]) {
      expect(safeReplayURL(raw, "http://127.0.0.1:5000/")).toBeNull();
    }
  });
});
