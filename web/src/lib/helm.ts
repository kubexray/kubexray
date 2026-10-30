// What the map shows about the tools that manage objects (Helm, Argo CD,
// Flux): who manages each app, Helm's release history from its revision
// records, and hand edits that a tool's next sync would undo.
import { createContext } from "react";
import type { Entry, Set as ChangeSet } from "../gen/feed";
import type { Node } from "../gen/graph";
import type { ClusterState } from "./state";

export interface Owner {
  tool: "helm" | "argocd" | "flux";
  name: string;
  /** Where the tool keeps it: a Helm release's namespace. */
  ns?: string;
  /** "1.4.0", from the helm.sh/chart label. */
  chart?: string;
}

/** "storefront-1.2.1" → "1.2.1". */
export function chartVersion(n: Node): string | undefined {
  const chart = n.labels?.["helm.sh/chart"];
  return chart?.match(/-(v?\d[\w.+-]*)$/)?.[1];
}

export function ownerOf(n: Node): Owner | undefined {
  const o = n.origin;
  if (!o || (o.tool !== "helm" && o.tool !== "argocd" && o.tool !== "flux") || !o.name) return undefined;
  return { tool: o.tool, name: o.name, ns: o.tool === "helm" ? (o.namespace ?? n.ns) : o.namespace, chart: o.tool === "helm" ? chartVersion(n) : undefined };
}

/** The badge's words: "storefront 1.2.1", "argo · shop". */
export function ownerText(o: Owner): string {
  if (o.tool === "helm") return o.chart ? `${o.name} ${o.chart}` : o.name;
  return `${o.tool === "argocd" ? "argo" : "flux"} · ${o.name}`;
}

export const TOOL_NAME: Record<Owner["tool"], string> = { helm: "Helm", argocd: "Argo CD", flux: "Flux" };

/** A release (or GitOps app) as something to select, by the namespace it's
 * drawn in: "release:helm/shop/storefront". */
export const releaseId = (o: Owner, ns: string) => `release:${o.tool}/${ns}/${o.name}`;

export function parseReleaseId(id: string | null): { tool: Owner["tool"]; ns: string; name: string } | undefined {
  const m = id?.match(/^release:(helm|argocd|flux)\/([^/]+)\/(.+)$/);
  return m ? { tool: m[1] as Owner["tool"], ns: m[2] as string, name: m[3] as string } : undefined;
}

// What the map draws: a release's pods and ReplicaSets show inside its apps.
const DRAWN = new Set(["Ingress", "Service", "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "ConfigMap", "Secret", "PersistentVolumeClaim"]);

/** The objects a release manages in a namespace. */
export function releaseObjects(state: ClusterState, tool: Owner["tool"], ns: string, name: string): Node[] {
  const out: Node[] = [];
  for (const n of state.nodes.values()) {
    if (n.ns !== ns || !DRAWN.has(n.kind)) continue;
    const o = ownerOf(n);
    if (o?.tool === tool && o.name === name) out.push(n);
  }
  return out;
}

export interface Revision {
  version: number;
  status: string; // deployed, superseded, failed, pending-upgrade…
  at: string;
}

/** A Helm release's revisions, newest first, from the Secrets Helm keeps
 * them in (sh.helm.release.v1.<release>.v<N>): only their labels are read. */
export function revisions(state: ClusterState, release: string, ns: string): Revision[] {
  const prefix = `Secret/${ns}/sh.helm.release.v1.${release}.v`;
  const out: Revision[] = [];
  for (const [id, n] of state.nodes) {
    if (!id.startsWith(prefix) || n.labels?.owner !== "helm") continue;
    const version = Number(n.labels.version ?? id.slice(prefix.length));
    if (Number.isFinite(version)) out.push({ version, status: n.labels.status ?? "", at: n.created });
  }
  return out.sort((a, b) => b.version - a.version);
}

/** A revision of any release in the cluster. */
export interface ReleaseRevision extends Revision {
  release: string;
  ns: string;
}

const RECORD = /^sh\.helm\.release\.v1\.(.+)\.v(\d+)$/;

/** Every Helm revision in the cluster, oldest first, from the labels of the
 * Secrets Helm keeps them in. */
export function allRevisions(state: ClusterState): ReleaseRevision[] {
  const out: ReleaseRevision[] = [];
  for (const n of state.nodes.values()) {
    const m = n.kind === "Secret" && n.labels?.owner === "helm" ? RECORD.exec(n.name) : null;
    if (!m || !n.ns) continue;
    out.push({ release: n.labels?.name ?? (m[1] as string), ns: n.ns, version: Number(n.labels?.version ?? m[2]), status: n.labels?.status ?? "", at: n.created });
  }
  return out.sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.version - b.version);
}

/** Whether the cluster has any Helm release kubexray can see. */
export const hasReleases = (state: ClusterState) => [...state.nodes.values()].some((n) => n.origin?.tool === "helm" || (n.kind === "Secret" && n.labels?.owner === "helm"));

/** The chart version a change moved from and to, from its helm.sh/chart
 * label: "storefront-1.2.0" → "storefront-1.2.1" is 1.2.0 → 1.2.1. */
export function chartChange(s: ChangeSet): { from?: string; to?: string } | undefined {
  for (const e of s.entries) {
    const l = e.diff?.find((d) => d.path.includes("helm.sh/chart"));
    if (!l) continue;
    const v = (x?: string) => x?.match(/-(v?\d[\w.+-]*)$/)?.[1];
    return { from: v(l.from), to: v(l.to) };
  }
  return undefined;
}

/** What the map and the timeline need from the moment shown: a release's
 * revisions, every revision, and a way to rewind to one. */
export interface HelmInfo {
  revisions: (release: string, ns: string) => Revision[];
  all?: () => ReleaseRevision[];
  onRewind?: (ts: string) => void;
}
export const Helm = createContext<HelmInfo>({ revisions: () => [] });

/** The change set a Helm revision made, if the feed has it: the change by
 * Helm to the release's objects closest to the revision, within a minute. */
export function revisionSet(sets: ChangeSet[], o: Owner, r: Revision): ChangeSet | undefined {
  const t = Date.parse(r.at);
  const off = (s: ChangeSet) => Math.abs(Date.parse(s.at) - t);
  return sets.filter((s) => s.origin?.tool === "helm" && s.origin.name === o.name && off(s) <= 60_000).sort((a, b) => off(a) - off(b))[0];
}

/** The hand edit an object carries now: its newest change, if that was
 * one. The tool applying it again clears it. */
export function driftOf(sets: ChangeSet[], id: string): Entry | undefined {
  let latest: Entry | undefined;
  for (const s of sets) {
    for (const e of s.entries) {
      if (e.object === id && (!latest || Date.parse(e.at) >= Date.parse(latest.at))) latest = e;
    }
  }
  return latest?.drift ? latest : undefined;
}
