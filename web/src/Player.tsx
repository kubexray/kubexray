import { useCallback, useEffect, useMemo, useState } from "react";
import { FeedSets } from "./lib/feedContext";
import { ChangesView } from "./components/ChangesView";
import { Header } from "./components/Header";
import { IncidentsView } from "./components/IncidentsView";
import type { Entry } from "./components/IncidentCard";
import { PostmortemDialog } from "./components/PostmortemDialog";
import { Timeline } from "./components/Timeline";
import { Workspace } from "./components/Workspace";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { changeEvents, mergeEvents } from "./lib/events";
import { clock } from "./lib/format";
import { firstIncident, incidentsAt, loadReplay, recoveredAt, type Replay, stateAt } from "./lib/replay";
import { Wordmark } from "./components/Wordmark";
import type { Filter } from "./lib/feed";
import { readRoute, type Route, routeURL, SEARCH, useRouteHistory, type View } from "./lib/route";

/** Playback speed: a few minutes of incident in under a minute. */
const SPEED = 10;
const TICK = 100;

/** A replay link must be https, or on this same site (kubexray play, the
 * demo): never another site over plain http, javascript: or data:. */
export function safeReplayURL(raw: string, base = location.href): URL | null {
  try {
    const url = new URL(raw, base);
    const sameOrigin = url.origin !== "null" && url.origin === new URL(base).origin;
    return url.protocol === "https:" || sameOrigin ? url : null;
  } catch {
    return null;
  }
}

