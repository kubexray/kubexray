import { type PointerEvent, useRef, useState } from "react";

interface Props {
  /** "col" sits between columns and resizes a width; "row" a height. */
  axis: "col" | "row";
  label: string;
  value: number;
  min: number;
  max: number;
  /** The pane resized is before the handle (grows as it moves right or
   * down) or after it (grows as it moves left or up). */
  pane: "before" | "after";
  onChange: (v: number) => void;
  /** Double-click: back to the default size. */
  onReset: () => void;
}

const clamp = (v: number, min: number, max: number) => Math.round(Math.min(max, Math.max(min, v)));

/** A drag handle between two panes, also moved with the arrow keys
 * (WAI-ARIA window splitter). */
export function Splitter({ axis, label, value, min, max, pane, onChange, onReset }: Props) {
  const start = useRef<{ at: number; value: number } | null>(null);
  const [dragging, setDragging] = useState(false);
  const pos = (e: PointerEvent) => (axis === "col" ? e.clientX : e.clientY);
  const sign = pane === "before" ? 1 : -1;
  return (
    <div
      role="separator"
      tabIndex={0}
      aria-label={label}
      aria-orientation={axis === "col" ? "vertical" : "horizontal"}
      aria-valuenow={value}
      aria-valuemin={min}
      aria-valuemax={max}
      title="Drag to resize · double-click to reset"
      className={`splitter ${axis}${dragging ? " dragging" : ""}`}
      onPointerDown={(e) => {
        start.current = { at: pos(e), value };
        setDragging(true);
        e.currentTarget.setPointerCapture(e.pointerId);
      }}
      onPointerMove={(e) => {
        if (start.current) onChange(clamp(start.current.value + sign * (pos(e) - start.current.at), min, max));
      }}
      onPointerUp={() => {
        start.current = null;
        setDragging(false);
      }}
      onLostPointerCapture={() => {
        start.current = null;
        setDragging(false);
      }}
      onDoubleClick={onReset}
      onKeyDown={(e) => {
        const step = e.shiftKey ? 64 : 16;
        const grow = axis === "col" ? (pane === "before" ? "ArrowRight" : "ArrowLeft") : pane === "before" ? "ArrowDown" : "ArrowUp";
        const shrink = axis === "col" ? (pane === "before" ? "ArrowLeft" : "ArrowRight") : pane === "before" ? "ArrowUp" : "ArrowDown";
        const next = e.key === grow ? value + step : e.key === shrink ? value - step : e.key === "Home" ? min : e.key === "End" ? max : null;
        if (next === null) return;
        e.preventDefault();
        onChange(clamp(next, min, max));
      }}
    >
      <i aria-hidden="true" />
    </div>
  );
}

/** A size kept in this browser, so the layout survives a reload. */
export function useStoredSize(key: string, fallback: number): [number, (v: number) => void] {
  const [v, setV] = useState(() => {
    try {
      const n = Number(localStorage.getItem(key));
      return Number.isFinite(n) && n > 0 ? n : fallback;
    } catch {
      return fallback; // storage blocked
    }
  });
  const set = (n: number) => {
    setV(n);
    try {
      localStorage.setItem(key, String(n));
    } catch {
      // storage blocked: the size lasts until reload
    }
  };
  return [v, set];
}
