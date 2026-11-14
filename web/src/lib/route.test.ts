// @vitest-environment jsdom
import { act, createElement, useState } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { readRoute, type Route, routeURL, useRouteHistory } from "./route";

describe("routes in the address bar", () => {
  it("reads the view, the moment and the app, ignoring what it doesn't know", () => {
    expect(readRoute("?view=changes&t=2026-10-28T11:26:00Z&app=Deployment%2Fshop%2Fcheckout")).toEqual({ view: "changes", t: "2026-10-28T11:26:00.000Z", app: "Deployment/shop/checkout" });
    expect(readRoute("?view=nope&t=yesterday")).toEqual({ view: "map", t: null, app: null });
  });

  it("writes them, keeping a replay's link", () => {
    const url = routeURL({ view: "incidents", t: "2026-10-28T11:26:00.000Z", app: null }, "https://kubexray.com/play/?replay=demo.kxr&app=x");
    expect(url).toBe("https://kubexray.com/play/?replay=demo.kxr&view=incidents&t=2026-10-28T11%3A26%3A00.000Z");
    expect(routeURL({ view: "map", t: null, app: null }, "https://h/?view=changes")).toBe("https://h/");
  });
});

describe("history", () => {
  let root: ReturnType<typeof createRoot> | null = null;
  afterEach(() => {
    act(() => root?.unmount());
    vi.useRealTimers();
  });

  it("makes a new view a step Back returns from, and brings it back", async () => {
    vi.useFakeTimers();
    history.replaceState(null, "", "/");
    let set: (r: Route) => void = () => {};
    const popped: Route[] = [];
    function Probe() {
      const [r, setR] = useState<Route>({ view: "map", t: null, app: null });
      set = setR;
      useRouteHistory(r, (p) => popped.push(p));
      return null;
    }
    const el = document.createElement("div");
    root = createRoot(el);
    act(() => root!.render(createElement(Probe)));
    const before = history.length;
    act(() => set({ view: "changes", t: null, app: null }));
    expect(location.search).toBe("?view=changes");
    expect(history.length).toBe(before + 1);
    // Selecting replaces the step, once it settles.
    act(() => set({ view: "changes", t: null, app: "Deployment/shop/api" }));
    act(() => vi.advanceTimersByTime(500));
    expect(history.length).toBe(before + 1);
    expect(new URLSearchParams(location.search).get("app")).toBe("Deployment/shop/api");
    vi.useRealTimers();
    history.back();
    await new Promise((r) => addEventListener("popstate", r, { once: true }));
    expect(popped.at(-1)?.view).toBe("map");
  });
});
