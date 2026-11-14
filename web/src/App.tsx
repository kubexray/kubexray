import { useCallback, useEffect, useMemo, useState } from "react";
import { FeedSets } from "./lib/feedContext";
import { ChangesView } from "./components/ChangesView";
import { Header } from "./components/Header";
import { IncidentsView } from "./components/IncidentsView";
import { SourcesView } from "./components/SourcesView";
import type { Entry } from "./components/IncidentCard";
import { PostmortemDialog } from "./components/PostmortemDialog";
import { ShareDialog } from "./components/ShareDialog";
import { Timeline } from "./components/Timeline";
import { Workspace } from "./components/Workspace";
import { ErrorBoundary } from "./components/ErrorBoundary";
import type { Change, Group, Incident } from "./gen/cause";
import type { FeedResponse, HistoryResponse, Sources } from "./gen/server";
import { fetchAt, fetchChanges, fetchFeed, fetchHistory, fetchMe, fetchSources, fetchWhy, type Me, signInURL } from "./lib/api";
import { changeEvents, mergeEvents } from "./lib/events";
import { type ClusterState, fromSnapshot } from "./lib/state";
import { MAX_EVENTS, useStream } from "./lib/useStream";
import { Wordmark } from "./components/Wordmark";
import type { Filter } from "./lib/feed";
import { readRoute, type Route, routeURL, SEARCH, useRouteHistory, type View } from "./lib/route";

interface Polled<T> {
  value: T | null;
  /** When it last answered. */
  ok: string | null;
  /** Failing: the last two tries didn't answer. */
  failing: boolean;
}

/** Polls an API endpoint, keeping the last good answer, and saying when
 * the endpoint stops answering. */
function usePoll<T>(load: (signal: AbortSignal) => Promise<T | null>, every: number): Polled<T> {
  const [polled, setPolled] = useState<Polled<T> & { misses: number }>({ value: null, ok: null, failing: false, misses: 0 });
  useEffect(() => {
    const ctl = new AbortController();
    const run = () =>
      load(ctl.signal).then(
        (v) => setPolled((p) => ({ value: v ?? p.value, ok: new Date().toISOString(), failing: false, misses: 0 })),
        () => !ctl.signal.aborted && setPolled((p) => ({ ...p, misses: p.misses + 1, failing: p.misses + 1 >= 2 })),
      );
    void run();
    const t = setInterval(run, every);
    return () => {
      clearInterval(t);
      ctl.abort();
    };
  }, [load, every]);
  return polled;
}

/** Who is signed in, asked once. */
function useMe(): Me | null {
  const [me, setMe] = useState<Me | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    fetchMe(ctl.signal).then(setMe, () => {});
    return () => ctl.abort();
  }, []);
  return me;
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

// A link opens a view at a moment with an app selected (alerts link to the
// moment an incident started, ADR-0011): ?view=…&t=<time>&app=<id>.
const LINK = readRoute();

/** Status events are only seen while the page is open (ADR-0007). */
const OPENED = new Date().toISOString();

const APP_KINDS = new Set(["Deployment", "StatefulSet", "DaemonSet", "Job", "ReplicaSet", "Pod"]);

