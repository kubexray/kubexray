// Replay files (ADR-0008): load, validate and play them back in the browser.
// Files are untrusted: size and line limits, header first, version, message
// order and every operation are checked, like the Go reader does.
import type { Change, IncidentsMessage } from "../gen/cause";
import { type Edge, type Message, type Node, SchemaVersion } from "../gen/graph";
import type { Set as ChangeSet } from "../gen/feed";
import { MaxLine, MaxUncompressed, type ChangesMessage, type FeedMessage, type Header } from "../gen/replay";
import { type ClusterEvent, statusEvents } from "./events";
import { applyOps, type ClusterState, fromSnapshot, validateDelta } from "./state";

export interface Replay {
  header: Header;
  messages: Message[]; // a snapshot, then deltas
  times: number[]; // messages' timestamps, for binary search
  incidents: IncidentsMessage[];
  incidentTimes: number[]; // incidents' timestamps, for binary search
  changes: Change[];
  feed: ChangeSet[]; // the change feed for the window (ADR-0012), newest first
  events: ClusterEvent[]; // status events from every delta
  checkpoints: { index: number; state: ClusterState }[];
}

const CHECKPOINT_EVERY = 200;

/** Decompresses a .kxr file, stopping at the size limit. */
async function gunzip(data: Blob, limit = MaxUncompressed): Promise<string> {
  const body = new Response(await data.arrayBuffer()).body;
  if (!body) throw new Error("empty file");
  const reader = body.pipeThrough(new DecompressionStream("gzip")).getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.length;
    if (size > limit) {
      await reader.cancel();
      throw new Error(`This replay is larger than ${limit >> 20} MiB uncompressed.`);
    }
    chunks.push(value);
  }
  const all = new Uint8Array(size);
  let off = 0;
  for (const c of chunks) {
    all.set(c, off);
    off += c.length;
  }
  return new TextDecoder().decode(all);
}

/** Parses a timestamp from the file; NaN is never let through. */
function time(raw: unknown, what: string): number {
  const t = typeof raw === "string" ? Date.parse(raw) : NaN;
  if (Number.isNaN(t)) throw new Error(`The replay has an invalid ${what} time.`);
  return t;
}

export async function loadReplay(data: Blob, limit = MaxUncompressed): Promise<Replay> {
  let text: string;
  try {
    text = await gunzip(data, limit);
  } catch (err) {
    if (err instanceof Error && err.message.startsWith("This replay")) throw err;
    throw new Error("This isn't a kubexray replay (.kxr) file.", { cause: err });
  }
  const lines = text.split("\n").filter((l) => l.length > 0);
  let header: Header | null = null;
  const r: Omit<Replay, "header"> = { messages: [], times: [], incidents: [], incidentTimes: [], changes: [], feed: [], events: [], checkpoints: [] };
  // One working state, changed in place; only checkpoints are copies, so
  // loading is linear in the file's size.
  let seq = -1;
  let ts = "";
  const nodes = new Map<string, Node>();
  const edges = new Map<string, Edge>();
  const current = (): ClusterState => ({ seq, ts, nodes, edges });
  // Times only move forward for searching, even if a clock stepped back.
  const forward = (times: number[], t: number) => times.push(Math.max(t, times.at(-1) ?? -Infinity));

  lines.forEach((line, i) => {
    if (line.length > MaxLine) throw new Error(`Line ${i + 1} is too long.`);
    let msg: { v?: number; type?: string };
    try {
      msg = JSON.parse(line) as { v?: number; type?: string };
    } catch {
      throw new Error(`Line ${i + 1} isn't valid JSON.`);
    }
    if (msg.v !== SchemaVersion) throw new Error(`Unsupported replay version ${String(msg.v)}.`);
    if (i === 0) {
      if (msg.type !== "header") throw new Error("The file doesn't start with a replay header.");
      const h = msg as Header;
      if (time(h.from, "start") > time(h.to, "end")) throw new Error("The replay ends before it starts.");
      header = { ...h, cluster: typeof h.cluster === "string" ? h.cluster : "" };
      return;
    }
    switch (msg.type) {
      case "snapshot":
      case "delta": {
        const m = msg as Message;
        const t = time(m.ts, `line ${i + 1}`);
        if (seq < 0) {
          const snap = fromSnapshot(m); // throws unless it's a snapshot
          for (const [id, n] of snap.nodes) nodes.set(id, n);
          for (const [id, e] of snap.edges) edges.set(id, e);
        } else {
          if (m.type !== "delta") throw new Error(`Line ${i + 1}: a replay has only one snapshot.`);
          validateDelta(m);
          if (m.seq !== seq + 1) throw new Error(`Line ${i + 1}: messages are out of order.`);
          r.events.push(...statusEvents(current(), m)); // before the delta applies
          applyOps(nodes, edges, m);
        }
        seq = m.seq;
        ts = m.ts;
        if (r.messages.length % CHECKPOINT_EVERY === 0) r.checkpoints.push({ index: r.messages.length, state: { seq, ts, nodes: new Map(nodes), edges: new Map(edges) } });
        r.messages.push(m);
        forward(r.times, t);
        break;
      }
      case "incidents": {
        const m = msg as IncidentsMessage;
        forward(r.incidentTimes, time(m.ts, `line ${i + 1}`));
        r.incidents.push({ ...m, incidents: Array.isArray(m.incidents) ? m.incidents : [], groups: Array.isArray(m.groups) ? m.groups : [] });
        break;
      }
      case "feed": {
        const sets = (msg as FeedMessage).sets;
        r.feed = Array.isArray(sets) ? sets : [];
        break;
      }
      case "changes": {
        const changes = (msg as ChangesMessage).changes;
        r.changes = Array.isArray(changes) ? changes : [];
        break;
      }
      default:
        throw new Error(`Line ${i + 1}: unknown message type "${String(msg.type)}".`);
    }
  });
  if (!header) throw new Error("This replay is empty.");
  if (!r.messages.length) throw new Error("This replay has no cluster snapshot.");
  return { header, ...r };
}

