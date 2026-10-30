import { useState } from "react";
import type { Me } from "../lib/api";
import { applyTheme, savedTheme, type Theme } from "../lib/theme";
import type { StreamStatus } from "../lib/useStream";
import { allWorkloads, type NamespaceView, workloadHealth } from "../lib/view";
import { Wordmark } from "./Wordmark";

const STATUS: Record<StreamStatus | "replay", string> = {
  connecting: "Connecting…",
  live: "Live",
  reconnecting: "Reconnecting…",
  "signed-out": "Signed out",
  replay: "Replay",
};

/** The three views: the map, the change feed, and incidents. */
export type View = "map" | "incidents" | "changes" | "releases" | "sources";

interface Props {
  view: NamespaceView[];
  status: StreamStatus | "replay";
  rewound?: boolean;
  /** What's being shown, e.g. which cluster a replay is from. */
  label?: string;
  /** sources: show the Sources view (live only). incidentApps: apps with an
   * incident, which need attention even when their pods look fine (a
   * signal such as failing requests, ADR-0016). */
  nav?: { active: View; onChange: (v: View) => void; incidents: number; releases?: boolean; sources?: boolean; incidentApps?: string[] };
  search?: { value: string; onChange: (q: string) => void };
  /** Who is signed in, with sign-in (ADR-0013). */
  me?: Me | null;
}

// In the order they're reached for: what's there, what's broken, what
// changed, how Helm changed it, and where the signals come from.
const VIEWS: { id: View; label: string }[] = [
  { id: "map", label: "Map" },
  { id: "incidents", label: "Incidents" },
  { id: "changes", label: "Changes" },
  { id: "releases", label: "Releases" },
  { id: "sources", label: "Sources" },
];

export function Header({ view, status, rewound = false, label, nav, search, me }: Props) {
  const ws = allWorkloads(view);
  const count = (h: string) => ws.filter(({ w }) => workloadHealth(w) === h).length;
  const incidentApps = new Set(nav?.incidentApps ?? []);
  const crit = ws.filter(({ w }) => workloadHealth(w) === "crit").map(({ w }) => w.node.id);
  const bad = new Set([...crit, ...incidentApps]).size;
  const updating = count("warn");
  return (
    <header className="header">
      <div className="brand">
        <Wordmark />
        {/* Live is the normal state, and the timeline says so: only the others show. */}
        {(rewound || status !== "live") && <span className={`status ${rewound ? "rewound" : status}`}>{rewound ? "Rewound" : STATUS[status]}</span>}
        {label && <span className="label">{label}</span>}
      </div>
      {nav && (
        <nav className="views" aria-label="Views">
          {VIEWS.filter((v) => (v.id !== "releases" || nav.releases) && (v.id !== "sources" || nav.sources)).map((v) => (
            <button key={v.id} type="button" aria-current={nav.active === v.id ? "page" : undefined} className={nav.active === v.id ? "on" : ""} onClick={() => nav.onChange(v.id)}>
              {v.label}
              {v.id === "incidents" && nav.incidents > 0 && <span className="views-count">{nav.incidents}</span>}
            </button>
          ))}
        </nav>
      )}
      {search && (
        <label className="search">
          <svg aria-hidden="true" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
            <circle cx="11" cy="11" r="7" />
            <path d="m20 20-3.5-3.5" />
          </svg>
          <span className="sr-only">Search apps and changes</span>
          <input type="search" value={search.value} placeholder="Search apps, people, images…" onChange={(e) => search.onChange(e.target.value)} />
        </label>
      )}
      {/* How the cluster is doing, in one line: the map shows the rest. */}
      <div className={`health${bad ? " bad" : ""}`} aria-live="polite">
        {bad ? (
          <>
            <i className="dot crit" aria-hidden="true" />
            <span>
              <b>{bad}</b> need{bad === 1 ? "s" : ""} attention
            </span>
          </>
        ) : (
          <>
            <svg aria-hidden="true" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="m5 12.5 4.5 4.5L19 7.5" />
            </svg>
            No incidents{rewound || status === "replay" ? " at this moment" : ""}
          </>
        )}
        {updating > 0 && <span className="updating">· {updating} updating</span>}
      </div>
      <ThemeToggle />
      {me?.email && <Account me={me} />}
    </header>
  );
}

/** Who is signed in, and Sign out. */
function Account({ me }: { me: Me }) {
  const who = me.name || me.email || "";
  return (
    <div className="account">
      <span className="account-avatar" aria-hidden="true">
        {who.slice(0, 1).toUpperCase()}
      </span>
      <span className="account-who" title={me.email}>
        {who}
      </span>
      <a className="account-out" href={me.signOut ?? "/auth/logout"}>
        Sign out
      </a>
    </div>
  );
}

/** Switches between the light (default) and dark looks. */
function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(savedTheme);
  const next: Theme = theme === "light" ? "dark" : "light";
  return (
    <button
      type="button"
      className="theme-toggle"
      aria-label={`Switch to ${next} mode`}
      title={`Switch to ${next} mode`}
      onClick={() => {
        applyTheme(next);
        setTheme(next);
      }}
    >
      {theme === "light" ? (
        <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z" />
        </svg>
      ) : (
        <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
          <circle cx="12" cy="12" r="4" />
          <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
        </svg>
      )}
    </button>
  );
}