/** Replay mode: plays a .kxr file, from a link or dropped in (ADR-0008). */
export function Player() {
  const params = useMemo(() => new URLSearchParams(location.search), []);
  const linked = params.get("replay");
  const linkedURL = useMemo(() => (linked ? safeReplayURL(linked) : null), [linked]);
  const [replay, setReplay] = useState<Replay | null>(null);
  const [source, setSource] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(linked && !linkedURL ? "Replay links must use https, or be on this site." : null);
  const [busy, setBusy] = useState(!!linkedURL);
  const [t, setT] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [drafting, setDrafting] = useState<Entry | null>(null);
  // ?view=changes or ?view=incidents opens on that view (kubexray.com links to it).
  const [view, setView] = useState<View>(() => {
    const v = readRoute().view;
    return v === "sources" ? "map" : v;
  });
  const [query, setQuery] = useState("");
  const [changesFilter, setChangesFilter] = useState<Filter>("all");
  const [focus, setFocus] = useState(0); // remounts the map with the selection

  const open = useCallback(
    async (blob: Blob, from: string | null) => {
      setBusy(true);
      setError(null);
      try {
        const r = await loadReplay(blob);
        const start = Date.parse(r.header.from);
        const end = Date.parse(r.header.to);
        // A link's moment and app belong to the linked replay only.
        const fromLink = from !== null && from === linked;
        const asked = fromLink ? Date.parse(params.get("t") ?? "") : NaN;
        const incident = firstIncident(r);
        // Opened on the change feed, the whole recording; else its first incident.
        const onFeed = fromLink && params.get("view") === "changes";
        setT(asked >= start && asked <= end ? asked : incident !== null && !onFeed ? Math.min(end, incident) : end);
        setReplay(r);
        setSource(from);
        setSelected(fromLink ? params.get("app") : null);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setBusy(false);
      }
    },
    [params, linked],
  );

  const fetchAndOpen = useCallback(
    async (raw: string) => {
      const url = safeReplayURL(raw);
      if (!url) {
        setError("Replay links must use https, or be on this site.");
        return;
      }
      setBusy(true);
      try {
        const resp = await fetch(url, { credentials: "same-origin" });
        if (!resp.ok) throw new Error(`Couldn't download the replay (${resp.status}).`);
        await open(await resp.blob(), raw);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        setBusy(false);
      }
    },
    [open],
  );

  // A replay linked in the URL loads once, at start.
  useEffect(() => {
    if (!linked || !linkedURL) return;
    let cancelled = false;
    fetch(linkedURL, { credentials: "same-origin" })
      .then((resp) => {
        if (!resp.ok) throw new Error(`Couldn't download the replay (${resp.status}).`);
        return resp.blob();
      })
      .then((blob) => (cancelled ? undefined : open(blob, linked)))
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
        setBusy(false);
      });
    return () => {
      cancelled = true;
    };
  }, [linked, linkedURL, open]);

  // Playback; it stops by itself at the end of the recording.
  const end = replay ? Date.parse(replay.header.to) : 0;
  const running = playing && t < end;
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => setT((now) => Math.min(now + TICK * SPEED, end)), TICK);
    return () => clearInterval(timer);
  }, [running, end]);

  const state = useMemo(() => (replay ? stateAt(replay, t) : null), [replay, t]);
  const incidents = useMemo(() => (replay ? incidentsAt(replay, t) : null), [replay, t]);
  const events = useMemo(() => (replay ? mergeEvents(replay.events, changeEvents(replay.changes)) : []), [replay]);
  const at = new Date(t).toISOString();

  // The address bar follows the view, the playhead and the selection, for
  // a linked replay; Back and Forward bring them back.
  const route = useMemo<Route>(() => ({ view, t: replay ? at : null, app: selected }), [view, replay, at, selected]);
  const onPop = useCallback(
    (r: Route) => {
      setPlaying(false);
      setView(r.view === "sources" ? "map" : r.view);
      if (r.t && replay) setT(Math.min(Date.parse(replay.header.to), Math.max(Date.parse(replay.header.from), Date.parse(r.t))));
      setSelected(r.app);
      setFocus((f) => f + 1);
    },
    [replay],
  );
  // A replay opened from a link (or the demo) is named in the address bar,
  // so the address is always a link to what's shown; a dropped file can't be.
  useEffect(() => {
    if (!replay || !source || new URLSearchParams(location.search).get("replay") === source) return;
    const url = new URL(location.href);
    url.search = new URLSearchParams({ replay: source }).toString();
    history.replaceState(null, "", url.href);
  }, [replay, source]);
  useRouteHistory(route, onPop, { enabled: !!replay && !!source, liveSteps: false });

  if (!replay || !state) {
    return (
      <main
        className="message landing"
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          e.preventDefault();
          const file = e.dataTransfer.files[0];
          if (file) void open(file, null);
        }}
      >
        <Wordmark />
        <p className="tagline">The flight recorder for your Kubernetes cluster. Watch an incident as it happened: what changed, what broke, and why.</p>
        <div className="drop">
          <p>Drop a replay (.kxr) here, or</p>
          <label className="pick">
            Choose a file
            <input type="file" accept=".kxr,application/gzip" onChange={(e) => e.target.files?.[0] && void open(e.target.files[0], null)} />
          </label>
          {import.meta.env.VITE_DEMO === "1" && (
            <button type="button" className="pick" onClick={() => void fetchAndOpen(new URL("demo.kxr", location.href).href)}>
              Watch a demo incident
            </button>
          )}
        </div>
        {busy && <p>Loading the replay…</p>}
        {error && <p className="error">{error}</p>}
        <p className="small">
          Replays are made with <a href="https://github.com/kubexray/kubexray">kubexray</a>. They play entirely in your browser; nothing is uploaded.
        </p>
      </main>
    );
  }

  const linkTo = (app: string | null, moment = at) => {
    if (!source) return null;
    const p = new URLSearchParams({ replay: source });
    return routeURL({ view: "map", t: moment, app }, `${location.origin}${location.pathname}?${p}`);
  };

  const h = replay.header;
  // A draft links to the replay only when readers can open it (kubexray
  // play's link is on localhost), opening just before the change or the
  // first symptom, wherever the author paused.
  const draft = (entry: Entry) => {
    const back = recoveredAt(replay, entry.incidents.map((i) => i.app), t);
    const shareable = location.protocol === "https:" && !!source?.startsWith("https://");
    const starts = [entry.group?.cause.at ?? entry.incidents[0]?.causes?.[0]?.at, ...entry.incidents.map((i) => i.since)].map((s) => Date.parse(s ?? "")).filter(Number.isFinite);
    const opens = starts.length ? Math.max(Date.parse(h.from), Math.min(...starts) - 10_000) : t;
    return {
      entry,
      cluster: h.cluster,
      at,
      recovered: back === null ? null : { at: new Date(back).toISOString(), exact: true },
      events,
      eventsFrom: null,
      link: shareable ? linkTo(entry.incidents[0]?.app ?? null, new Date(opens).toISOString()) : null,
    };
  };
  const close = () => {
    setPlaying(false);
    setReplay(null);
  };
  const shown = (
    <FeedSets.Provider value={replay.feed}>
      <Workspace
        key={focus}
        search={query}
        page={
          view === "changes" ? (
            <ChangesView
              key={changesFilter}
              sets={replay.feed}
              now={t}
              from={Date.parse(h.from)}
              onShowAll={() => {
                setPlaying(false);
                setT(end);
              }}
              initialFilter={changesFilter}
              query={query}
              live={false}
              canRewindTo={(ts) => Date.parse(ts) >= Date.parse(h.from)}
              onRewind={(ts, id) => {
                setPlaying(false);
                setT(Date.parse(ts));
                setSelected(id);
                setFocus((f) => f + 1);
                setView("map");
              }}
            />
          ) : view === "incidents" ? (
            <IncidentsView
              incidents={incidents?.incidents ?? []}
              groups={incidents?.groups ?? []}
              now={t}
              live={false}
              query={query}
              broke={replay.feed.filter((s) => s.verdict === "broke" && Date.parse(s.at) <= t).length}
              onShow={(id) => {
                setSelected(id);
                setFocus((f) => f + 1);
                setView("map");
              }}
              onBroke={() => {
                setChangesFilter("broke");
                setView("changes");
              }}
            />
          ) : undefined
        }
        header={(v) => (
          <Header
            view={v}
            status="replay"
            label={`${h.cluster}${h.anonymized ? " (anonymized)" : ""} · ${clock(h.from)} to ${clock(h.to)}`}
            nav={{
              active: view,
              onChange: (v) => {
                if (v === "changes") setChangesFilter("all");
                setView(v);
              },
              incidents: incidents?.incidents.length ?? 0,
            }}
            search={SEARCH[view] ? { value: query, onChange: setQuery, placeholder: SEARCH[view] ?? "" } : undefined}
            menu={[
              {
                label: "Open another replay",
                note: "A .kxr file or link",
                onSelect: () => {
                  setPlaying(false);
                  setReplay(null);
                },
              },
            ]}
          />
        )}
        onClearSearch={() => setQuery("")}
        state={state}
        incidents={incidents?.incidents ?? []}
        groups={incidents?.groups ?? []}
        events={events}
        at={at}
        initialSelection={selected}
        onSelectionChange={setSelected}
        canRewindTo={(ts) => Date.parse(ts) >= Date.parse(h.from)}
        onRewind={(ts) => {
          setPlaying(false);
          setT(Date.parse(ts) + 1000);
        }}
        actions={(entry) => (
          <button type="button" onClick={() => setDrafting(entry)}>
            Post-mortem draft
          </button>
        )}
        timeline={(compare, ghost) => (
          <Timeline
            compare={compare}
            ghost={ghost}
            bounds={h}
            at={at}
            mode="replay"
            incidents={incidents?.incidents ?? []}
            onFocus={(app, ts) => {
              setPlaying(false);
              setT(Date.parse(ts));
              setSelected(app);
              setFocus((f) => f + 1);
            }}
            playing={running}
            onPlay={() => {
              if (t >= end) setT(Date.parse(h.from));
              setPlaying(!running);
            }}
            onSeek={(ts) => {
              setPlaying(false);
              if (ts) setT(Date.parse(ts));
            }}
            link={source ? () => linkTo(selected) : undefined}
          />
        )}
      />
      {drafting && <PostmortemDialog draft={() => draft(drafting)} onClose={() => setDrafting(null)} />}
    </FeedSets.Provider>
  );
  return <ErrorBoundary what="this replay" back={{ label: "Open another replay", onClick: close }}>{shown}</ErrorBoundary>;
}