/** The index of the last element ≤ t, or -1. */
function lastAtOrBefore(times: number[], t: number): number {
  let lo = 0;
  let hi = times.length - 1;
  let found = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if ((times[mid] ?? Infinity) <= t) {
      found = mid;
      lo = mid + 1;
    } else hi = mid - 1;
  }
  return found;
}

// The last state computed per replay, so playback steps forward a delta or
// two instead of replaying from a checkpoint on every tick.
const last = new WeakMap<Replay, { index: number; state: ClusterState }>();

/** The cluster at time t: from the last state or nearest checkpoint, apply deltas up to t. */
export function stateAt(r: Replay, t: number): ClusterState {
  const target = Math.max(0, lastAtOrBefore(r.times, t));
  let base = r.checkpoints[0];
  for (const c of r.checkpoints) if (c.index <= target) base = c;
  if (!base) throw new Error("replay has no checkpoints");
  const prev = last.get(r);
  if (prev && prev.index <= target && prev.index >= base.index) base = prev;
  if (base.index === target) return base.state;

  // Copy once, then apply in place: states already handed out never change.
  const nodes = new Map(base.state.nodes);
  const edges = new Map(base.state.edges);
  let { seq, ts } = base.state;
  for (let i = base.index + 1; i <= target; i++) {
    const m = r.messages[i];
    if (!m) continue;
    applyOps(nodes, edges, m);
    seq = m.seq;
    ts = m.ts;
  }
  const state = { seq, ts, nodes, edges };
  last.set(r, { index: target, state });
  return state;
}

/** The incidents as of time t. */
export function incidentsAt(r: Replay, t: number): IncidentsMessage | null {
  const i = lastAtOrBefore(r.incidentTimes, t);
  return i >= 0 ? (r.incidents[i] ?? null) : null;
}

/** When the replay's first incident appears, or null if it has none. */
export function firstIncident(r: Replay): number | null {
  const i = r.incidents.findIndex((x) => x.incidents.length > 0);
  return i >= 0 ? (r.incidentTimes[i] ?? null) : null;
}

/** When none of the apps had an incident any more, after t, or null if the
 * recording ends before that. */
export function recoveredAt(r: Replay, apps: string[], t: number): number | null {
  const want = new Set(apps);
  for (let i = Math.max(0, lastAtOrBefore(r.incidentTimes, t) + 1); i < r.incidents.length; i++) {
    if (!r.incidents[i]?.incidents.some((inc) => want.has(inc.app))) return r.incidentTimes[i] ?? null;
  }
  return null;
}
