// Replay files (ADR-0008): load, validate and play them back in the browser.
// Files are untrusted: size and line limits, header first, version, message
// order and every operation are checked, like the Go reader does.
import type { Change, IncidentsMessage } from "../gen/cause";
import type { Message } from "../gen/graph";
import { MaxLine, MaxUncompressed, type ChangesMessage, type Header } from "../gen/replay";
import { type ClusterEvent, statusEvents } from "./events";
import { applyDelta, type ClusterState, fromSnapshot, validateDelta } from "./state";

export interface Replay {
  header: Header;
  messages: Message[]; // a snapshot, then deltas
  times: number[]; // messages' timestamps, for binary search
  incidents: IncidentsMessage[];
  changes: Change[];
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
  const r: Omit<Replay, "header"> = { messages: [], times: [], incidents: [], changes: [], events: [], checkpoints: [] };
  let state: ClusterState | null = null;

  lines.forEach((line, i) => {
    if (line.length > MaxLine) throw new Error(`Line ${i + 1} is too long.`);
    let msg: { v?: number; type?: string };
    try {
      msg = JSON.parse(line) as { v?: number; type?: string };
    } catch {
      throw new Error(`Line ${i + 1} isn't valid JSON.`);
    }
    if (msg.v !== 1) throw new Error(`Unsupported replay version ${String(msg.v)}.`);
    if (i === 0) {
      if (msg.type !== "header") throw new Error("The file doesn't start with a replay header.");
      header = msg as Header;
      return;
    }
    switch (msg.type) {
      case "snapshot":
      case "delta": {
        const m = msg as Message;
        if (!state) {
          state = fromSnapshot(m); // throws unless it's a snapshot
        } else {
          validateDelta(m);
          if (m.seq !== state.seq + 1) throw new Error(`Line ${i + 1}: messages are out of order.`);
          const before = state;
          state = applyDelta(before, m);
          r.events.push(...statusEvents(before, m));
        }
        if (r.messages.length % CHECKPOINT_EVERY === 0) r.checkpoints.push({ index: r.messages.length, state });
        r.messages.push(m);
        r.times.push(Date.parse(m.ts));
        break;
      }
      case "incidents":
        r.incidents.push(msg as IncidentsMessage);
        break;
      case "changes":
        r.changes = (msg as ChangesMessage).changes ?? [];
        break;
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

/** The cluster at time t: from the nearest checkpoint, apply deltas up to t. */
export function stateAt(r: Replay, t: number): ClusterState {
  const target = Math.max(0, lastAtOrBefore(r.times, t));
  let cp = r.checkpoints[0];
  for (const c of r.checkpoints) if (c.index <= target) cp = c;
  if (!cp) throw new Error("replay has no checkpoints");
  let state = cp.state;
  for (let i = cp.index + 1; i <= target; i++) {
    const m = r.messages[i];
    if (m) state = applyDelta(state, m);
  }
  return state;
}

/** The incidents as of time t. */
export function incidentsAt(r: Replay, t: number): IncidentsMessage | null {
  const times = r.incidents.map((m) => Date.parse(m.ts));
  const i = lastAtOrBefore(times, t);
  return i >= 0 ? (r.incidents[i] ?? null) : null;
}

/** When the replay's first incident appears, or null if it has none. */
export function firstIncident(r: Replay): number | null {
  const m = r.incidents.find((x) => x.incidents.length > 0);
  return m ? Date.parse(m.ts) : null;
}
