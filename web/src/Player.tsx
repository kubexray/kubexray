import { useCallback, useEffect, useMemo, useState } from "react";
import { Header } from "./components/Header";
import { Timeline } from "./components/Timeline";
import { Workspace } from "./components/Workspace";
import { changeEvents, mergeEvents } from "./lib/events";
import { clock } from "./lib/format";
import { firstIncident, incidentsAt, loadReplay, type Replay, stateAt } from "./lib/replay";

/** Playback speed: a few minutes of incident in under a minute. */
const SPEED = 10;
const TICK = 100;

/** A replay link must be https, or a path on this site (kubexray play). */
export function safeReplayURL(raw: string, base = location.href): URL | null {
  try {
    const url = new URL(raw, base);
    const sameOrigin = url.origin === new URL(base).origin;
    return url.protocol === "https:" || (sameOrigin && raw.startsWith("/") && !raw.startsWith("//")) ? url : null;
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
  const [error, setError] = useState<string | null>(linked && !linkedURL ? "Replay links must start with https://." : null);
  const [busy, setBusy] = useState(!!linkedURL);
  const [t, setT] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

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
        setError("Replay links must start with https://.");
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
        <h1>
          kube<span>xray</span>
        </h1>
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

  const permalink = () => {
    if (!source) return;
    const p = new URLSearchParams({ replay: source, t: at });
    if (selected) p.set("app", selected);
    const link = `${location.origin}${location.pathname}?${p}`;
    navigator.clipboard.writeText(link).then(
      () => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      },
      () => prompt("Copy this link", link),
    );
  };

  const h = replay.header;
  return (
    <Workspace
      header={(view) => <Header view={view} status="replay" label={`${h.cluster}${h.anonymized ? " (anonymized)" : ""} · ${clock(h.from)} to ${clock(h.to)}`} />}
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
      timeline={
        <Timeline
          bounds={h}
          at={at}
          mode="replay"
          events={events}
          incidents={incidents?.incidents ?? []}
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
      }
    />
  );
}
