import { useRef, useState } from "react";
import type { Me } from "../lib/api";
import { hm } from "../lib/format";
import { useEscape } from "../lib/keys";
import type { View } from "../lib/route";
import { applyTheme, savedTheme, type Theme } from "../lib/theme";
import type { StreamStatus } from "../lib/useStream";
import { allWorkloads, incidentCount, type NamespaceView, workloadHealth } from "../lib/view";
import { Wordmark } from "./Wordmark";

export type { View } from "../lib/route";

interface Props {
  view: NamespaceView[];
  status: StreamStatus | "replay";
  rewound?: boolean;
  /** What's being shown, e.g. which cluster a replay is from. */
  label?: string;
  /** incidents: how many there are at the moment shown, from the same list
   * the Incidents view shows; the count is the way into it. */
  nav?: { active: View; onChange: (v: View) => void; incidents: number };
  /** Hidden on views it doesn't search. */
  search?: { value: string; onChange: (q: string) => void; placeholder: string };
  /** Who is signed in, with sign-in (ADR-0013). */
  me?: Me | null;
  /** Not up to date: the stream is reconnecting or the API isn't answering.
   * When it last was, if ever. */
  stale?: { since: string | null } | null;
  /** What isn't needed at 3 a.m., in a menu: Sources, another replay. */
  menu?: MenuItem[];
}

export interface MenuItem {
  label: string;
  note?: string;
  onSelect: () => void;
}

const VIEWS: { id: View; label: string }[] = [
  { id: "map", label: "Map" },
  { id: "changes", label: "Changes" },
  { id: "incidents", label: "Incidents" },
];

export function Header({ view, status, rewound = false, label, nav, search, me, stale, menu = [] }: Props) {
  const updating = allWorkloads(view).filter(({ w }) => workloadHealth(w) === "warn").length;
  const n = nav?.incidents ?? 0;
  const past = rewound || status === "replay";
  return (
    <header className="header">
      <div className="brand">
        <Wordmark />
        {/* Live is the normal state, and the timeline says so: only the others show. */}
        {(rewound || status === "replay") && <span className={`status ${rewound ? "rewound" : status}`}>{rewound ? "Rewound" : "Replay"}</span>}
        {label && <span className="label">{label}</span>}
      </div>
      {nav && (
        <nav className="views" aria-label="Views">
          {VIEWS.map((v) => (
            <button key={v.id} type="button" aria-current={nav.active === v.id ? "page" : undefined} className={nav.active === v.id ? "on" : ""} onClick={() => nav.onChange(v.id)}>
              {v.label}
              {v.id === "incidents" && n > 0 && (
                <span className="views-count" aria-label={incidentCount(n)}>
                  {n}
                </span>
              )}
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
          <span className="sr-only">{search.placeholder}</span>
          <input type="search" value={search.value} placeholder={search.placeholder} onChange={(e) => search.onChange(e.target.value)} />
          <kbd aria-hidden="true">/</kbd>
        </label>
      )}
      {/* How the cluster is doing, in one line: the map shows the rest. Out
          of date, it says so instead: an old "no incidents" isn't news. */}
      <div className="health-slot" aria-live="polite">
        {status === "connecting" || status === "reconnecting" || stale ? (
          <span className="health stale">
            <i className="dot stale" aria-hidden="true" />
            {stale?.since ? `Last update ${hm(stale.since)} · reconnecting` : "Connecting…"}
          </span>
        ) : n > 0 ? (
          <button type="button" className="health bad" onClick={() => nav?.onChange("incidents")} disabled={!nav || nav.active === "incidents"}>
            <i className="dot crit" aria-hidden="true" />
            <b>{incidentCount(n)}</b>
          </button>
        ) : (
          <span className="health">
            <svg aria-hidden="true" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="m5 12.5 4.5 4.5L19 7.5" />
            </svg>
            No incidents{past ? " at this moment" : ""}
          </span>
        )}
        {updating > 0 && !stale && status !== "reconnecting" && <span className="updating">· {updating} updating</span>}
      </div>
      <ThemeToggle />
      {(me?.email || menu.length > 0) && <Menu me={me} items={menu} />}
    </header>
  );
}

/** What isn't needed at 3 a.m., and the account. */
function Menu({ me, items }: { me?: Me | null; items: MenuItem[] }) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  useEscape(open, () => setOpen(false));
  const who = me?.name || me?.email || "";
  return (
    <div
      className="hmenu"
      ref={box}
      onBlur={(e) => {
        if (!box.current?.contains(e.relatedTarget as globalThis.Node | null)) setOpen(false);
      }}
    >
      <button type="button" className={`hmenu-btn${who ? " who" : ""}`} aria-haspopup="menu" aria-expanded={open} aria-label="Menu" title={me?.email} onClick={() => setOpen(!open)}>
        {who ? (
          who.slice(0, 1).toUpperCase()
        ) : (
          <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="currentColor">
            <circle cx="5" cy="12" r="1.8" />
            <circle cx="12" cy="12" r="1.8" />
            <circle cx="19" cy="12" r="1.8" />
          </svg>
        )}
      </button>
      {open && (
        <div className="hmenu-list" role="menu">
          {who && (
            <p className="hmenu-who">
              Signed in as <b>{who}</b>
            </p>
          )}
          {items.map((it) => (
            <button
              key={it.label}
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                it.onSelect();
              }}
            >
              {it.label}
              {it.note && <small>{it.note}</small>}
            </button>
          ))}
          {me?.email && (
            <a role="menuitem" href={me.signOut ?? "/auth/logout"}>
              Sign out
            </a>
          )}
        </div>
      )}
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
