import type { DiffLine, Entry } from "../gen/feed";

/** Whether a change has anything to show under "See diff": a diff, or an
 * edit whose contents kubexray doesn't read (it says so). */
export function hasDiff(entries: Entry[]): boolean {
  return entries.some((e) => (e.diff?.length ?? 0) > 0 || unread(e));
}

const unread = (e: Entry) => (e.kind === "ConfigMap" || e.kind === "Secret") && e.what === "edited";

/** A change, before and after, git style (ADR-0015): "-" what it was, "+"
 * what it became, "~" where kubexray knows something changed but doesn't
 * keep its value. */
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

function Lines({ l }: { l: DiffLine }) {
  const note = l.note ? <span className="diff-note"> · {l.note}</span> : null;
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

function Line({ op, path, value, note }: { op: "+" | "-" | "~"; path: string; value: string; note?: React.ReactNode }) {
  const kind = op === "+" ? "add" : op === "-" ? "del" : "note";
  return (
    <div className={`diff-line ${kind}`}>
      <span className="diff-op" aria-label={op === "+" ? "after" : op === "-" ? "before" : "changed"}>
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
