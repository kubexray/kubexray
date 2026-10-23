import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { DetailsPanel } from "./components/DetailsPanel";
import { EventLog } from "./components/EventLog";
import { Header } from "./components/Header";
import { IncidentCard, rankIncidents } from "./components/IncidentCard";
import { MapView } from "./components/MapView";
import { Timeline } from "./components/Timeline";
import type { Change, Incident } from "./gen/cause";
import type { HistoryResponse } from "./gen/server";
import { fetchAt, fetchChanges, fetchHistory, fetchWhy } from "./lib/api";
import { changeEvents, mergeEvents } from "./lib/events";
import { clock } from "./lib/format";
import { type ClusterState, fromSnapshot } from "./lib/state";
import { useStream } from "./lib/useStream";
import { allWorkloads, buildView, chainOf } from "./lib/view";

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
          const moment = { at, state: fromSnapshot(r.snapshot), incidents: r.incidents };
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

const APP_KINDS = new Set(["Deployment", "StatefulSet", "DaemonSet", "Job", "ReplicaSet", "Pod"]);

export function App() {
  const stream = useStream();
  const history = usePoll<HistoryResponse>(fetchHistory, 5000);
  const changes = usePoll<Change[]>(fetchChanges, 10_000);
  const [at, setAt] = useState<string | null>(null);
  const { past, last } = usePast(at);

  // While a past moment loads (or isn't recorded), keep showing the last one
  // loaded, or the live cluster, dimmed.
  const shownMoment = past && "state" in past ? past : last;
  const state = at ? (shownMoment?.state ?? stream.state) : stream.state;
  const incidents = at ? (shownMoment?.incidents ?? stream.incidents) : stream.incidents;
  const missing = !!past && "missing" in past;
  const loading = !!at && !(past && "state" in past);

  const view = useMemo(() => (state ? buildView(state) : []), [state]);
  const events = useMemo(() => mergeEvents(stream.events, changeEvents(changes ?? [])), [stream.events, changes]);

  const [selected, setSelected] = useState<string | null>(null);
  const [hovered, setHovered] = useState<string | null>(null);
  const [open, setOpen] = useState(new Map<string, boolean>());

  // Open on the most severe incident, once.
  const autoSelected = useRef(false);
  useEffect(() => {
    const first = rankIncidents(incidents)[0];
    if (!autoSelected.current && first) {
      autoSelected.current = true;
      setSelected(first.app);
    }
  }, [incidents]);

  const show = useCallback(
    (id: string) => {
      const ns = state?.nodes.get(id)?.ns;
      if (ns) setOpen((o) => new Map(o).set(ns, true));
      setSelected(id);
      requestAnimationFrame(() => document.querySelector(`[data-key="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" }));
    },
    [state],
  );
  const toggle = useCallback((ns: string, isOpen: boolean) => setOpen((o) => new Map(o).set(ns, isOpen)), []);

  const appOf = (id: string | null) => (id ? (allWorkloads(view).find(({ w }) => w.pods.some((p) => p.id === id))?.w.node.id ?? id) : null);
  const selectedApp = appOf(selected);
  const listed = incidents.find((i) => i.app === selectedApp) ?? null;
  const explainable = APP_KINDS.has(state?.nodes.get(selected ?? "")?.kind ?? "");
  const why = useWhy(selected, !at && !listed && explainable);

  const top = rankIncidents(incidents)[0];
  const broken = useMemo(() => (top ? chainOf(view, top.app, "up") : null), [view, top]);
  const lit = useMemo(() => (hovered ? chainOf(view, hovered) : null), [view, hovered]);
  const highlight = useMemo(() => ({ lit, hovered, broken, selected }), [lit, hovered, broken, selected]);
  const causeOf = useCallback(
    (appId: string) => {
      const c = incidents.find((i) => i.app === appId)?.causes?.[0];
      return c ? `${c.confidence === "possible" ? "Possibly" : "Likely"} caused by: ${c.summary}${c.at ? ` at ${clock(c.at)}` : ""}` : null;
    },
    [incidents],
  );

  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setSelected(null);
    addEventListener("keydown", esc);
    return () => removeEventListener("keydown", esc);
  }, []);

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
    <div className="app">
      <Header view={view} status={stream.status} rewound={!!at} />
      {missing && <div className="notice">kubexray has no history for that moment. Pick a later time, or go back to live.</div>}
      <IncidentCard
        incidents={incidents}
        at={at}
        canRewindTo={(ts) => !!history && Date.parse(ts) >= Date.parse(history.from)}
        onShow={show}
        onRewind={(ts) => setAt(new Date(Date.parse(ts) + 1000).toISOString())}
      />
      {incidents.length === 0 && !loading && <div className="allclear">No incidents{at ? " at this moment" : ""}. Every app is running as expected.</div>}
      <p className="guide">
        Read each row left to right: requests come in, a load balancer spreads them across the app's pods, and the app reads its config and storage.
      </p>
      <main className={`main${loading ? " loading" : ""}`}>
        <MapView view={view} open={open} highlight={highlight} causeOf={causeOf} onToggle={toggle} onSelect={setSelected} onHover={setHovered} />
        <aside>
          <DetailsPanel id={selected} state={state} view={view} incident={listed ?? why} onSelect={show} />
          <EventLog events={events} at={at} />
        </aside>
      </main>
      <Timeline history={history} at={at} events={events} incidents={stream.incidents} onSeek={setAt} />
    </div>
  );
}
