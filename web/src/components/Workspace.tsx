import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Group, Incident } from "../gen/cause";
import type { ClusterEvent } from "../lib/events";
import { clock } from "../lib/format";
import type { ClusterState } from "../lib/state";
import { searchView } from "../lib/feed";
import { allWorkloads, buildView, chainOf } from "../lib/view";
import { DetailsPanel } from "./DetailsPanel";
import { DiffPanel } from "./DiffPanel";
import { EventLog } from "./EventLog";
import { type Entry, entries, IncidentCard } from "./IncidentCard";
import { MapView } from "./MapView";

interface Props {
  /** Renders the header for the current view. */
  header: (view: ReturnType<typeof buildView>) => ReactNode;
  notice?: ReactNode;
  state: ClusterState;
  incidents: Incident[];
  groups: Group[];
  events: ClusterEvent[];
  /** The moment shown, or null for live. */
  at: string | null;
  loading?: boolean;
  /** Renders the timeline, given diff mode's controls. */
  timeline: (compare: Compare) => ReactNode;
  /** Another view (Changes, Incidents) shown instead of the map. */
  page?: ReactNode;
  /** Narrows the map to matching apps. */
  search?: string;
  canRewindTo: (ts: string) => boolean;
  onRewind: (ts: string) => void;
  actions?: (entry: Entry) => ReactNode;
  /** An explanation for the selected app when the incident list has none. */
  why?: Incident | null;
  initialSelection?: string | null;
  /** The selected node, and the app it belongs to (a pod's workload). */
  onSelectionChange?: (id: string | null, app: string | null) => void;
}

/** Diff mode's controls for the timeline: the moment compared from, if any. */
export interface Compare {
  from: string | null;
  toggle: () => void;
  /** Off while the moment shown is loading or isn't recorded. */
  disabled: boolean;
}

/** Everything live mode and replay mode share: incident card, map, side
 * panel and event log, with selection and hover tracing. */
export function Workspace(props: Props) {
  const { state, incidents, groups, events, at, onSelectionChange } = props;
  const view = useMemo(() => buildView(state), [state]);

  const [selected, setSelectedState] = useState<string | null>(props.initialSelection ?? null);
  // Diff mode: the moment compared from. States never change once made, so
  // keeping this one is enough; the other side is whatever is shown.
  const [base, setBase] = useState<{ at: string; state: ClusterState } | null>(null);
  // Live, the moment is now: the last delta may be minutes old.
  const [liveNow, setLiveNow] = useState(() => new Date().toISOString());
  useEffect(() => {
    if (at || !base) return;
    const t = setInterval(() => setLiveNow(new Date().toISOString()), 1000);
    return () => clearInterval(t);
  }, [at, base]);
  const shownAt = at ?? liveNow;
  const compare: Compare = {
    from: base?.at ?? null,
    disabled: !base && !!props.loading,
    toggle: () => setBase(base ? null : { at: at ?? new Date().toISOString(), state }),
  };
  const [hovered, setHovered] = useState<string | null>(null);
  // A selection passed in (a jump from the feed, a link) opens its namespace,
  // which may be collapsed, and is scrolled to once drawn.
  const [open, setOpen] = useState(() => {
    const ns = props.initialSelection ? state.nodes.get(props.initialSelection)?.ns : undefined;
    return new Map<string, boolean>(ns ? [[ns, true]] : []);
  });
  const initial = useRef(props.initialSelection);
  useEffect(() => {
    const id = initial.current;
    if (id) requestAnimationFrame(() => document.querySelector(`[data-key="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "center" }));
  }, []);
  const appOf = useCallback((id: string | null) => (id ? (allWorkloads(view).find(({ w }) => w.pods.some((p) => p.id === id))?.w.node.id ?? id) : null), [view]);
  const setSelected = useCallback(
    (id: string | null) => {
      setSelectedState(id);
      onSelectionChange?.(id, appOf(id));
    },
    [onSelectionChange, appOf],
  );

  const list = useMemo(() => entries(incidents, groups), [incidents, groups]);
  const topEntry = list[0];

  // Open on the incident the card shows, once (unless a link chose an app).
  const autoSelected = useRef(!!props.initialSelection);
  useEffect(() => {
    const first = topEntry?.incidents[0];
    if (!autoSelected.current && first) {
      autoSelected.current = true;
      setSelected(first.app);
    }
  }, [topEntry, setSelected]);

  const show = useCallback(
    (id: string) => {
      const ns = state.nodes.get(id)?.ns;
      if (ns) setOpen((o) => new Map(o).set(ns, true));
      setSelected(id);
      requestAnimationFrame(() => document.querySelector(`[data-key="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "nearest", behavior: "smooth" }));
    },
    [state, setSelected],
  );
  const toggle = useCallback((ns: string, isOpen: boolean) => setOpen((o) => new Map(o).set(ns, isOpen)), []);

  const selectedApp = appOf(selected);
  const listed = incidents.find((i) => i.app === selectedApp) ?? null;

  // The broken paths of the top incident, or of every app in its group.
  const broken = useMemo(() => {
    if (!topEntry) return null;
    const ids = new Set<string>();
    for (const inc of topEntry.incidents) for (const id of chainOf(view, inc.app, "up")) ids.add(id);
    return ids;
  }, [view, topEntry]);
  const lit = useMemo(() => (hovered ? chainOf(view, hovered) : null), [view, hovered]);
  const highlight = useMemo(() => ({ lit, hovered, broken, selected }), [lit, hovered, broken, selected]);
  const causeOf = useCallback(
    (appId: string) => {
      const c = incidents.find((i) => i.app === appId)?.causes?.[0];
      return c ? `${c.confidence === "possible" ? "Possibly" : "Likely"} caused by: ${c.summary}${c.at ? ` at ${clock(c.at)}` : ""}` : null;
    },
    [incidents],
  );

  // Escape clears the selection on the map; on another view it belongs to
  // that view (closing a dialog).
  const onMap = !props.page;
  useEffect(() => {
    if (!onMap) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setSelected(null);
    addEventListener("keydown", esc);
    return () => removeEventListener("keydown", esc);
  }, [setSelected, onMap]);

  const mapView = useMemo(() => searchView(view, props.search ?? ""), [view, props.search]);

  if (props.page) {
    return (
      <div className="app page">
        {props.header(view)}
        {props.notice}
        {props.page}
      </div>
    );
  }

  return (
    <div className="app">
      {props.header(view)}
      {props.notice}
      <IncidentCard list={list} at={at} canRewindTo={props.canRewindTo} onShow={show} onRewind={props.onRewind} actions={props.actions} />
      {incidents.length === 0 && !props.loading && <div className="allclear">No incidents{at ? " at this moment" : ""}. Every app is running as expected.</div>}
      <p className="guide">
        Read each row left to right: requests come in, a load balancer spreads them across the app's pods, and the app reads its config and storage.
      </p>
      <main className={`main${props.loading ? " loading" : ""}`}>
        <MapView view={mapView} open={open} highlight={highlight} causeOf={causeOf} onToggle={toggle} onSelect={setSelected} onHover={setHovered} />
        <aside>
          <DetailsPanel id={selected} state={state} view={view} incident={listed ?? props.why ?? null} onSelect={show} />
          {base ? (
            <DiffPanel from={base} to={{ at: shownAt, state }} onSelect={show} onClose={() => setBase(null)} />
          ) : (
            <EventLog events={events} at={at} />
          )}
        </aside>
      </main>
      {props.timeline(compare)}
    </div>
  );
}
