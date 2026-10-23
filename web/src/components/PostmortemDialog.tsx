import { useEffect, useMemo, useRef, useState } from "react";
import { type DraftInput, postmortem } from "../lib/postmortem";

interface Props {
  /** Called once, when the dialog opens: the draft is a snapshot of that moment. */
  draft: () => DraftInput;
  onClose: () => void;
}

/** A post-mortem draft to copy or download as Markdown. */
export function PostmortemDialog({ draft, onClose }: Props) {
  const [input] = useState(draft);
  const markdown = useMemo(() => postmortem(input), [input]);
  const [copied, setCopied] = useState<boolean | null>(null);
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = dialog.current;
    d?.showModal();
    return () => d?.close();
  }, []);

  // The file is made when it's asked for, so there's no object URL to manage.
  const download = () => {
    const stamp = Date.parse(input.at);
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([markdown], { type: "text/markdown" }));
    a.download = `postmortem-${Number.isFinite(stamp) ? new Date(stamp).toISOString().slice(0, 16).replace(/[^0-9]/g, "") : Date.now()}.md`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  };

  const copy = () =>
    navigator.clipboard.writeText(markdown).then(
      () => setCopied(true),
      () => setCopied(false),
    );

  return (
    <dialog ref={dialog} className="share postmortem" onClose={onClose} aria-labelledby="pm-title">
      <h2 id="pm-title">Post-mortem draft</h2>
      <p>A starting point in Markdown, from what kubexray recorded. Fill in the TODOs; it can't see users, traffic or code.</p>
      <textarea readOnly value={markdown} aria-label="Post-mortem draft in Markdown" spellCheck={false} />
      <div className="share-actions">
        <button type="button" className="primary" onClick={() => void copy()}>
          {copied ? "Copied" : "Copy Markdown"}
        </button>
        <button type="button" onClick={download}>
          Download .md
        </button>
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
      {copied === false && <p className="note">Couldn't copy: select the text above and copy it, or download it.</p>}
    </dialog>
  );
}
