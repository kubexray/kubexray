// What's on screen, in the address bar: the view, the moment and the
// selected app (?view=changes&t=…&app=…), so a link opens it and Back
// moves between views (ADR-0011's alert links use t and app).
import { useEffect, useEffectEvent, useRef } from "react";

export type View = "map" | "changes" | "incidents" | "sources";
const VIEWS: readonly View[] = ["map", "changes", "incidents", "sources"];

/** What the search box searches, by view; Sources has none. */
export const SEARCH: Partial<Record<View, string>> = {
  map: "Search apps and images…",
  changes: "Search changes and people…",
  incidents: "Search incidents…",
};

export interface Route {
  view: View;
  /** The moment shown, or null for live (or, in a replay, not said). */
  t: string | null;
  app: string | null;
}

export function readRoute(search = location.search): Route {
  const p = new URLSearchParams(search);
  const view = p.get("view") as View | null;
  const t = Date.parse(p.get("t") ?? "");
  return {
    view: view && VIEWS.includes(view) ? view : "map",
    t: Number.isFinite(t) ? new Date(t).toISOString() : null,
    app: p.get("app") || null,
  };
}

/** The address of a route, keeping other parameters (a replay's link). */
export function routeURL(r: Route, base: string = location.href): string {
  const url = new URL(base);
  const p = url.searchParams;
  for (const [k, v] of [["view", r.view === "map" ? null : r.view], ["t", r.t], ["app", r.app]] as const) {
    if (v) p.set(k, v);
    else p.delete(k);
  }
  url.search = p.toString();
  return url.href;
}

/** Keeps the address bar in step with the route. A new view (or, live,
 * going back to live or away from it) is a step Back returns from; moving
 * through time or selecting replaces the current step, once it settles (a
 * replay playing moves every tenth of a second). Back and Forward call
 * onPop with the route they land on. Off, the address bar is left alone. */
export function useRouteHistory(route: Route, onPop: (r: Route) => void, { enabled = true, liveSteps = true } = {}) {
  const last = useRef<Route | null>(null);
  const { view, t, app } = route;
  useEffect(() => {
    if (!enabled) return;
    const prev = last.current ?? readRoute();
    const next = { view, t, app };
    last.current = next;
    const write = (step: boolean) => {
      const href = routeURL(next);
      if (href === location.href) return;
      if (step) history.pushState(null, "", href);
      else history.replaceState(null, "", href);
    };
    if (prev.view !== view || (liveSteps && (prev.t === null) !== (t === null))) return write(true);
    const timer = setTimeout(() => write(false), 400);
    return () => clearTimeout(timer);
  }, [enabled, liveSteps, view, t, app]);
  const pop = useEffectEvent(() => {
    const r = readRoute();
    last.current = r;
    onPop(r);
  });
  useEffect(() => {
    const h = () => pop();
    addEventListener("popstate", h);
    return () => removeEventListener("popstate", h);
  }, []);
}
