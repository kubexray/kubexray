import { describe, expect, it } from "vitest";
import { fieldChanges, segments, toYaml } from "./manifest";

const v = (text: string, hash = text) => ({ text, hash });

describe("segments", () => {
  it("splits keys, quoted keys and list items", () => {
    expect(segments('metadata.labels["app.kubernetes.io/name"]')).toEqual(["metadata", "labels", "app.kubernetes.io/name"]);
    expect(segments("spec.containers[api].env[0].value")).toEqual(["spec", "containers", "[api]", "env", "[0]", "value"]);
  });
});

describe("toYaml", () => {
  it("rebuilds YAML from paths", () => {
    expect(
      toYaml({
        "spec.maxReplicas": v("10"),
        "spec.metrics[0].type": v("Resource"),
        "spec.metrics[0].resource.name": v("cpu"),
        "spec.containers[api].env[DB_PASSWORD].value": { hash: "fp:1", masked: "name" },
        "spec.containers[api].name": v("api"),
      }),
    ).toBe(
      [
        "spec:",
        "  containers:",
        "    - env:",
        "        - value: ‹hidden›",
        "      name: api",
        "  maxReplicas: 10",
        "  metrics:",
        "    - resource:",
        "        name: cpu",
        "      type: Resource",
      ].join("\n"),
    );
  });

  it("orders list items by number", () => {
    expect(toYaml({ "a[10]": v("ten"), "a[2]": v("two") })).toBe(["a:", "  - two", "  - ten"].join("\n"));
  });
});

describe("fieldChanges", () => {
  it("says what changed, with short values", () => {
    expect(fieldChanges({ "spec.maxReplicas": v("10"), old: v("x") }, { "spec.maxReplicas": v("3"), new: v("y") })).toEqual([
      "new added",
      "spec.maxReplicas 10 → 3",
      "old removed",
    ]);
    expect(fieldChanges({ k: { hash: "fp:1" } }, { k: { hash: "fp:2" } })).toEqual(["k changed"]);
  });
});
