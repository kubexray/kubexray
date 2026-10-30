import { useContext } from "react";
import type { Cause, Diagnosis, Group, Incident } from "../gen/cause";
import type { DiffLine } from "../gen/feed";
import { causeDiff } from "../lib/feed";
import { FeedSets } from "../lib/feedContext";
import { clock, who } from "../lib/format";

/** Mirrors cause.Incident.Down: no ready pods, of those it wants or at all
 * while traffic is routed to it (scaled to zero). */
export function down(inc: Incident): boolean {
  return !inc.healthy && inc.symptom.ready === 0 && (inc.symptom.desired > 0 || !inc.symptom.badPods?.length);
}

/** Mirrors cause.Incident.State: scaled to zero behind an Ingress, down,
 * degraded, refused new pods, failing pods during an otherwise healthy
 * rollout, or a signal such as restarts (ADR-0016). */
export function stateText(inc: Incident): string {
  const bad = inc.symptom.badPods?.length ?? 0;
  if (inc.healthy) return "is healthy";
  if (inc.signal && bad === 0) return inc.signal.state;
  if (inc.symptom.desired === 0 && inc.symptom.ready === 0 && bad === 0) return "is scaled to zero";
  if (down(inc)) return "is down";
  if (inc.symptom.ready < inc.symptom.desired) return "is degraded";
  if (bad === 0) return "can't create new pods";
  return "has failing pods";
}

const SIGNAL: Record<string, string> = { alert: "Your alert", errors: "Errors", latency: "Latency", restarts: "Restarts", memory: "Memory" };

/** What spotted an incident whose pods look healthy: "Restarts · Kubernetes". */
export function SignalTag({ inc }: { inc: Incident }) {
  if (!inc.signal) return null;
  return (
    <span className="signal-tag">
      {SIGNAL[inc.signal.kind] ?? inc.signal.kind} · {inc.signal.source}
    </span>
  );
}

/** Most severe first: apps that are fully down, then the oldest. */
export function rankIncidents(incidents: Incident[]): Incident[] {
  return [...incidents].sort((a, b) => Number(down(b)) - Number(down(a)) || (a.since ?? "").localeCompare(b.since ?? ""));
}

/** One card entry: a group of incidents sharing a cause, or one incident. */
export interface Entry {
  group: Group | null;
  incidents: Incident[];
}

/** Mirrors cause.Entries: entries with an app that's down first, then larger
 * groups, then the oldest. Groups naming no known incident are dropped. */
export function entries(incidents: Incident[], groups: Group[]): Entry[] {
  const byApp = new Map(incidents.map((i) => [i.app, i]));
  const grouped = new Set<string>();
  const out: Entry[] = [];
  for (const g of groups) {
    const members = g.apps.flatMap((a) => byApp.get(a) ?? []);
    if (!members.length) continue;
    members.forEach((m) => grouped.add(m.app));
    out.push({ group: g, incidents: members });
  }
  for (const inc of rankIncidents(incidents)) if (!grouped.has(inc.app)) out.push({ group: null, incidents: [inc] });
  const since = (e: Entry) => e.incidents.map((i) => i.since ?? "").filter(Boolean).sort()[0] ?? "";
  const anyDown = (e: Entry) => Number(e.incidents.some(down));
  return out.sort((a, b) => anyDown(b) - anyDown(a) || b.incidents.length - a.incidents.length || since(a).localeCompare(since(b)));
}

const LABEL: Record<string, string> = { "very likely": "Very likely cause", likely: "Likely cause", possible: "Possible cause" };

/** How many of a change's diff lines a cause shows; See diff has the rest. */
const SHOWN = 3;

export function CauseLine({ cause }: { cause: Cause }) {
  const author = who(cause.by);
  const sets = useContext(FeedSets);
  const lines = cause.from && cause.to ? [] : causeDiff(sets, cause);
  return (
    <div className="cause">
      <span className="lbl">{LABEL[cause.confidence] ?? "Cause"}</span>
      <span>
        {cause.summary}
        {author && <> by {author}</>}
        {cause.at && cause.kind !== "missing" && <> at {clock(cause.at)}</>}
      </span>
      {cause.from && cause.to && (
        <span className="diff">
          <span className="old">{cause.from}</span>→<span className="new">{cause.to}</span>
        </span>
      )}
      {lines.length > 0 && (
        <ul className="cause-values" aria-label="What changed">
          {lines.slice(0, SHOWN).map((l, i) => (
            <li key={`${l.path}-${i}`}>
              <CauseValue l={l} />
            </li>
          ))}
          {lines.length > SHOWN && <li className="more">and {lines.length - SHOWN} more</li>}
        </ul>
      )}
    </div>
  );
}

/** One changed value: "LOG_LEVEL info → verbose", or what's known when the
 * value isn't shown. */
function CauseValue({ l }: { l: DiffLine }) {
  const short = (s?: string) => !!s && s.length <= 60 && !s.includes("\n");
  if (l.op === "changed" && short(l.from) && short(l.to)) {
    return (
      <>
        <code>{l.path}</code>{" "}
        <span className="diff">
          <span className="old">{l.from}</span>→<span className="new">{l.to}</span>
        </span>
      </>
    );
  }
  if (l.op === "added" && short(l.to)) {
    return (
      <>
        <code>{l.path}</code> added:{" "}
        <span className="diff">
          <span className="new">{l.to}</span>
        </span>
      </>
    );
  }
  return (
    <>
      <code>{l.path}</code> {l.op}
      {l.note && <span className="note"> · {l.note}</span>}
    </>
  );
}

/** What Kubernetes says is wrong (ADR-0009). */
export function DiagnosisLine({ diagnosis }: { diagnosis: Diagnosis }) {
  return (
    <div className="cause">
      <span className="lbl">What's wrong</span>
      <span>{diagnosis.summary}</span>
    </div>
  );
}

