import { useCallback, useEffect, useMemo, useState } from "react";
import { Header } from "./components/Header";
import type { Entry } from "./components/IncidentCard";
import { PostmortemDialog } from "./components/PostmortemDialog";
import { ShareDialog } from "./components/ShareDialog";
import { Timeline } from "./components/Timeline";
import { Workspace } from "./components/Workspace";
import type { Change, Group, Incident } from "./gen/cause";
import type { HistoryResponse } from "./gen/server";
import { fetchAt, fetchChanges, fetchHistory, fetchWhy } from "./lib/api";
import { changeEvents, mergeEvents } from "./lib/events";
import { type ClusterState, fromSnapshot } from "./lib/state";
import { useStream } from "./lib/useStream";

/** Polls an API endpoint, keeping the last good answer. */
function usePoll<T>(load: (signal: AbortSignal) => Promise<T | null>, every: number): T | null {
  const [value, setValue] = useState<T | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    const run = () =>
      load(ctl.signal).then(
        (v) => v !== null && setValue(v),
        () => {},
      );
    void run();
    const t = setInterval(run, every);
    return () => {
      clearInterval(t);
      ctl.abort();
    };
  }, [load, every]);
  return value;
}

interface Moment {
  at: string;
  state: ClusterState;
  incidents: Incident[];
  groups: Group[];
}
type Past = Moment | { at: string; missing: true };

/** The cluster at a past moment, from the recorder. `past` only ever answers
 * for the requested moment; `last` is the most recent moment loaded, to keep
 * on screen while the next one loads. */
function usePast(at: string | null): { past: Past | null; last: Moment | null } {
  const [past, setPast] = useState<Past | null>(null);
  const [last, setLast] = useState<Moment | null>(null);
  useEffect(() => {
    if (!at) return;
    const ctl = new AbortController();
    const t = setTimeout(() => {
      fetchAt(at, ctl.signal).then(
        (r) => {
          if (!r) return setPast({ at, missing: true });
          const moment = { at, state: fromSnapshot(r.snapshot), incidents: r.incidents, groups: r.groups ?? [] };
          setPast(moment);
          setLast(moment);
        },
        () => !ctl.signal.aborted && setPast({ at, missing: true }),
      );
    }, 120); // let the slider settle
    return () => {
      clearTimeout(t);
      ctl.abort();
    };
  }, [at]);
  return { past: at && past?.at === at ? past : null, last };
}

/** The engine's explanation for an app the incident list doesn't cover,
 * refreshed every few seconds while it's selected. */
function useWhy(id: string | null, enabled: boolean): Incident | null {
  const [why, setWhy] = useState<{ id: string; inc: Incident } | null>(null);
  useEffect(() => {
    if (!id || !enabled) return;
    const ctl = new AbortController();
    const load = () =>
      fetchWhy(id, ctl.signal).then(
        (inc) => setWhy(inc ? { id, inc } : null),
        () => {},
      );
    void load();
    const t = setInterval(load, 5000);
    return () => {
      clearInterval(t);
      ctl.abort();
    };
  }, [id, enabled]);
  return enabled && why?.id === id ? why.inc : null;
}

/** Status events are only seen while the page is open (ADR-0007). */
const OPENED = new Date().toISOString();

const APP_KINDS = new Set(["Deployment", "StatefulSet", "DaemonSet", "Job", "ReplicaSet", "Pod"]);

/** Live mode: the cluster as it is, with rewind over the recording. */
export function App() {
  const stream = useStream();
  const history = usePoll<HistoryResponse>(fetchHistory, 5000);
  const changes = usePoll<Change[]>(fetchChanges, 10_000);
  const [at, setAt] = useState<string | null>(null);
  const { past, last } = usePast(at);
  const [selected, setSelected] = useState<{ id: string; app: string } | null>(null);
  const [sharing, setSharing] = useState<Entry | null>(null);
  const [drafting, setDrafting] = useState<Entry | null>(null);

  // While a past moment loads (or isn't recorded), keep showing the last one
  // loaded, or the live cluster, dimmed.
  const shownMoment = past && "state" in past ? past : last;
  const state = at ? (shownMoment?.state ?? stream.state) : stream.state;
  const incidents = at ? (shownMoment?.incidents ?? stream.incidents) : stream.incidents;
  const groups = at ? (shownMoment?.groups ?? stream.groups) : stream.groups;
  const missing = !!past && "missing" in past;
  const loading = !!at && !(past && "state" in past);
  const events = useMemo(() => mergeEvents(stream.events, changeEvents(changes ?? [])), [stream.events, changes]);

  const listed = incidents.some((i) => i.app === selected?.app || i.symptom.badPods?.includes(selected?.id ?? ""));
  const explainable = APP_KINDS.has(state?.nodes.get(selected?.id ?? "")?.kind ?? "");
  const why = useWhy(selected?.id ?? null, !at && !listed && explainable);
  const onSelectionChange = useCallback((id: string | null, app: string | null) => setSelected(id ? { id, app: app ?? id } : null), []);

  if (stream.status === "signed-out") {
    return (
      <main className="message">
        <h1>
          kube<span>xray</span>
        </h1>
        <p>This tab isn't signed in. Open kubexray with the link it printed in your terminal.</p>
      </main>
    );
  }
  if (!state) {
    return (
      <main className="message">
        <h1>
          kube<span>xray</span>
        </h1>
        <p>Connecting to kubexray…</p>
      </main>
    );
  }

  return (
    <>
      <Workspace
        header={(view) => <Header view={view} status={stream.status} rewound={!!at} />}
        notice={missing && <div className="notice">kubexray has no history for that moment. Pick a later time, or go back to live.</div>}
        state={state}
        incidents={incidents}
        groups={groups}
        events={events}
        at={at}
        loading={loading}
        why={why}
        onSelectionChange={onSelectionChange}
        canRewindTo={(ts) => !!history && Date.parse(ts) >= Date.parse(history.from)}
        onRewind={(ts) => setAt(new Date(Date.parse(ts) + 1000).toISOString())}
        actions={(entry) => (
          <>
            <button type="button" onClick={() => setSharing(entry)}>
              Share replay
            </button>
            <button type="button" onClick={() => setDrafting(entry)}>
              Post-mortem draft
            </button>
          </>
        )}
        timeline={<Timeline bounds={history} at={at} mode="live" events={events} incidents={stream.incidents} onSeek={setAt} />}
      />
      {drafting && (
        // The card only lists ongoing incidents, so there's no recovery to report.
        <PostmortemDialog
          draft={() => ({ entry: drafting, cluster: "", at: at ?? new Date().toISOString(), recoveredAt: null, events, eventsFrom: OPENED, link: null })}
          onClose={() => setDrafting(null)}
        />
      )}
      {sharing && <ShareDialog entry={sharing} recordedFrom={history?.from ?? null} onClose={() => setSharing(null)} />}
    </>
  );
}
