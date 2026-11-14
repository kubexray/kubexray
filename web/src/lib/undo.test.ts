import { describe, expect, it } from "vitest";
import type { Entry } from "../gen/feed";
import { undoCommand } from "./undo";

const drift = (over: Partial<Entry>): Entry => ({
  at: "2026-10-28T11:22:44Z", object: "Deployment/shop/storefront", kind: "Deployment", name: "storefront", namespace: "shop",
  what: "template", summary: "env PROMO_BANNER added", drift: "edited by hand", ...over,
});

describe("undoing a hand edit", () => {
  it("removes an env var added by hand", () => {
    expect(undoCommand(drift({ driftDiff: [{ path: "env PROMO_BANNER", op: "added", to: "on", value: true }] }))).toBe("kubectl set env deploy/storefront -n shop PROMO_BANNER-");
  });

  it("puts back a value it recorded, quoted for the shell", () => {
    expect(undoCommand(drift({ kind: "StatefulSet", driftDiff: [{ path: "env GREETING", op: "changed", from: "hello world", to: "hi", value: true }] }))).toBe("kubectl set env sts/storefront -n shop 'GREETING=hello world'");
    expect(undoCommand(drift({ driftDiff: [{ path: "replicas", op: "changed", from: "2", to: "5" }] }))).toBe("kubectl scale deploy/storefront -n shop --replicas=2");
  });

  it("leaves it to the full restore when it can't undo all of it", () => {
    expect(undoCommand(drift({ driftDiff: [{ path: "env SECRET", op: "removed", from: "a value" }] }))).toBeNull(); // value not recorded
    expect(undoCommand(drift({ driftDiff: [{ path: "env A", op: "added", to: "1", value: true }, { path: "image", op: "changed", from: "a", to: "b" }] }))).toBeNull();
    expect(undoCommand(drift({ kind: "ConfigMap", driftDiff: [{ path: "env A", op: "added", to: "1", value: true }] }))).toBeNull();
    expect(undoCommand(drift({ driftDiff: [] }))).toBeNull();
  });
});
