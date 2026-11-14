// The short way back from a hand edit (ADR-0019): a kubectl command that
// undoes just what was edited, when kubexray knows enough to write one.
// The restore command (the whole object as its tool applied it) stays the
// fallback.
import type { Entry } from "../gen/feed";

const SHORT: Record<string, string> = { Deployment: "deploy", StatefulSet: "sts", DaemonSet: "ds" };

/** Quotes a shell word only when it needs it. */
const sh = (s: string) => (/^[\w./:=@%+-]+$/.test(s) ? s : `'${s.replaceAll("'", `'\\''`)}'`);

/** The command that undoes an entry's drift, or null: env vars added,
 * changed or removed by hand, and replicas, when every value it needs was
 * recorded. */
export function undoCommand(e: Entry): string | null {
  const kind = SHORT[e.kind];
  const lines = e.driftDiff ?? [];
  if (!kind || !lines.length) return null;
  const target = `${kind}/${e.name}${e.namespace ? ` -n ${e.namespace}` : ""}`;
  const env: string[] = [];
  let replicas: string | null = null;
  for (const l of lines) {
    const name = l.path.match(/^env (\w+)$/)?.[1];
    // A value kubexray describes rather than records ("a value") can't be put back.
    const known = (v?: string) => v !== undefined && !!l.value;
    if (name && l.op === "added") env.push(`${name}-`);
    else if (name && known(l.from)) env.push(`${name}=${l.from}`);
    else if (l.path === "replicas" && l.from && /^\d+$/.test(l.from)) replicas = l.from;
    else return null; // something else changed too: only the full restore puts it all back
  }
  const out: string[] = [];
  if (env.length) out.push(`kubectl set env ${target} ${env.map(sh).join(" ")}`);
  if (replicas) out.push(`kubectl scale ${target} --replicas=${replicas}`);
  return out.join(" && ") || null;
}
