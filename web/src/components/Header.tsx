import { useState } from "react";
import { applyTheme, savedTheme, type Theme } from "../lib/theme";
import type { StreamStatus } from "../lib/useStream";
import { allWorkloads, type NamespaceView, workloadHealth } from "../lib/view";

const STATUS: Record<StreamStatus | "replay", string> = {
  connecting: "Connecting…",
  live: "Live",
  reconnecting: "Reconnecting…",
  "signed-out": "Signed out",
  replay: "Replay",
};

/** The three views: the map, the change feed, and incidents. */
export type View = "map" | "changes" | "incidents";

interface Props {
  view: NamespaceView[];
  status: StreamStatus | "replay";
  rewound?: boolean;
  /** What's being shown, e.g. which cluster a replay is from. */
  label?: string;
  nav?: { active: View; onChange: (v: View) => void; incidents: number };
  search?: { value: string; onChange: (q: string) => void };
}

const VIEWS: { id: View; label: string }[] = [
  { id: "map", label: "Map" },
  { id: "changes", label: "Changes" },
  { id: "incidents", label: "Incidents" },
];

export function Header({ view, status, rewound = false, label, nav, search }: Props) {
  const ws = allWorkloads(view);
  const count = (h: string) => ws.filter(({ w }) => workloadHealth(w) === h).length;
  const pods = ws.reduce((n, { w }) => n + w.pods.length, 0);
  const bad = count("crit");
  const updating = count("warn");
  return (
    <header className="header">
      <div className="brand">
        <h1>
          kube<span>xray</span>
        </h1>
        <span className={`status ${rewound ? "rewound" : status}`}>{rewound ? "Rewound" : STATUS[status]}</span>
        {label && <span className="label">{label}</span>}
      </div>
      {nav && (
        <nav className="views" aria-label="Views">
          {VIEWS.map((v) => (
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
      <div className="counts" aria-live="polite">
        <span>
          <i className="dot ok" />
          <b>{count("ok")}</b> healthy apps
        </span>
        {updating > 0 && (
          <span>
            <b>{updating}</b> updating
          </span>
        )}
        <span className={bad ? "bad" : ""}>
          <i className="dot crit" />
          <b>{bad}</b> need attention
        </span>
        <span>
          <b>{pods}</b> pods
        </span>
      </div>
      <ThemeToggle />
    </header>
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
