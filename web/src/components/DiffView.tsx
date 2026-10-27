import { useState } from "react";
import type { DiffLine, Entry } from "../gen/feed";

/** Whether a change has anything to show under "See diff": a diff, or an
 * edit whose contents kubexray doesn't read (it says so). */
export function hasDiff(entries: Entry[]): boolean {
  return entries.some((e) => (e.diff?.length ?? 0) > 0 || unread(e));
}

const unread = (e: Entry) => (e.kind === "ConfigMap" || e.kind === "Secret") && e.what === "edited" && !e.diff?.length;

/** A change, before and after, git style (ADR-0015): "-" what it was, "+"
 * what it became, "~" where kubexray knows something changed but doesn't
 * show its value (not recorded, or hidden because it looks like a
 * credential). */
export function DiffView({ entries }: { entries: Entry[] }) {
  const shown = entries.filter((e) => (e.diff?.length ?? 0) > 0 || unread(e));
  const several = new Set(shown.map((e) => e.object)).size > 1;
  return (
    <div className="diff-view" role="group" aria-label="What changed, before and after">
      {shown.map((e, i) => (
        <div key={`${e.object}-${i}`} className="diff-object">
          {several && (
            <div className="diff-head">
              {e.kind} {e.name}
            </div>
          )}
          {unread(e) && (
            <div className="diff-line note">
              <span className="diff-op">~</span>
              <span className="diff-path">data</span>
              <span className="diff-val">edited · kubexray doesn't read {e.kind} contents, so it can't show the values</span>
            </div>
          )}
          {e.diff?.map((l, j) => <Lines key={`${l.path}-${j}`} l={l} />)}
        </div>
      ))}
    </div>
  );
}

const TOOL: Record<string, string> = { helm: "Helm", argocd: "Argo CD", flux: "Flux" };

/** A hand edit to a managed object (ADR-0019): what it says, how the object
 * differs from what its tool last applied, and the command that puts it
 * back. */
export function DriftView({ entry: e, label }: { entry: Entry; label?: boolean }) {
  const [copied, setCopied] = useState(false);
  const tool = TOOL[e.origin?.tool ?? ""] ?? e.origin?.tool ?? "its tool";
  const copy = () => {
    if (!e.restore) return;
    navigator.clipboard.writeText(e.restore).then(
      () => setCopied(true),
      () => setCopied(false),
    );
  };
  return (
    <div className="feed-drift">
      <div>
        {label && <strong>Drift: </strong>}
        {e.drift}
      </div>
      {(e.driftDiff?.length ?? 0) > 0 && (
        <div className="diff-view" role="group" aria-label={`How it differs from what ${tool} applied`}>
          <div className="diff-head">against what {tool} applied</div>
          {e.driftDiff?.map((l, i) => <Lines key={`${l.path}-${i}`} l={l} />)}
        </div>
      )}
      {e.restore && (
        <div className="drift-restore">
          <span>{e.origin?.tool === "helm" ? "Put it back as Helm applied it:" : `Re-apply from Git (${tool} may keep fields added by hand):`}</span>
          <code>{e.restore}</code>
          <button type="button" onClick={copy}>
            {copied ? "Copied" : "Copy"}
          </button>
        </div>
      )}
    </div>
  );
}

function Lines({ l }: { l: DiffLine }) {
  const note = l.note ? <span className="diff-note"> · {l.note}</span> : null;
  // A file in a ConfigMap: only the lines that changed, with a little around them.
  if (l.op === "changed" && l.from && l.to && (l.from.includes("\n") || l.to.includes("\n"))) {
    const hunk = lineDiff(l.from, l.to);
    if (hunk) {
      return (
        <>
          <Line op="~" path={l.path} value="changed" note={note} />
          {hunk.map((h, i) => (
            <Line key={i} op={h.op} path="" value={h.text} />
          ))}
        </>
      );
    }
  }
  if (l.op === "changed" && (l.from || l.to)) {
    return (
      <>
        <Line op="-" path={l.path} value={l.from ?? ""} />
        <Line op="+" path={l.path} value={l.to ?? ""} note={note} />
      </>
    );
  }
  if (l.op === "changed") return <Line op="~" path={l.path} value="changed" note={note} />;
  if (l.op === "removed") return <Line op="-" path={l.path} value={l.from ?? "removed"} note={note} />;
  return <Line op="+" path={l.path} value={l.to ?? "added"} note={note} />;
}

type Op = "+" | "-" | "~" | " ";

/** How many unchanged lines to keep around a change. */
const CONTEXT = 2;
/** Larger values are shown whole, not compared line by line. */
const MAX_LINES = 400;

/** The changed lines of two texts, with CONTEXT lines around each change
 * and "…" where unchanged lines are left out. Null when too long to
 * compare. */
export function lineDiff(from: string, to: string): { op: Op; text: string }[] | null {
  const a = from.split("\n");
  const b = to.split("\n");
  if (a.length > MAX_LINES || b.length > MAX_LINES) return null;
  // Longest common subsequence, from the end: at(i, j) for a[i:] and b[j:].
  const w = b.length + 1;
  const lcs = new Uint16Array((a.length + 1) * w);
  const at = (i: number, j: number) => lcs[i * w + j] ?? 0;
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i * w + j] = a[i] === b[j] ? at(i + 1, j + 1) + 1 : Math.max(at(i + 1, j), at(i, j + 1));
    }
  }
  const all: { op: Op; text: string }[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length || j < b.length) {
    if (i < a.length && j < b.length && a[i] === b[j]) {
      all.push({ op: " ", text: a[i++] ?? "" });
      j++;
    } else if (i < a.length && (j === b.length || at(i + 1, j) >= at(i, j + 1))) {
      all.push({ op: "-", text: a[i++] ?? "" }); // removals first, as git does
    } else {
      all.push({ op: "+", text: b[j++] ?? "" });
    }
  }
  const near = (k: number) => all.slice(Math.max(0, k - CONTEXT), k + CONTEXT + 1).some((l) => l.op !== " ");
  const out: { op: Op; text: string }[] = [];
  all.forEach((l, k) => {
    if (l.op !== " " || near(k)) out.push(l);
    else if (out.at(-1)?.text !== "…" || out.at(-1)?.op !== " ") out.push({ op: " ", text: "…" });
  });
  return out;
}

function Line({ op, path, value, note }: { op: Op; path: string; value: string; note?: React.ReactNode }) {
  const kind = op === "+" ? "add" : op === "-" ? "del" : op === "~" ? "note" : "same";
  return (
    <div className={`diff-line ${kind}`}>
      <span className="diff-op" aria-label={op === "+" ? "after" : op === "-" ? "before" : op === "~" ? "changed" : "unchanged"}>
        {op}
      </span>
      <span className="diff-path">{path}</span>
      <span className="diff-val">
        {value}
        {note}
      </span>
    </div>
  );
}
