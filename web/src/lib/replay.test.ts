import { describe, expect, it } from "vitest";
import { safeReplayURL } from "../Player";
import { firstIncident, incidentsAt, loadReplay, stateAt } from "./replay";

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
  ];
  for (const [name, blob, why] of bad) {
    it(`rejects a file with ${name}`, async () => {
      await expect(loadReplay(await blob)).rejects.toThrow(why);
    });
  }

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
