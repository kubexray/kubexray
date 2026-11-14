// The live stream: a WebSocket that delivers a snapshot, the current
// incidents, then every change. Reconnects with backoff and resyncs from a
// fresh snapshot after any gap.
//
// Deltas are applied as they arrive but shown once per animation frame: a
// busy cluster sends many a second, and rendering each would redo the view
// for changes no one could see.
import { useEffect, useState } from "react";
import type { Group, Incident, IncidentsMessage } from "../gen/cause";
import type { Edge, Message, Node } from "../gen/graph";
import { fetchHistory, SignedOutError } from "./api";
import { type ClusterEvent, statusEvents } from "./events";
import { applyOps, type ClusterState, fromSnapshot, GapError } from "./state";

export type StreamStatus = "connecting" | "live" | "reconnecting" | "signed-out";

export interface Stream {
  status: StreamStatus;
  state: ClusterState | null;
  incidents: Incident[];
  groups: Group[];
  events: ClusterEvent[];
}

export const MAX_EVENTS = 300;

/** Deltas waiting for the next frame, applied to one copy of the state. */
interface Draft {
  seq: number;
  ts: string;
  nodes: Map<string, Node>;
  edges: Map<string, Edge>;
}

/**
 * Collects deltas between frames. `apply` takes them as they arrive and
 * `take` hands over what's new: a state copied once per frame however many
 * deltas came in, and their status events. States handed out never change.
 */
export function deltaBatcher(initial: ClusterState) {
  let shown = initial;
  let draft: Draft | null = null;
  let events: ClusterEvent[] = [];
  return {
    /** Applies a delta; throws GapError if one was missed. */
    apply(msg: Message) {
      if (msg.type !== "delta") throw new Error(`expected a delta, got ${msg.type}`);
      const seq = draft?.seq ?? shown.seq;
      if (msg.seq <= seq) return; // already included
      if (msg.seq !== seq + 1) throw new GapError(seq, msg.seq);
      const d: Draft = (draft ??= { seq: shown.seq, ts: shown.ts, nodes: new Map(shown.nodes), edges: new Map(shown.edges) });
      events.push(...statusEvents(d, msg)); // before the delta applies
      if (events.length > MAX_EVENTS) events = events.slice(-MAX_EVENTS);
      applyOps(d.nodes, d.edges, msg);
      d.seq = msg.seq;
      d.ts = msg.ts;
    },
    pending: () => draft !== null || events.length > 0,
    /** The state with every delta so far, and the events since the last take. */
    take(): { state: ClusterState; events: ClusterEvent[] } {
      if (draft) shown = { seq: draft.seq, ts: draft.ts, nodes: draft.nodes, edges: draft.edges };
      draft = null;
      const out = events;
      events = [];
      return { state: shown, events: out };
    },
  };
}

// Hidden tabs don't get animation frames; nothing is drawn there anyway.
const frame = (f: () => void) => (typeof requestAnimationFrame === "function" ? requestAnimationFrame(f) : setTimeout(f, 16));
const cancelFrame = (id: ReturnType<typeof frame>) => (typeof cancelAnimationFrame === "function" ? cancelAnimationFrame(id as number) : clearTimeout(id));

export function useStream(): Stream {
  const [stream, setStream] = useState<Stream>({ status: "connecting", state: null, incidents: [], groups: [], events: [] });

  useEffect(() => {
    let ws: WebSocket | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let delay = 500;
    let stopped = false;
    let batch: ReturnType<typeof deltaBatcher> | null = null;
    let scheduled: ReturnType<typeof frame> | null = null;

    const flush = () => {
      scheduled = null;
      if (!batch?.pending()) return;
      const { state, events } = batch.take();
      setStream((s) => ({ ...s, state, events: events.length ? [...s.events, ...events].slice(-MAX_EVENTS) : s.events }));
    };

    const connect = () => {
      const scheme = location.protocol === "https:" ? "wss" : "ws";
      const socket = new WebSocket(`${scheme}://${location.host}/api/stream`);
      ws = socket;
      // Something the stream can't follow: start over from a fresh snapshot.
      const resync = (why: unknown) => {
        if (!(why instanceof GapError)) console.error("kubexray: resyncing the stream:", why);
        socket.close(4000, "resync");
      };

      socket.onmessage = (ev: MessageEvent<string>) => {
        try {
          const parsed = JSON.parse(ev.data) as Message | IncidentsMessage;
          if (parsed.type === "incidents") {
            const { incidents, groups } = parsed as IncidentsMessage;
            setStream((s) => ({ ...s, incidents: Array.isArray(incidents) ? incidents : [], groups: Array.isArray(groups) ? groups : [] }));
            return;
          }
          const msg = parsed as Message;
          if (msg.type === "snapshot") {
            // Events already collected still happened; keep them.
            const kept = batch?.take().events ?? [];
            const state = fromSnapshot(msg);
            batch = deltaBatcher(state);
            delay = 500;
            setStream((s) => ({ ...s, status: "live", state, events: kept.length ? [...s.events, ...kept].slice(-MAX_EVENTS) : s.events }));
          } else if (msg.type === "delta" && batch) {
            batch.apply(msg);
            scheduled ??= frame(flush);
          }
        } catch (err) {
          resync(err);
        }
      };

      socket.onclose = () => {
        if (stopped || ws !== socket) return;
        // Show what arrived before the close; the snapshot after the
        // reconnect replaces it.
        if (scheduled !== null) cancelFrame(scheduled);
        flush();
        batch = null;
        // A refused upgrade looks like any other close; ask the API why.
        fetchHistory()
          .then(() => true)
          .catch((err: unknown) => !(err instanceof SignedOutError))
          .then((retryable) => {
            if (stopped) return;
            if (!retryable) {
              // Only the launch link can fix this; stop knocking.
              stopped = true;
              setStream((s) => ({ ...s, status: "signed-out" }));
              return;
            }
            setStream((s) => ({ ...s, status: "reconnecting" }));
            retry = setTimeout(connect, delay);
            delay = Math.min(delay * 2, 10_000);
          });
      };
    };

    connect();
    return () => {
      stopped = true;
      clearTimeout(retry);
      if (scheduled !== null) cancelFrame(scheduled);
      ws?.close();
    };
  }, []);

  return stream;
}
