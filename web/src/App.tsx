import { useEffect, useMemo, useRef, useState } from "react";
import { DetailsPanel } from "./components/DetailsPanel";
import { EventLog } from "./components/EventLog";
import { Header } from "./components/Header";
import { IncidentCard, rankIncidents } from "./components/IncidentCard";
import { MapView } from "./components/MapView";
import { Timeline } from "./components/Timeline";
import type { Incident } from "./gen/cause";
import type { HistoryResponse } from "./gen/server";
import { fetchAt, fetchHistory, fetchWhy } from "./lib/api";
import { clock } from "./lib/format";
import { type ClusterState, fromSnapshot } from "./lib/state";
import { useStream } from "./lib/useStream";
import { allWorkloads, buildView, chainOf } from "./lib/view";

/** A clock that ticks, for "4m ago" and durations. */
function useNow(every = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [every]);
  return now;
}

function useHistory(): HistoryResponse | null {
  const [history, setHistory] = useState<HistoryResponse | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    const load = () => fetchHistory(ctl.signal).then(setHistory, () => {});
    load();
    const t = setInterval(load, 5000);
    return () => {
      clearInterval(t);
      ctl.abort();
    };
  }, []);
  return history;
}

/** The cluster at a past moment, from the recorder. */
function usePast(at: string | null): { state: ClusterState; incidents: Incident[] } | null {
  const [past, setPast] = useState<{ state: ClusterState; incidents: Incident[] } | null>(null);
  useEffect(() => {
    if (!at) return;
    const ctl = new AbortController();
    const t = setTimeout(() => {
      fetchAt(at, ctl.signal).then(
        (r) => setPast(r ? { state: fromSnapshot(r.snapshot), incidents: r.incidents } : null),
        () => {},
      );
    }, 120); // let the slider settle
    return () => {
      clearTimeout(t);
      ctl.abort();
    };
  }, [at]);
  return at ? past : null;
}

/** The engine's explanation for an app the incident list doesn't cover. */
function useWhy(id: string | null, seq: number | undefined, enabled: boolean): Incident | null {
  const [why, setWhy] = useState<{ id: string; inc: Incident } | null>(null);
  useEffect(() => {
    if (!id || !enabled) return;
    const ctl = new AbortController();
    const t = setTimeout(() => {
      fetchWhy(id, ctl.signal).then((inc) => setWhy(inc ? { id, inc } : null), () => setWhy(null));
    }, 300);
    return () => {
      clearTimeout(t);
      ctl.abort();
    };
  }, [id, seq, enabled]);
  return why && why.id === id ? why.inc : null;
}

export function App() {
  const stream = useStream();
  const now = useNow();
  const history = useHistory();
  const [at, setAt] = useState<string | null>(null);
  const past = usePast(at);

  const state = at ? (past?.state ?? null) : stream.state;
  const incidents = useMemo(() => (at ? (past?.incidents ?? []) : stream.incidents), [at, past, stream.incidents]);
  const view = useMemo(() => (state ? buildView(state) : []), [state]);

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

  const show = (id: string) => {
    const ns = state?.nodes.get(id)?.ns;
    if (ns) setOpen((o) => new Map(o).set(ns, true));
    setSelected(id);
    requestAnimationFrame(() => document.querySelector(`[data-key="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" }));
  };

  const incidentFor = (id: string | null): Incident | null => {
    if (!id) return null;
    const appId = allWorkloads(view).find(({ w }) => w.pods.some((p) => p.id === id))?.w.node.id ?? id;
    return incidents.find((i) => i.app === appId) ?? null;
  };
  const listed = incidentFor(selected);
  const kind = selected ? state?.nodes.get(selected)?.kind : undefined;
  const explainable = !!kind && ["Deployment", "StatefulSet", "DaemonSet", "Job", "ReplicaSet", "Pod"].includes(kind);
  const why = useWhy(selected, stream.state?.seq, !at && !listed && explainable);

  const top = rankIncidents(incidents)[0];
  const highlight = {
    lit: hovered ? chainOf(view, hovered) : null,
    hovered,
    broken: top ? chainOf(view, top.app, "up") : null,
    selected,
  };
  const causeOf = (appId: string) => {
    const c = incidents.find((i) => i.app === appId)?.causes?.[0];
    return c ? `${c.confidence === "possible" ? "Possibly" : "Likely"} caused by: ${c.summary}${c.at ? ` at ${clock(c.at)}` : ""}` : null;
  };

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
        <p>{at ? "Rebuilding that moment…" : "Connecting to kubexray…"}</p>
      </main>
    );
  }

  return (
    <div className="app">
      <Header view={view} status={stream.status} rewound={!!at} />
      <IncidentCard
        incidents={incidents}
        now={at ? Date.parse(at) : now}
        canRewindTo={(ts) => !!history && Date.parse(ts) >= Date.parse(history.from)}
        onShow={show}
        onRewind={(ts) => setAt(new Date(Date.parse(ts) + 1000).toISOString())}
      />
      {incidents.length === 0 && <div className="allclear">No incidents{at ? " at this moment" : ""}. Every app is running as expected.</div>}
      <p className="guide">
        Read each row left to right: requests come in, a load balancer spreads them across the app's pods, and the app reads its config and storage.
      </p>
      <main className="main">
        <MapView
          view={view}
          open={open}
          highlight={highlight}
          causeOf={causeOf}
          onToggle={(ns, isOpen) => setOpen((o) => (o.get(ns) === isOpen ? o : new Map(o).set(ns, isOpen)))}
          onSelect={setSelected}
          onHover={setHovered}
        />
        <aside>
          <DetailsPanel id={selected} state={state} view={view} incident={listed ?? why} now={now} onSelect={show} />
          <EventLog events={stream.events} at={at} />
        </aside>
      </main>
      <Timeline history={history} at={at} now={now} events={stream.events} incidents={stream.incidents} onSeek={setAt} />
    </div>
  );
}
