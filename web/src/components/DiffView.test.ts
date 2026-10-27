import { describe, expect, it } from "vitest";
import { lineDiff } from "./DiffView";

describe("lineDiff", () => {
  it("keeps the changed lines and a little around them", () => {
    const from = ["a", "b", "c", "d", "e", "f", "g", "h"].join("\n");
    const to = ["a", "b", "c", "d", "E", "f", "g", "h"].join("\n");
    expect(lineDiff(from, to)).toEqual([
      { op: " ", text: "…" },
      { op: " ", text: "c" },
      { op: " ", text: "d" },
      { op: "-", text: "e" },
      { op: "+", text: "E" },
      { op: " ", text: "f" },
      { op: " ", text: "g" },
      { op: " ", text: "…" },
    ]);
  });

  it("shows added and removed lines", () => {
    expect(lineDiff("x\ny", "x\ny\nz")).toEqual([
      { op: " ", text: "x" },
      { op: " ", text: "y" },
      { op: "+", text: "z" },
    ]);
  });

  it("gives up on very long values", () => {
    expect(lineDiff("a\n".repeat(500), "b")).toBeNull();
  });
});
