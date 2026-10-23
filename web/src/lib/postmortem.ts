// Post-mortem drafts: the incident as Markdown, to paste into a doc or an
// issue and finish by hand. Everything here comes from the cluster or a replay
// file, so it's treated as untrusted text: escaped, one line, and timestamps
// that don't parse never throw.
import type { Cause, Incident } from "../gen/cause";
import { stateText, type Entry } from "../components/IncidentCard";
import type { ClusterEvent } from "./events";
import { who } from "./format";

export interface DraftInput {
  entry: Entry;
  cluster: string;
  /** The moment the draft describes: now when live, the playhead in a replay. */
  at: string;
  /** When the apps were healthy again, if the recording shows it. */
  recoveredAt: string | null;
  events: ClusterEvent[];
  /** Status events before this time weren't seen (live mode: the tab opened). */
  eventsFrom: string | null;
  /** A link to watch the incident, when there is one to share. */
  link: string | null;
}

/** How many timeline lines to list before "N more". */
const MAX_EVENTS = 40;
/** How far before the change or the first symptom the timeline starts. */
const LEAD = 2 * 60_000;

/** Cluster strings as inert inline Markdown, on one line. */
export function text(s: string): string {
  return s.replace(/\s+/g, " ").trim().replace(/[\\`*_[\]<>|~&#]/g, (c) => `\\${c}`);
}

const ms = (ts: string | undefined | null): number => (ts ? Date.parse(ts) : NaN);
const valid = (t: number) => !Number.isNaN(t);

/** 2026-10-23 14:05:09 UTC; drafts are shared across time zones. */
function utc(t: number): string {
  return valid(t) ? `${new Date(t).toISOString().slice(0, 19).replace("T", " ")} UTC` : "unknown time";
}
const timeOf = (t: number) => (valid(t) ? new Date(t).toISOString().slice(11, 19) : "--:--:--");

function span(from: number, to: number): string {
  if (!valid(from) || !valid(to)) return "unknown";
  const s = Math.max(0, Math.round((to - from) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

/** Inline code; backticks inside become quotes so the span can't break. */
const inline = (s: string) => `\`${s.replace(/\s+/g, " ").replace(/`/g, "'")}\``;

/** A fenced code block that the content can't close early. */
function code(lines: string[]): string {
  const longest = Math.max(0, ...lines.map((l) => Math.max(0, ...(l.match(/`+/g) ?? []).map((m) => m.length))));
  const fence = "`".repeat(Math.max(3, longest + 1));
  return [fence + "sh", ...lines.map((l) => l.replace(/[\r\n]+/g, " ")), fence].join("\n");
}

function causeText(c: Cause): string {
  const author = who(c.by);
  return `${text(c.summary)}${author ? ` by ${text(author)}` : ""}${c.at && c.kind !== "missing" ? ` at ${utc(ms(c.at))}` : ""}`;
}

export function title(entry: Entry): string {
  const first = entry.incidents[0];
  if (entry.group) return `${text(entry.group.cause.summary)}: ${entry.incidents.length} apps affected`;
  return first ? `${text(first.name)} ${stateText(first)}` : "Incident";
}

/** The draft, as Markdown. */
export function postmortem(d: DraftInput): string {
  const { entry } = d;
  const first: Incident | undefined = entry.incidents[0];
  const cause = entry.group?.cause ?? first?.causes?.[0];
  const starts = entry.incidents.map((i) => ms(i.since)).filter(valid);
  const start = starts.length ? Math.min(...starts) : NaN;
  const at = ms(d.at);
  const recovered = ms(d.recoveredAt);
  const end = valid(recovered) ? recovered : at;
  const out: string[] = [];
  const add = (...lines: string[]) => out.push(...lines);

  add(`# Post-mortem (draft): ${title(entry)}`, "");
  add(
    "> Drafted by kubexray from what it recorded in the cluster. It sees Kubernetes objects, not users, traffic or code: check every line, and fill in the TODOs, before sharing.",
    "",
  );
  add(`- **Status:** ${valid(recovered) ? `resolved at ${utc(recovered)}` : `ongoing as of ${utc(at)}`}`);
  add(`- **Started:** ${utc(start)}`);
  add(`- **Duration:** ${span(start, end)}${valid(recovered) ? "" : " so far"}`);
  if (d.cluster) add(`- **Cluster:** ${text(d.cluster)}`);
  add("");

  add("## Summary", "");
  if (cause) add(`${title(entry)}. The ${cause.confidence} cause: ${causeText(cause)}.`, "", "TODO: one or two sentences on what users saw.", "");
  else add(`${title(entry)}. kubexray found no change in the cluster that explains it${first?.noCause ? ` (${text(first.noCause)})` : ""}. The cause may be outside the cluster: a dependency, the network, or traffic.`, "");

  add("## Impact", "");
  for (const inc of entry.incidents) {
    add(`- ${text(inc.namespace)}/${text(inc.name)} ${stateText(inc)}: ${text(inc.symptom.summary)} (${inc.symptom.ready} of ${inc.symptom.desired} ready)`);
    for (const line of inc.impact ?? []) add(`  - ${text(line)}`);
  }
  add("- TODO: who or what was affected outside the cluster.", "");

  add("## Cause", "");
  if (cause) {
    add(`${cause.confidence[0]?.toUpperCase()}${cause.confidence.slice(1)}: ${causeText(cause)}.`);
    if (cause.from && cause.to) add("", `Changed from ${inline(cause.from)} to ${inline(cause.to)}.`);
    if (cause.evidence?.length) add("", "Evidence:", ...cause.evidence.map((e) => `- ${text(e)}`));
    const other = entry.group ? [] : (first?.causes?.slice(1) ?? []);
    if (other.length) add("", "Also changed:", ...other.map((c) => `- ${causeText(c)} (${c.confidence})`));
  } else add("TODO: what caused it.");
  add("");

  add("## Timeline (UTC)", "");
  const from = Math.min(...[ms(cause?.at), start].filter(valid)) - LEAD;
  const seen = d.events
    .map((e) => ({ t: ms(e.ts), text: e.text }))
    .filter((e) => valid(e.t) && (!valid(from) || e.t >= from) && (!valid(end) || e.t <= end))
    .sort((a, b) => a.t - b.t);
  const lines = [
    ...seen.map((e) => ({ t: e.t, line: text(e.text) })),
    ...(valid(start) ? [{ t: start, line: "**Incident began**" }] : []),
    ...(valid(recovered) ? [{ t: recovered, line: "**Recovered**" }] : []),
  ].sort((a, b) => a.t - b.t);
  if (valid(from) && valid(ms(d.eventsFrom)) && ms(d.eventsFrom) > from) {
    add(`_Pod status changes before ${timeOf(ms(d.eventsFrom))} aren't listed: kubexray's page wasn't open yet. Changes to objects are complete._`, "");
  }
  for (const l of lines.slice(0, MAX_EVENTS)) add(`- ${timeOf(l.t)} ${l.line}`);
  if (lines.length > MAX_EVENTS) add(`- … ${lines.length - MAX_EVENTS} more`);
  if (!lines.length) add("- TODO: what happened, when.");
  add("");

  add("## Resolution", "");
  add(valid(recovered) ? `The apps were healthy again at ${utc(recovered)}. TODO: what fixed it.` : "TODO: what fixed it, and when.");
  const steps = first?.next ?? [];
  if (steps.length) add("", "kubexray suggested:", "", code(steps.flatMap((s) => (s.note ? [`# ${s.note}`, s.command] : [s.command]))));
  add("");

  add("## Follow-ups", "", "- [ ] TODO: how it was detected, and how it could be detected sooner", "- [ ] TODO: what would have caught it before it shipped", "");

  if (d.link) add("## Replay", "", `Watch it happen: <${d.link.replace(/[<>\s]/g, encodeURIComponent)}>`, "");
  return out.join("\n");
}
