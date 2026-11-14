// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
import { useEscape, useShortcut } from "./keys";

const press = (key: string, target: EventTarget = document.body) => act(() => target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true })));

describe("keys", () => {
  let root: ReturnType<typeof createRoot> | null = null;
  afterEach(() => act(() => root?.unmount()));

  function mount(el: React.ReactElement) {
    const box = document.createElement("div");
    document.body.append(box);
    root = createRoot(box);
    act(() => root!.render(el));
  }

  it("closes the innermost thing first, one per Escape", () => {
    const closed: string[] = [];
    function Probe({ menu }: { menu: boolean }) {
      useEscape(true, () => closed.push("selection"));
      useEscape(menu, () => closed.push("menu"));
      return null;
    }
    mount(createElement(Probe, { menu: false }));
    act(() => root!.render(createElement(Probe, { menu: true })));
    press("Escape");
    expect(closed).toEqual(["menu"]);
    act(() => root!.render(createElement(Probe, { menu: false })));
    press("Escape");
    expect(closed).toEqual(["menu", "selection"]);
  });

  it("runs shortcuts, but not while typing", () => {
    let live = 0;
    function Probe() {
      useShortcut("l", () => live++);
      return null;
    }
    mount(createElement(Probe));
    press("L");
    expect(live).toBe(1);
    const input = document.createElement("input");
    document.body.append(input);
    press("l", input);
    expect(live).toBe(1);
  });
});
