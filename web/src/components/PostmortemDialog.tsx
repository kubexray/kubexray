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

  const href = useMemo(() => URL.createObjectURL(new Blob([markdown], { type: "text/markdown" })), [markdown]);
  useEffect(() => () => URL.revokeObjectURL(href), [href]);
  const name = `postmortem-${input.at.slice(0, 16).replace(/[^0-9]/g, "")}.md`;

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
        <a href={href} download={name}>
          Download .md
        </a>
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
      {copied === false && <p className="note">Couldn't copy: select the text above and copy it, or download it.</p>}
    </dialog>
  );
}
