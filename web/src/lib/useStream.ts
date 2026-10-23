// The live stream: a WebSocket that delivers a snapshot, the current
// incidents, then every change. Reconnects with backoff and resyncs from a
// fresh snapshot after any gap.
import { useEffect, useRef, useState } from "react";
import type { Group, Incident, IncidentsMessage } from "../gen/cause";
import type { Message } from "../gen/graph";
import { fetchHistory, SignedOutError } from "./api";
import { type ClusterEvent, statusEvents } from "./events";
import { applyDelta, type ClusterState, fromSnapshot, GapError } from "./state";

export type StreamStatus = "connecting" | "live" | "reconnecting" | "signed-out";

export interface Stream {
  status: StreamStatus;
  state: ClusterState | null;
  incidents: Incident[];
  groups: Group[];
  events: ClusterEvent[];
}

export const MAX_EVENTS = 300;

export function useStream(): Stream {
  const [stream, setStream] = useState<Stream>({ status: "connecting", state: null, incidents: [], groups: [], events: [] });
  const current = useRef<ClusterState | null>(null);

  useEffect(() => {
    let ws: WebSocket | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let delay = 500;
    let stopped = false;

    const connect = () => {
      const scheme = location.protocol === "https:" ? "wss" : "ws";
      ws = new WebSocket(`${scheme}://${location.host}/api/stream`);

      ws.onmessage = (ev: MessageEvent<string>) => {
        const parsed = JSON.parse(ev.data) as Message | IncidentsMessage;
        if (parsed.type === "incidents") {
          const { incidents, groups } = parsed as IncidentsMessage;
          setStream((s) => ({ ...s, incidents, groups: groups ?? [] }));
          return;
        }
        const msg = parsed as Message;
        try {
          if (msg.type === "snapshot") {
            current.current = fromSnapshot(msg);
            delay = 500;
            setStream((s) => ({ ...s, status: "live", state: current.current }));
          } else if (msg.type === "delta" && current.current) {
            const before = current.current;
            current.current = applyDelta(before, msg);
            const events = statusEvents(before, msg);
            setStream((s) => ({
              ...s,
              state: current.current,
              events: events.length ? [...s.events, ...events].slice(-MAX_EVENTS) : s.events,
            }));
          }
        } catch (err) {
          if (err instanceof GapError) ws?.close(4000, "resync"); // reconnect for a fresh snapshot
          else throw err;
        }
      };

      ws.onclose = () => {
        if (stopped) return;
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
      ws?.close();
    };
  }, []);

  return stream;
}
