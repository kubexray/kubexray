import { useEffect, useRef, useState } from "react";
import type { Entry } from "./IncidentCard";

/** The player that opens replay files without a cluster (ADR-0008). */
export const PLAYER_URL = "https://kubexray.github.io/kubexray/play/";

interface Props {
  entry: Entry;
  /** Start of the recording, so the window never asks for more. */
  recordedFrom: string | null;
  onClose: () => void;
}

/** Exports the incident as a replay file to share. */
export function ShareDialog({ entry, recordedFrom, onClose }: Props) {
  const [anonymize, setAnonymize] = useState(true);
  const [whole, setWhole] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = dialog.current;
    d?.showModal();
    return () => d?.close();
  }, []);

  // From five minutes before the incident began, so the change is included.
  const since = entry.incidents.map((i) => i.since).filter((s): s is string => !!s).sort()[0];
  let from = since ? new Date(Date.parse(since) - 5 * 60_000) : null;
  if (from && recordedFrom && from < new Date(recordedFrom)) from = new Date(recordedFrom);
  const params = new URLSearchParams();
  if (!whole && from) params.set("from", from.toISOString());
  if (anonymize) params.set("anonymize", "1");
  const href = `/api/export${params.size ? `?${params}` : ""}`;

  return (
    <dialog ref={dialog} className="share" onClose={onClose} aria-labelledby="share-title">
      <h2 id="share-title">Share this incident</h2>
      <p>Download a replay of what kubexray recorded. Anyone can watch it without access to your cluster.</p>
      <fieldset>
        <label>
          <input type="radio" name="window" checked={!whole} onChange={() => setWhole(false)} disabled={!from} /> From 5 minutes before it started until now
        </label>
        <label>
          <input type="radio" name="window" checked={whole} onChange={() => setWhole(true)} /> Everything recorded
        </label>
      </fieldset>
      <label className="check">
        <input type="checkbox" checked={anonymize} onChange={(e) => setAnonymize(e.target.checked)} /> Anonymize names, labels and images
        <small>Kinds, statuses, image tags and the cause stay readable.</small>
      </label>
      <p className="note">Replays never contain Secret or ConfigMap contents, environment values or annotations.</p>
      <div className="share-actions">
        <a className="primary" href={href} download>
          Download replay
        </a>
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
      <p className="note">
        Open it with <code>kubexray play &lt;file&gt;</code>, or drop it on the{" "}
        <a href={PLAYER_URL} target="_blank" rel="noreferrer">
          kubexray player
        </a>
        .
      </p>
    </dialog>
  );
}
