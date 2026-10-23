import { describe, expect, it } from "vitest";
import type { Incident } from "../gen/cause";
import { postmortem, text } from "./postmortem";

const T = (s: number) => new Date(Date.UTC(2026, 9, 23, 14, 0, s)).toISOString();

const checkout: Incident = {
  app: "Deployment/shop/checkout",
  kind: "Deployment",
  namespace: "shop",
  name: "checkout",
  healthy: false,
  since: T(20),
  symptom: { summary: "1 pod CrashLoopBackOff", ready: 2, desired: 3 },
  impact: ["Ingress web → Service checkout → 2 of 3 pods ready"],
  causes: [
    {
      kind: "config",
      object: "ConfigMap/shop/checkout-config",
      at: T(5),
      by: { at: T(5), by: "kubectl-patch", tool: "kubectl", via: "patch" },
      summary: "ConfigMap checkout-config was edited",
      confidence: "likely",
      evidence: ["1 pod started after the edit is failing"],
    },
  ],
  next: [{ command: "kubectl -n shop rollout undo deployment/checkout", note: "roll back" }],
};

const events = [
  { id: "1", ts: T(-600), text: "long before", kind: "info" as const },
  { id: "2", ts: T(5), text: "ConfigMap checkout-config edited", kind: "info" as const },
  { id: "3", ts: T(25), text: "checkout-1: CrashLoopBackOff", kind: "crit" as const },
  { id: "4", ts: "not a time", text: "garbage", kind: "info" as const },
];

const draft = (over: Partial<Parameters<typeof postmortem>[0]> = {}) =>
  postmortem({ entry: { group: null, incidents: [checkout] }, cluster: "demo", at: T(50), recoveredAt: null, events, eventsFrom: null, link: null, ...over });

describe("postmortem", () => {
  it("drafts an ongoing incident with its cause, impact and timeline", () => {
    const md = draft();
    expect(md).toContain("# Post-mortem (draft): checkout is degraded");
    expect(md).toContain("**Status:** ongoing as of 2026-10-23 14:00:50 UTC");
    expect(md).toContain("**Duration:** 30s so far");
    expect(md).toContain("ConfigMap checkout-config was edited by kubectl patch at 2026-10-23 14:00:05 UTC");
    expect(md).toContain("- shop/checkout is degraded: 1 pod CrashLoopBackOff (2 of 3 ready)");
    expect(md).toContain("- 14:00:25 checkout-1: CrashLoopBackOff");
    expect(md).toContain("- 14:00:20 **Incident began**");
    expect(md).toContain("```sh\n# roll back\nkubectl -n shop rollout undo deployment/checkout\n```");
    // Outside the window, or unparseable: left out.
    expect(md).not.toContain("long before");
    expect(md).not.toContain("garbage");
    expect(md).not.toContain("## Replay");
  });

  it("reports recovery, the replay link and what the timeline missed", () => {
    const md = draft({ recoveredAt: T(80), eventsFrom: T(10), link: "https://kubexray.github.io/kubexray/?replay=https://x/a.kxr&t=1" });
    expect(md).toContain("**Status:** resolved at 2026-10-23 14:01:20 UTC");
    expect(md).toContain("**Duration:** 1m 0s");
    expect(md).toContain("- 14:01:20 **Recovered**");
    expect(md).toContain("Pod status changes before 14:00:10 aren't listed");
    expect(md).toContain("Watch it happen: <https://kubexray.github.io/kubexray/?replay=https://x/a.kxr&t=1>");
  });

  it("drafts a group, and an incident with no cause", () => {
    const group = { cause: { kind: "node" as const, object: "Node/w2", summary: "node w2 is not ready", confidence: "very likely" }, apps: [checkout.app] };
    expect(draft({ entry: { group, incidents: [checkout] } })).toContain("# Post-mortem (draft): node w2 is not ready: 1 apps affected");
    const md = draft({ entry: { group: null, incidents: [{ ...checkout, causes: [], noCause: "nothing changed in the last 30 minutes" }] } });
    expect(md).toContain("found no change in the cluster that explains it (nothing changed in the last 30 minutes)");
    expect(md).toContain("TODO: what caused it.");
  });

  it("keeps cluster strings inert, and never throws on bad times", () => {
    expect(text("evil\n# heading <img src=x> [link](y) `code` | & *")).toBe("evil \\# heading \\<img src=x\\> \\[link\\](y) \\`code\\` \\| \\& \\*");
    const md = draft({
      at: "nope",
      entry: { group: null, incidents: [{ ...checkout, since: "bad", causes: [{ ...checkout.causes![0]!, at: "bad", from: "a`b", to: "c\nd" }], next: [{ command: "echo ```" }] }] },
    });
    expect(md).toContain("**Status:** ongoing as of unknown time");
    expect(md).toContain("Changed from `a'b` to `c d`.");
    expect(md).toContain("````sh\necho ```\n````");
  });
});
