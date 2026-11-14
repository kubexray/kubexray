// What the map lights up: the chain under the pointer, the current
// incident's broken path and the selection. It changes on every hover, so it
// lives outside React state: each card subscribes to the one class it shows,
// and only the cards whose class changes render again.
import { createContext, useContext, useSyncExternalStore } from "react";

export interface Highlight {
  /** Cards lit by hovering, or null. */
  lit: Set<string> | null;
  hovered: string | null;
  /** The broken path of the current incident, or null. */
  broken: Set<string> | null;
  selected: string | null;
}

export interface HighlightStore {
  get: () => Highlight;
  set: (change: Partial<Highlight>) => void;
  subscribe: (listener: () => void) => () => void;
}

export const NO_HIGHLIGHT: Highlight = { lit: null, hovered: null, broken: null, selected: null };

export function highlightStore(initial: Highlight = NO_HIGHLIGHT): HighlightStore {
  let value = initial;
  const listeners = new Set<() => void>();
  return {
    get: () => value,
    set(change) {
      const next = { ...value, ...change };
      if ((Object.keys(next) as (keyof Highlight)[]).every((k) => next[k] === value[k])) return;
      value = next;
      for (const l of listeners) l();
    },
    subscribe(l) {
      listeners.add(l);
      return () => listeners.delete(l);
    },
  };
}

/** Without a provider (a test rendering the map alone), nothing is lit. */
export const HighlightContext = createContext<HighlightStore>(highlightStore());

/** The part of the highlight a component shows; it renders again only when
 * that changes. `pick` must return a primitive or an object kept in the store. */
export function useHighlight<T>(pick: (h: Highlight) => T): T {
  const store = useContext(HighlightContext);
  const get = () => pick(store.get());
  return useSyncExternalStore(store.subscribe, get, get);
}

/** The classes a card gets from the highlight. */
export function highlightClasses(id: string, h: Highlight, base: string): string {
  const c = [base];
  if (h.lit && !h.lit.has(id)) c.push("dim");
  if (h.lit?.has(id) && h.hovered !== id) c.push("lit");
  if (h.broken?.has(id)) c.push("incident");
  if (h.selected === id) c.push("selected");
  return c.join(" ");
}
