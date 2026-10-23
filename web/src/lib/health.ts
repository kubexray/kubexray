// How a pod or app looks: healthy, starting, failing or shutting down. Mirrors
// the cause engine's rules (internal/cause): a ready pod is fine, finished
// pods don't count.
import type { Node } from "../gen/graph";

export type Health = "ok" | "warn" | "crit" | "dying";

const failing = /CrashLoop|ImagePull|ErrImage|InvalidImageName|Error|OOMKilled|Evicted|Init:|BackOff|Unschedulable|CreateContainer/;

export function podHealth(p: Node): Health {
  const s = p.status;
  if (s.reason === "Terminating") return "dying";
  if (s.ready) return "ok";
  if (failing.test(s.reason ?? "") || s.phase === "Failed") return "crit";
  return "warn";
}

/** Whether a pod is part of its app's current state (not finished). */
export function activePod(appKind: string, p: Node): boolean {
  if (p.status.reason === "Terminating" || p.status.phase === "Succeeded") return false;
  if (p.status.phase === "Failed") return appKind === "Job" || appKind === "Pod";
  return true;
}

export function podText(p: Node): string {
  const s = p.status;
  const parts = [s.reason || (s.ready ? "Running" : s.phase || "Not ready")];
  if (s.restarts) parts.push(`${s.restarts} restart${s.restarts === 1 ? "" : "s"}`);
  return parts.join(" · ");
}

export function nodeSummary(n: Node): string {
  const s = n.status;
  if (s.replicas) return `${s.replicas.ready}/${s.replicas.desired} ready`;
  if (n.kind === "Pod") return podText(n);
  return [s.phase, s.reason].filter(Boolean).join(" · ") || (s.ready ? "Ready" : "Not ready");
}
