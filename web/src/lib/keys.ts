// Keyboard: one listener for the page, so a key does one thing. Escape
// closes the innermost thing open (a menu, a pinned card, the log full
// screen) before the selection; "/" goes to search; "l" back to live.
import { useEffect, useEffectEvent } from "react";

type Handler = { run: () => void };
const escapes: Handler[] = [];
const shortcuts = new Map<string, Handler[]>();

const typing = (t: EventTarget | null) => t instanceof HTMLElement && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName));
// A dialog handles its own keys (Escape closes it).
const modal = () => !!document.querySelector("dialog[open]");

/** The page's key handling; exported for tests. */
export function onKey(e: KeyboardEvent) {
  if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || modal()) return;
  if (e.key === "Escape") {
    // In a field, Escape leaves it (a search box clears itself first).
    if (typing(e.target)) {
      if (!(e.target as HTMLInputElement).value) (e.target as HTMLElement).blur();
      return;
    }
    const top = escapes.at(-1);
    if (top) {
      e.preventDefault();
      top.run();
    }
    return;
  }
  if (typing(e.target)) return;
  const h = shortcuts.get(e.key.toLowerCase())?.at(-1);
  if (h) {
    e.preventDefault();
    h.run();
  }
}

let installed = false;
function install() {
  if (installed) return;
  installed = true;
  addEventListener("keydown", onKey);
}

function useRegistered(list: Handler[], active: boolean, run: () => void) {
  const fire = useEffectEvent(run);
  useEffect(() => {
    if (!active) return;
    install();
    // Registered when it opens: what opened last is innermost.
    const h = { run: () => fire() };
    list.push(h);
    return () => {
      const i = list.indexOf(h);
      if (i >= 0) list.splice(i, 1);
    };
  }, [list, active]);
}

/** Escape runs `close` while `open`, unless something opened since takes it. */
export function useEscape(open: boolean, close: () => void) {
  useRegistered(escapes, open, close);
}

/** A one-key shortcut, ignored while typing or with a dialog open. */
export function useShortcut(key: string, run: (() => void) | undefined) {
  const k = key.toLowerCase();
  let list = shortcuts.get(k);
  if (!list) shortcuts.set(k, (list = []));
  useRegistered(list, !!run, () => run?.());
}
