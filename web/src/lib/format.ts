// Times as people read them.

export function clock(ts: string): string {
  return new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function ago(ts: string, now: number): string {
  const s = Math.round((now - Date.parse(ts)) / 1000);
  if (s < 1) return "just now";
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 48 * 3600) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function duration(fromTs: string, now: number): string {
  const s = Math.max(0, Math.round((now - Date.parse(fromTs)) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

/** Who made a change, e.g. "kubectl patch" or "helm". */
export function who(c: { by: string; tool: string; via?: string } | undefined): string {
  if (!c) return "";
  if (c.tool === "kubectl" && c.via) return `kubectl ${c.via}`;
  return c.tool === "other" || c.tool === "controller" ? c.by : c.tool;
}
