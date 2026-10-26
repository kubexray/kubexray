import { useEffect, useRef, useState } from "react";
import type { RecommendPreview } from "../gen/server";
import { draftRecommendation, previewRecommendation, type Recommendation } from "../lib/api";
import { shortName } from "../lib/feed";

type Step =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "consent"; preview: RecommendPreview }
  | { kind: "drafting"; preview: RecommendPreview }
  | { kind: "done"; rec: Recommendation }
  | { kind: "error"; message: string };

/** Drafts a guardrail that would have stopped a change from breaking an app,
 * proven against the recording before it's shown (ADR-0014). Nothing is sent
 * until the person has seen exactly what will be. */
export function SafetyRecommendation({ set, app, model }: { set: string; app: string; model: string }) {
  const [step, setStep] = useState<Step>({ kind: "idle" });
  const abort = useRef<AbortController | null>(null);
  useEffect(() => () => abort.current?.abort(), []);

  const run = (f: (signal: AbortSignal) => Promise<void>) => {
    abort.current?.abort();
    const ctl = new AbortController();
    abort.current = ctl;
    f(ctl.signal).catch((err: unknown) => {
      if (!ctl.signal.aborted) setStep({ kind: "error", message: err instanceof Error ? err.message : String(err) });
    });
  };
  const preview = () => {
    setStep({ kind: "loading" });
    run(async (signal) => setStep({ kind: "consent", preview: await previewRecommendation(set, signal) }));
  };
  const draft = (p: RecommendPreview) => {
    setStep({ kind: "drafting", preview: p });
    run(async (signal) => setStep({ kind: "done", rec: await draftRecommendation(set, signal) }));
  };

  return (
    <section className="rec">
      <h3>Prevent it happening again</h3>
      {(step.kind === "idle" || step.kind === "loading") && (
        <>
          <p className="rec-lead">
            Draft an admission policy that would have refused this change, with {model}. kubexray proves it against what happened before you see it, and never
            applies it itself.
          </p>
          <div className="rec-actions">
            <button type="button" className="primary" disabled={step.kind === "loading"} onClick={preview}>
              {step.kind === "loading" ? "Gathering…" : "Draft a safety recommendation"}
            </button>
          </div>
        </>
      )}

      {(step.kind === "consent" || step.kind === "drafting") && (
        <div className="rec-consent">
          <p>
            This sends the text below to <strong>{step.preview.model}</strong>: the incident, {shortName(step.preview.object)} as it is now, how{" "}
            {shortName(step.preview.app)} starts, and the crashed container's last log lines, with credential-like values removed.
          </p>
          <pre className="rec-request" tabIndex={0} aria-label="What will be sent">
            {step.preview.request}
          </pre>
          <div className="rec-actions">
            <button type="button" className="primary" disabled={step.kind === "drafting"} onClick={() => draft(step.preview)}>
              {step.kind === "drafting" ? "Drafting and proving…" : "Send and draft"}
            </button>
            <button
              type="button"
              onClick={() => {
                abort.current?.abort();
                setStep({ kind: "idle" });
              }}
            >
              Cancel
            </button>
          </div>
          {step.kind === "drafting" && <p className="rec-note">A local model can take a minute.</p>}
        </div>
      )}

      {step.kind === "error" && (
        <div className="rec-error" role="alert">
          <p>{step.message}</p>
          <div className="rec-actions">
            <button type="button" onClick={() => setStep({ kind: "idle" })}>
              Back
            </button>
          </div>
        </div>
      )}

      {step.kind === "done" && <Result rec={step.rec} app={shortName(app)} onAgain={preview} />}
    </section>
  );
}

function Result({ rec, app, onAgain }: { rec: Recommendation; app: string; onAgain: () => void }) {
  const proven = rec.proof.checks.length > 0 && rec.proof.checks.every((c) => c.passed);
  const [copied, setCopied] = useState(false);
  const copy = () =>
    navigator.clipboard.writeText(rec.yaml).then(
      () => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      },
      () => prompt("Copy the guardrail", rec.yaml),
    );
  const download = () => {
    const url = URL.createObjectURL(new Blob([rec.yaml], { type: "application/yaml" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "kubexray-guardrail.yaml";
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <div className="rec-result">
      <div className="rec-head">
        <span className={`verdict ${proven ? "fixed" : "broke"}`}>{proven ? "Proven" : "Not proven"}</span>
        {proven && <span className={`verdict ${rec.strict ? "fixed" : "stuck"}`}>{rec.strict ? "Strict" : "Only refuses this value"}</span>}
      </div>
      {rec.title && <p className="rec-title">{rec.title}</p>}
      {rec.rationale && <p className="rec-why">{rec.rationale}</p>}
      {proven && (
        <p className="rec-note">
          {rec.strict
            ? `It also refuses values ${app} was never seen to accept, not only the one that broke it.`
            : `It refuses the value that broke ${app}, but would allow other values ${app} may not accept.`}
        </p>
      )}

      <ul className="rec-checks" aria-label="Proof">
        {rec.proof.checks.map((c) => (
          <li key={c.name} className={c.passed ? "pass" : "fail"}>
            <span aria-hidden="true">{c.passed ? "✓" : "✗"}</span>
            <span>
              {c.name}
              {c.tried ? <span className="rec-tried"> · {c.tried === 1 ? "1 object" : `${c.tried} objects`}</span> : null}
              {c.failures?.map((f) => (
                <span key={f} className="rec-failure">
                  {f}
                </span>
              ))}
            </span>
          </li>
        ))}
      </ul>

      {rec.reconstructed && (
        <p className="rec-note">
          The bad change was rebuilt from the logs: <code>{`${rec.reconstructed.key}=${rec.reconstructed.value}`}</code>, from the line{" "}
          <code>{rec.reconstructed.evidence}</code>. kubexray doesn't keep ConfigMap contents, so it can't confirm that was the exact value.
        </p>
      )}

      {proven ? (
        <>
          <pre className="rec-yaml" tabIndex={0} aria-label="The guardrail">
            {rec.yaml}
          </pre>
          <div className="rec-actions">
            <button type="button" className="primary" onClick={copy}>
              {copied ? "Copied" : "Copy YAML"}
            </button>
            <button type="button" onClick={download}>
              Download
            </button>
          </div>
          <p className="rec-note">
            Apply it with <code>kubectl apply -f kubexray-guardrail.yaml</code>. It starts in Warn and Audit mode: a change that breaks the rule goes through with a warning
            and is logged. To refuse such changes, set <code>validationActions: [Deny]</code> in the binding.
          </p>
        </>
      ) : (
        <div className="rec-actions">
          <button type="button" onClick={onAgain}>
            Try again
          </button>
        </div>
      )}
      <p className="rec-meta">
        {rec.model} · {rec.attempts === 1 ? "1 attempt" : `${rec.attempts} attempts`} · {rec.usage.in + rec.usage.out} tokens
      </p>
    </div>
  );
}
