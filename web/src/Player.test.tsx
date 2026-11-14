// The player in a DOM: a replay is untrusted, and a bad one must end in a
// plain message, never a blank page.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import demo from "../pages-public/demo.kxr?inline";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { Player } from "./Player";

const T = (s: number) => new Date(Date.UTC(2026, 9, 23, 14, 0, s)).toISOString();

async function gzip(text: string): Promise<Blob> {
  const body = new Response(text).body;
  if (!body) throw new Error("no body");
  return new Blob([await new Response(body.pipeThrough(new CompressionStream("gzip"))).arrayBuffer()]);
}
const kxr = async (rows: unknown[]) => new File([await gzip(rows.map((r) => JSON.stringify(r)).join("\n"))], "bad.kxr");

const env = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean; ResizeObserver?: unknown };
const was = { act: env.IS_REACT_ACT_ENVIRONMENT, ro: env.ResizeObserver };
beforeAll(() => {
  env.IS_REACT_ACT_ENVIRONMENT = true;
  env.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});
afterAll(() => {
  env.IS_REACT_ACT_ENVIRONMENT = was.act;
  env.ResizeObserver = was.ro;
});

let root: Root | null = null;
let el: HTMLElement;
const render = (ui: React.ReactNode) => {
  el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  act(() => root?.render(ui));
};
afterEach(() => {
  act(() => root?.unmount());
  el.remove();
  root = null;
  vi.restoreAllMocks();
});

/** Opens a file the way choosing one in the page does. */
async function choose(file: File) {
  const input = el.querySelector<HTMLInputElement>('input[type="file"]');
  if (!input) throw new Error("no file input");
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  await act(async () => {
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
}
const settle = (check: () => void) =>
  vi.waitFor(async () => {
    await act(async () => {});
    check();
  });

describe("Player", () => {
  it("says what's wrong with a replay whose node has no status", async () => {
    render(<Player />);
    await choose(
      await kxr([
        { v: 1, type: "header", from: T(0), to: T(60), cluster: "bad", anonymized: false, kubexray: "dev" },
        // A pod without a status: the map reads status on every card.
        { v: 1, type: "snapshot", seq: 1, ts: T(0), nodes: [{ id: "Pod/shop/a", kind: "Pod", ns: "shop", name: "a", created: T(0) }], edges: [] },
      ]),
    );
    await settle(() => expect(el.querySelector(".error")?.textContent).toBe("In the snapshot, node Pod/shop/a has an invalid status."));
    // Still the landing page, ready for another file.
    expect(el.querySelector('input[type="file"]')).not.toBeNull();
  });

  it("rejects a delta that would break the map later", async () => {
    render(<Player />);
    await choose(
      await kxr([
        { v: 1, type: "header", from: T(0), to: T(60), cluster: "bad", anonymized: false, kubexray: "dev" },
        { v: 1, type: "snapshot", seq: 1, ts: T(0), nodes: [], edges: [] },
        { v: 1, type: "delta", seq: 2, ts: T(5), ops: [{ op: "upsertNode", node: { id: "Deployment/shop/x", kind: "Deployment", ns: "shop", name: "x", created: T(0), status: { ready: true, replicas: "lots" } } }] },
      ]),
    );
    await settle(() => expect(el.querySelector(".error")?.textContent).toMatch(/node Deployment\/shop\/x has an invalid status/));
  });

  it("plays a good replay", async () => {
    render(<Player />);
    const bytes = Uint8Array.from(atob(demo.slice(demo.indexOf(",") + 1)), (c) => c.charCodeAt(0)); // a data: URL
    await choose(new File([bytes], "demo.kxr"));
    await settle(() => expect(el.querySelector('[aria-label="Cluster map"]')).not.toBeNull());
    expect(el.querySelectorAll(".node.workload").length).toBeGreaterThan(0);
  });
});

describe("ErrorBoundary", () => {
  function Broken(): never {
    throw new Error("status is undefined");
  }

  it("shows a plain message with a way back instead of a blank page", () => {
    vi.spyOn(console, "error").mockImplementation(() => {}); // React reports the error too
    const back = vi.fn();
    render(
      <ErrorBoundary what="this replay" back={{ label: "Open another replay", onClick: back }}>
        <Broken />
      </ErrorBoundary>,
    );
    expect(el.textContent).toContain("Something went wrong showing this replay.");
    expect(el.textContent).toContain("status is undefined");
    const button = [...el.querySelectorAll("button")].find((b) => b.textContent === "Open another replay");
    act(() => button?.click());
    expect(back).toHaveBeenCalledOnce();
  });
});
