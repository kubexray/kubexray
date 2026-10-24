import { useCallback, useEffect, useMemo, useState } from "react";
import { ChangesView } from "./components/ChangesView";
import { Header, type View } from "./components/Header";
import { IncidentsView } from "./components/IncidentsView";
import type { Entry } from "./components/IncidentCard";
import { PostmortemDialog } from "./components/PostmortemDialog";
import { ShareDialog } from "./components/ShareDialog";
import { Timeline } from "./components/Timeline";
import { Workspace } from "./components/Workspace";
import type { Change, Group, Incident } from "./gen/cause";
import type { FeedResponse, HistoryResponse } from "./gen/server";
import { fetchAt, fetchChanges, fetchFeed, fetchHistory, fetchWhy } from "./lib/api";
import { changeEvents, mergeEvents } from "./lib/events";
import { type ClusterState, fromSnapshot } from "./lib/state";
import { useNow } from "./lib/useNow";
import { MAX_EVENTS, useStream } from "./lib/useStream";

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

// A link can open a moment with an app selected (alerts link to the moment
// an incident started, ADR-0011): ?t=<time>&app=<id>.
// Read once, then dropped from the address bar so a reload shows live.
const LINK = new URLSearchParams(location.search);
if (LINK.has("t") || LINK.has("app")) history.replaceState(null, "", location.pathname);
function linkedMoment(): string | null {
  const t = Date.parse(LINK.get("t") ?? "");
  return Number.isFinite(t) ? new Date(t).toISOString() : null;
}
function linkedApp(): { id: string; app: string } | null {
  const id = LINK.get("app");
  return id ? { id, app: id } : null; // links name apps, not pods
}

/** Status events are only seen while the page is open (ADR-0007). */
const OPENED = new Date().toISOString();

const APP_KINDS = new Set(["Deployment", "StatefulSet", "DaemonSet", "Job", "ReplicaSet", "Pod"]);

/** Live mode: the cluster as it is, with rewind over the recording. */
export function App() {
  const stream = useStream();
  const history = usePoll<HistoryResponse>(fetchHistory, 5000);
  const changes = usePoll<Change[]>(fetchChanges, 10_000);
  const feed = usePoll<FeedResponse>(fetchFeed, 5000);
  const tick = useNow(60_000); // only until the first feed arrives
  const [view, setView] = useState<View>("map");
  const [query, setQuery] = useState("");
  // Jumping to the map from the feed or incidents selects an object there
  // (a new Workspace, seeded with it).
  const [focus, setFocus] = useState<{ id: string | null; n: number }>(() => ({ id: LINK.get("app"), n: 0 }));
  const [at, setAt] = useState<string | null>(linkedMoment);
  const { past, last } = usePast(at);
  const [selected, setSelected] = useState<{ id: string; app: string } | null>(linkedApp);
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

  const canRewindTo = (ts: string) => !!history && Date.parse(ts) >= Date.parse(history.from);
  const showOnMap = (id: string, moment: string | null) => {
    setAt(moment);
    setFocus((f) => ({ id, n: f.n + 1 }));
    setSelected(id ? { id, app: id } : null); // the new Workspace doesn't report its seeded selection
    setView("map");
  };
  const sets = feed?.sets ?? [];
  const now = feed ? Date.parse(feed.now) : tick;
  const page =
    view === "changes" ? (
      <ChangesView sets={sets} now={now} query={query} live canRewindTo={canRewindTo} onRewind={(ts, id) => showOnMap(id, ts)} />
    ) : view === "incidents" ? (
      <IncidentsView incidents={stream.incidents} groups={stream.groups} sets={sets} now={now} onShow={(id) => showOnMap(id, null)} />
    ) : undefined;

  return (
    <>
      <Workspace
        key={focus.n}
        page={page}
        search={query}
        header={(v) => (
          <Header
            view={v}
            status={stream.status}
            rewound={!!at}
            nav={{ active: view, onChange: setView, incidents: stream.incidents.length }}
            search={{ value: query, onChange: setQuery }}
          />
        )}
        notice={missing && <div className="notice">kubexray has no history for that moment. Pick a later time, or go back to live.</div>}
        state={state}
        incidents={incidents}
        groups={groups}
        events={events}
        at={at}
        loading={loading}
        why={why}
        initialSelection={focus.id}
        onSelectionChange={onSelectionChange}
        canRewindTo={canRewindTo}
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
        timeline={(compare) => <Timeline bounds={history} at={at} mode="live" events={events} incidents={stream.incidents} onSeek={setAt} compare={compare} />}
      />
      {drafting && (
        <PostmortemDialog
          draft={() => {
            const now = new Date().toISOString();
            // Live, the card only shows ongoing incidents. Rewound, an app no
            // longer listed now has recovered since; when isn't known here.
            const ongoing = new Set(stream.incidents.map((i) => i.app));
            const over = !!at && drafting.incidents.every((i) => !ongoing.has(i.app));
            // The stream keeps the latest MAX_EVENTS status events.
            const capped = stream.events.length >= MAX_EVENTS ? stream.events[0]?.ts : undefined;
            return {
              entry: drafting,
              cluster: "",
              at: at ?? now,
              recovered: over ? { at: now, exact: false } : null,
              events,
              eventsFrom: capped && capped > OPENED ? capped : OPENED,
              link: null,
            };
          }}
          onClose={() => setDrafting(null)}
        />
      )}
      {sharing && <ShareDialog entry={sharing} recordedFrom={history?.from ?? null} onClose={() => setSharing(null)} />}
    </>
  );
}
