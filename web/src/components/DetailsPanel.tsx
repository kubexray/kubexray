import { useState } from "react";
import { useNow } from "../lib/useNow";
import type { Incident } from "../gen/cause";
import { ago, who } from "../lib/format";
import { nodeSummary, podHealth, podText } from "../lib/health";
import type { ClusterState } from "../lib/state";
import { allWorkloads, type NamespaceView, workloadHealth, workloadProblem } from "../lib/view";
import { CauseLine, DiagnosisLine } from "./IncidentCard";

interface Props {
  id: string | null;
  state: ClusterState | null;
  view: NamespaceView[];
  /** The cause engine's answer for the selected app, when there is one. */
  incident: Incident | null;
  onSelect: (id: string) => void;
}

export function DetailsPanel({ id, state, view, incident, onSelect }: Props) {
  const now = useNow();
  const n = id ? state?.nodes.get(id) : undefined;
  if (!n || !state) {
    return (
      <section className="panel details" aria-live="polite">
        <h2>Selected</h2>
        <p className="empty">
          {id ? "This object doesn't exist at this point in time." : "Select an app, pod or service to see what it's doing and how it connects."}
        </p>
      </section>
    );
  }

  const link = (target: string) => {
    const t = state.nodes.get(target);
    return t ? (
      <button type="button" key={target} onClick={() => onSelect(target)}>
        {t.name}
      </button>
    ) : (
      <span key={target}>{target.split("/").pop()} (missing)</span>
    );
  };
  const facts: [string, React.ReactNode][] = [];
  const wl = allWorkloads(view).find(({ w }) => w.node.id === n.id || w.pods.some((p) => p.id === n.id));
  const isApp = wl?.w.node.id === n.id;
  const health = n.kind === "Pod" ? podHealth(n) : isApp && wl ? workloadHealth(wl.w) : n.status.ready ? "ok" : "warn";
  const stateText = isApp && wl ? workloadProblem(wl.w) || nodeSummary(n) : nodeSummary(n);

  if (n.kind === "Pod") {
    if (wl && !isApp) facts.push(["App", link(wl.w.node.id)]);
    facts.push(["Node", n.parent?.startsWith("Node/") ? n.parent.slice(5) : "not scheduled yet"]);
  }
  if (isApp && wl) {
    const ns = view.find((v) => v.name === n.ns);
    const from = [...(ns?.serviceTargets ?? [])].filter(([, t]) => t.has(n.id)).map(([s]) => link(s));
    if (from.length) facts.push(["Traffic from", join(from)]);
    if (wl.w.uses.size) facts.push(["Uses", join([...wl.w.uses.keys()].map(link))]);
  }
  if (n.kind === "Service") {
    const targets = [...(view.find((v) => v.name === n.ns)?.serviceTargets.get(n.id) ?? [])];
    facts.push(["Sends traffic to", targets.length ? join(targets.map(link)) : "no pods (its selector matches nothing)"]);
  }
  if (n.kind === "Ingress") {
    facts.push(["Routes to", join([...state.edges.values()].filter((e) => e.type === "routes" && e.from === n.id).map((e) => link(e.to)))]);
  }
  if (n.kind === "ConfigMap" || n.kind === "Secret" || n.kind === "PersistentVolumeClaim") {
    const users = allWorkloads(view).filter(({ w }) => w.uses.has(n.id));
    facts.push(["Used by", users.length ? join(users.map(({ w }) => link(w.node.id))) : "nothing running"]);
    if (n.kind === "Secret") facts.push(["Contents", "never read by kubexray"]);
  }
  const images = (n.spec?.containers ?? []).filter((c) => !c.init);
  if (images.length) facts.push(["Image", join(images.map((c) => <code key={c.name}>{c.image}</code>))]);
  if (n.change) facts.push(["Last change", `${who(n.change)}, ${ago(n.change.at, now)}`]);
  if (n.origin) facts.push(["Managed by", `${n.origin.tool}${n.origin.name ? ` (${n.origin.name})` : ""}`]);
  if (n.ns) facts.push(["Namespace", n.ns]);
  if (n.labels && Object.keys(n.labels).length) {
    facts.push(["Labels", Object.entries(n.labels).map(([k, v]) => `${k}=${v}`).join(", ")]);
  }

  return (
    <section className="panel details" aria-live="polite">
      <h2>Selected</h2>
      <div className="kind">{n.kind}</div>
      <div className="name">{n.name}</div>
      <div className={`state ${health}`}>{stateText}</div>
      {incident && !incident.healthy && <Explanation incident={incident} podId={n.kind === "Pod" ? n.id : null} />}
      <dl className="facts">
        {facts.map(([k, v]) => (
          <div key={k} className="fact">
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      {isApp && wl && wl.w.pods.length > 0 && (
        <ul className="podlist">
          {wl.w.pods.map((p) => (
            <li key={p.id}>
              <button type="button" onClick={() => onSelect(p.id)}>
                <span className={`pod ${podHealth(p)}`} />
                {p.name.startsWith(`${n.name}-`) ? `…${p.name.slice(n.name.length + 1)}` : p.name}
                <span className="st">{podText(p)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Explanation({ incident, podId }: { incident: Incident; podId: string | null }) {
  const top = incident.causes?.[0];
  // A healthy pod of a degraded app gets the app's story only if it's affected.
  if (podId && !incident.symptom.badPods?.includes(podId)) return null;
  return (
    <div className="explain">
      {incident.diagnosis && (
        <>
          <DiagnosisLine diagnosis={incident.diagnosis} />
          {incident.diagnosis.evidence && (
            <ul className="evidence">
              {incident.diagnosis.evidence.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          )}
        </>
      )}
      {top ? <CauseLine cause={top} /> : <p>No cause found: {incident.noCause}.</p>}
      {top?.evidence && (
        <ul className="evidence">
          {top.evidence.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}
      {incident.causes && incident.causes.length > 1 && (
        <p className="also">
          Also changed: {incident.causes.slice(1).map((c) => `${c.summary} (${c.confidence})`).join("; ")}
        </p>
      )}
      {incident.next?.map((s) => <Command key={s.command} command={s.command} note={s.note} />)}
    </div>
  );
}

function Command({ command, note }: { command: string; note?: string }) {
  const [copied, setCopied] = useState(false);
  const [code, setCode] = useState<HTMLElement | null>(null);
  const copy = () => {
    navigator.clipboard
      .writeText(command)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => {
        // No clipboard access: select the text so it can be copied by hand.
        if (!code) return;
        const range = document.createRange();
        range.selectNodeContents(code);
        getSelection()?.removeAllRanges();
        getSelection()?.addRange(range);
      });
  };
  return (
    <div className="cmd-wrap">
      {note && <div className="cmd-note">{note}</div>}
      <div className="cmd">
        <code ref={setCode}>{command}</code>
        <button type="button" onClick={copy}>
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </div>
  );
}

function join(items: React.ReactNode[]): React.ReactNode[] {
  return items.flatMap((x, i) => (i ? [", ", x] : [x]));
}