/** Live mode: the cluster as it is, with rewind over the recording. */
export function App() {
  const stream = useStream();
  const me = useMe();
  const history = usePoll<HistoryResponse>(fetchHistory, 5000).value;
  const changes = usePoll<Change[]>(fetchChanges, 10_000).value;
  const sources = usePoll<Sources>(fetchSources, 30_000).value;
  const feedPoll = usePoll<FeedResponse>(fetchFeed, 5000);
  const feed = feedPoll.value;
  const [view, setView] = useState<View>(LINK.view);
  const [query, setQuery] = useState("");
  const [changesFilter, setChangesFilter] = useState<Filter>("all");
  // Jumping to the map from the feed or incidents selects an object there
  // (a new Workspace, seeded with it).
  const [focus, setFocus] = useState<{ id: string | null; n: number }>(() => ({ id: LINK.app, n: 0 }));
  const [at, setAt] = useState<string | null>(LINK.t);
  const { past, last } = usePast(at);
  const [selected, setSelected] = useState<{ id: string; app: string } | null>(LINK.app ? { id: LINK.app, app: LINK.app } : null); // links name apps, not pods
  const [sharing, setSharing] = useState<Entry | null>(null);
  const [drafting, setDrafting] = useState<Entry | null>(null);

  // The address bar follows the view, the moment and the selection; Back
  // and Forward bring them back.
  const route = useMemo<Route>(() => ({ view, t: at, app: selected?.app ?? null }), [view, at, selected?.app]);
  const onPop = useCallback((r: Route) => {
    setView(r.view);
    setAt(r.t);
    setSelected(r.app ? { id: r.app, app: r.app } : null);
    setFocus((f) => ({ id: r.app, n: f.n + 1 }));
  }, []);
  useRouteHistory(route, onPop, { enabled: stream.status !== "signed-out" });

  // While a past moment loads (or isn't recorded), keep showing the last one
  // loaded, or the live cluster, dimmed.
  const shownMoment = past && "state" in past ? past : last;
  const state = at ? (shownMoment?.state ?? stream.state) : stream.state;
  const incidents = at ? (shownMoment?.incidents ?? stream.incidents) : stream.incidents;
  const groups = at ? (shownMoment?.groups ?? stream.groups) : stream.groups;
  const missing = !!past && "missing" in past;
  const loading = !!at && !(past && "state" in past);
  const events = useMemo(() => mergeEvents(stream.events, changeEvents(changes ?? [])), [stream.events, changes]);
  // Out of date: the stream is down, or the API stopped answering. Said
  // with when it last was, never with a stale "No incidents".
  const stale = stream.status !== "live" ? { since: stream.updated } : feedPoll.failing ? { since: feedPoll.ok } : null;
  const listed = incidents.some((i) => i.app === selected?.app || i.symptom.badPods?.includes(selected?.id ?? ""));
  const explainable = APP_KINDS.has(state?.nodes.get(selected?.id ?? "")?.kind ?? "");
  const why = useWhy(selected?.id ?? null, !at && !listed && explainable);
  const onSelectionChange = useCallback((id: string | null, app: string | null) => setSelected(id ? { id, app: app ?? id } : null), []);

  if (stream.status === "signed-out") {
    return (
      <main className="message">
        <Wordmark />
        {me?.email ? (
          <p>
            Your sign-in has ended. <a href={signInURL()}>Sign in again</a>
          </p>
        ) : (
          <p>This tab isn't signed in. Open kubexray with the link it printed in your terminal.</p>
        )}
      </main>
    );
  }
  if (!state) {
    return (
      <main className="message">
        <Wordmark />
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
  const go = (v: View) => {
    if (v === "changes") setChangesFilter("all");
    setView(v);
  };
  const sets = feed?.sets ?? [];
  const now = Date.parse(feed?.now ?? state.ts); // the server's clock, like the sets' times
  const moment = at ? Date.parse(at) : now;
  const page =
    view === "changes" ? (
      <ChangesView key={changesFilter} sets={sets} now={now} query={query} live initialFilter={changesFilter} canRewindTo={canRewindTo} onRewind={(ts, id) => showOnMap(id, ts)} recommend={history?.recommend} />
    ) : view === "incidents" ? (
      <IncidentsView
        incidents={incidents}
        groups={groups}
        now={moment}
        live={!at}
        query={query}
        broke={sets.filter((s) => s.verdict === "broke" && Date.parse(s.at) <= moment).length}
        onShow={(id) => showOnMap(id, at)}
        onBroke={() => {
          setChangesFilter("broke");
          setView("changes");
        }}
      />
    ) : view === "sources" ? (
      <SourcesView sources={sources} now={now} />
    ) : undefined;
  const placeholder = SEARCH[view];

  // The way back from a view that couldn't be drawn: live, on the map.
  const backToLive = () => {
    setAt(null);
    setView("map");
    setFocus((f) => ({ id: null, n: f.n + 1 }));
  };
  const shown = (
    <FeedSets.Provider value={sets}>
      <Workspace
        key={focus.n}
        page={page}
        search={query}
        onClearSearch={() => setQuery("")}
        stale={!!stale}
        onLive={at ? () => setAt(null) : undefined}
        header={(v) => (
          <Header
            view={v}
            status={stream.status}
            rewound={!!at}
            stale={stale}
            nav={{ active: view, onChange: go, incidents: incidents.length }}
            search={placeholder ? { value: query, onChange: setQuery, placeholder } : undefined}
            me={me}
            menu={[{ label: "Signal sources", note: "Where incidents come from", onSelect: () => setView("sources") }]}
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
        timeline={(compare, ghost) => (
          <Timeline
            bounds={history}
            at={at}
            mode="live"
            incidents={stream.incidents}
            onSeek={setAt}
            onFocus={(app, ts) => showOnMap(app, ts)}
            compare={compare}
            ghost={ghost}
            stale={!!stale}
            link={() => routeURL({ view: "map", t: at ?? new Date().toISOString(), app: selected?.app ?? null })}
          />
        )}
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
    </FeedSets.Provider>
  );
  return <ErrorBoundary what="the cluster" back={{ label: "Back to live", onClick: backToLive }}>{shown}</ErrorBoundary>;
}
