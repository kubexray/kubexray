import { useCallback, useEffect, useMemo, useState } from "react";
import { FeedSets } from "./lib/feedContext";
import { ChangesView } from "./components/ChangesView";
import { Header, type View } from "./components/Header";
import { IncidentsView } from "./components/IncidentsView";
import { ReleasesView } from "./components/ReleasesView";
import { hasReleases } from "./lib/helm";
import type { Entry } from "./components/IncidentCard";
import { PostmortemDialog } from "./components/PostmortemDialog";
import { Timeline } from "./components/Timeline";
import { Workspace } from "./components/Workspace";
import { changeEvents, mergeEvents } from "./lib/events";
import { clock } from "./lib/format";
import { firstIncident, incidentsAt, loadReplay, recoveredAt, type Replay, stateAt } from "./lib/replay";
import { Wordmark } from "./components/Wordmark";

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
  const [copied, setCopied] = useState(false);
  const [drafting, setDrafting] = useState<Entry | null>(null);
  // ?view=changes or ?view=incidents opens on that view (kubexray.com links to it).
  const [view, setView] = useState<View>(() => (["changes", "incidents", "releases"].includes(params.get("view") ?? "") ? (params.get("view") as View) : "map"));
  const [query, setQuery] = useState("");
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
        setT(asked >= start && asked <= end ? asked : incident !== null ? Math.min(end, incident) : end);
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
    const p = new URLSearchParams({ replay: source, t: moment });
    if (app) p.set("app", app);
    return `${location.origin}${location.pathname}?${p}`;
  };
  const permalink = () => {
    const link = linkTo(selected);
    if (!link) return;
    navigator.clipboard.writeText(link).then(
      () => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      },
      () => prompt("Copy this link", link),
    );
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
  return (
    <FeedSets.Provider value={replay.feed}>
      <Workspace
        key={focus}
        search={query}
        page={
          view === "changes" ? (
            <ChangesView
              sets={replay.feed}
              now={end}
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
              sets={replay.feed}
              now={t}
              onShow={(id) => {
                setSelected(id);
                setFocus((f) => f + 1);
                setView("map");
              }}
            />
          ) : view === "releases" ? (
            <ReleasesView
              state={state}
              sets={replay.feed}
              now={t}
              onShow={(id) => {
                setSelected(id);
                setFocus((f) => f + 1);
                setView("map");
              }}
              onRewind={(id, ts) => {
                setPlaying(false);
                setT(Date.parse(ts));
                setSelected(id);
                setFocus((f) => f + 1);
                setView("map");
              }}
            />
          ) : undefined
        }
        header={(v) => (
          <Header
            view={v}
            status="replay"
            label={`${h.cluster}${h.anonymized ? " (anonymized)" : ""} · ${clock(h.from)} to ${clock(h.to)}`}
            nav={{ active: view, onChange: setView, incidents: incidents?.incidents.length ?? 0, releases: hasReleases(state), incidentApps: incidents?.incidents.map((i) => i.app) }}
            search={{ value: query, onChange: setQuery }}
          />
        )}
        notice={
          <div className="replay-bar">
            <span>You're watching a recording. Use the timeline or press Play; links keep the moment and the selected app.</span>
            {source && (
              <button type="button" onClick={permalink}>
                {copied ? "Link copied" : "Copy link to this moment"}
              </button>
            )}
            <button
              type="button"
              onClick={() => {
                setPlaying(false);
                setReplay(null);
              }}
            >
              Open another replay
            </button>
          </div>
        }
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
          />
        )}
      />
      {drafting && <PostmortemDialog draft={() => draft(drafting)} onClose={() => setDrafting(null)} />}
    </FeedSets.Provider>
  );
}
