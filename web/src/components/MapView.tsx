import { useLayoutEffect, useRef, useState } from "react";
import type { Node } from "../gen/graph";
import { podHealth, podText } from "../lib/health";
import { type NamespaceView, type Workload, wireTarget, workloadHealth, workloadProblem } from "../lib/view";

const KIND_LABEL: Record<string, string> = { PersistentVolumeClaim: "Volume" };

export interface Highlight {
  /** Cards lit by hovering, or null. */
  lit: Set<string> | null;
  hovered: string | null;
  /** The broken path of the current incident, or null. */
  broken: Set<string> | null;
  selected: string | null;
}

interface Props {
  view: NamespaceView[];
  open: Map<string, boolean>;
  highlight: Highlight;
  causeOf: (appId: string) => string | null;
  onToggle: (ns: string, open: boolean) => void;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}

export function MapView(props: Props) {
  return (
    <div className="map" aria-label="Cluster map">
      {props.view.map((ns) => (
        <Namespace key={ns.name} ns={ns} {...props} />
      ))}
    </div>
  );
}

function Namespace({ ns, open, highlight, causeOf, onToggle, onSelect, onHover }: Props & { ns: NamespaceView }) {
  const isOpen = open.get(ns.name) ?? (!ns.system || !ns.allHealthy);
  const meta = ns.problems
    ? `${ns.workloadCount} apps · ${ns.problems} need${ns.problems === 1 ? "s" : ""} attention`
    : ns.workloadCount
      ? `${ns.workloadCount} app${ns.workloadCount === 1 ? "" : "s"} · all healthy`
      : `${ns.serviceCount} service${ns.serviceCount === 1 ? "" : "s"}`;
  const card = (n: Node, kind: string, key = n.id, extra = "") => (
    <NodeCard key={key} n={n} kind={kind} dataKey={key} extra={extra} highlight={highlight} onSelect={onSelect} onHover={onHover} />
  );
  return (
    <details className="ns" open={isOpen} onToggle={(e) => onToggle(ns.name, e.currentTarget.open)}>
      <summary>
        <span className="ns-name">{ns.name}</span>
        <span className={`ns-meta ${ns.problems ? "bad" : ""}`}>{meta}</span>
      </summary>
      {isOpen && (
        <div className="lanes">
          <Grid ns={ns} highlight={highlight}>
            <ColHead title="Way in" sub="Ingress" />
            <ColHead title="Load balancer" sub="Service" />
            <ColHead title="App" sub="Deployment · StatefulSet · DaemonSet · Job. Each dot is a pod, one running copy." />
            <ColHead title="Config & storage" sub="ConfigMap · Secret · Volume" />
            {ns.rows.map((row) => (
              <Row key={row.key}>
                <div className="cell">{row.ingresses.map((i) => card(i, "Ingress"))}</div>
                <div className="cell">
                  {row.services.map((s) => (
                    <div key={s.id} className="svc">
                      {card(s, "Service")}
                      {!ns.serviceTargets.get(s.id)?.size && s.name !== "kubernetes" && <span className="warn-note">selects no pods</span>}
                    </div>
                  ))}
                </div>
                <div className="cell">
                  {row.workload && (
                    <WorkloadCard w={row.workload} cause={causeOf(row.workload.node.id)} highlight={highlight} onSelect={onSelect} onHover={onHover} />
                  )}
                </div>
                <div className="cell uses">
                  {row.workload &&
                    [...row.workload.uses.values()].map((u) =>
                      card(u, KIND_LABEL[u.kind] ?? u.kind, `${row.key}|${u.id}`, u.created ? "chip" : "chip missing"),
                    )}
                </div>
              </Row>
            ))}
          </Grid>
        </div>
      )}
    </details>
  );
}

function ColHead({ title, sub }: { title: string; sub: string }) {
  return (
    <div className="colhead">
      <b>{title}</b>
      <small>{sub}</small>
    </div>
  );
}

// Rows are just their four cells laid into the parent grid.
function Row({ children }: { children: React.ReactNode }) {
  return <>{children}</>;
}

function classes(n: Node, h: Highlight, base: string): string {
  const c = [base];
  if (h.lit && !h.lit.has(n.id)) c.push("dim");
  if (h.lit?.has(n.id) && h.hovered !== n.id) c.push("lit");
  if (h.broken?.has(n.id)) c.push("incident");
  if (h.selected === n.id) c.push("selected");
  return c.join(" ");
}

interface CardProps {
  n: Node;
  kind: string;
  dataKey: string;
  extra: string;
  highlight: Highlight;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}

function NodeCard({ n, kind, dataKey, extra, highlight, onSelect, onHover }: CardProps) {
  return (
    <button
      type="button"
      className={classes(n, highlight, `node ${extra}`)}
      data-key={dataKey}
      title={extra.includes("missing") ? "Referenced, but doesn't exist" : undefined}
      onClick={() => onSelect(n.id)}
      onMouseEnter={() => onHover(n.id)}
      onMouseLeave={() => onHover(null)}
      onFocus={() => onHover(n.id)}
      onBlur={() => onHover(null)}
    >
      <span className="k">{kind}</span>
      <span className="n">{n.name}</span>
    </button>
  );
}

interface WorkloadProps {
  w: Workload;
  cause: string | null;
  highlight: Highlight;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}

function WorkloadCard({ w, cause, highlight, onSelect, onHover }: WorkloadProps) {
  const health = workloadHealth(w);
  const r = w.node.status.replicas;
  const ready = r ? `${r.ready}/${r.desired}` : w.node.kind === "Job" ? w.node.status.phase ?? "" : "";
  const problem = workloadProblem(w);

  // During a rollout, pods belong to different ReplicaSets: old ones first.
  const groups = new Map<string, Node[]>();
  for (const p of w.pods) {
    const rs = p.name.slice(0, p.name.lastIndexOf("-"));
    groups.set(rs, [...(groups.get(rs) ?? []), p]);
  }
  const ordered = [...groups.values()].sort((a, b) => (a[0]?.created ?? "").localeCompare(b[0]?.created ?? ""));
  const rolling = w.node.kind === "Deployment" && ordered.length > 1;

  return (
    <div
      className={classes(w.node, highlight, `node workload ${health}`)}
      data-key={w.node.id}
      role="button"
      tabIndex={0}
      aria-label={`${w.node.kind} ${w.node.name}, ${ready} ready${problem ? `, ${problem}` : ""}`}
      onClick={() => onSelect(w.node.id)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSelect(w.node.id);
        }
      }}
      onMouseEnter={() => onHover(w.node.id)}
      onMouseLeave={() => onHover(null)}
    >
      <div className="top">
        <span className="n">{w.node.name}</span>
        <span className="k">{w.node.kind}</span>
        <span className="ready">{ready}</span>
      </div>
      <div className="pods">
        {(rolling ? ordered : [w.pods]).map((pods, i) => (
          <span key={i} className="group">
            {i > 0 && <span className="sep" />}
            {pods.map((p) => (
              <button
                type="button"
                key={p.id}
                className={`pod ${podHealth(p)}${highlight.selected === p.id ? " selected" : ""}`}
                title={`${p.name} · ${podText(p)}`}
                aria-label={`Pod ${p.name}: ${podText(p)}`}
                onClick={(e) => {
                  e.stopPropagation();
                  onSelect(p.id);
                }}
              />
            ))}
          </span>
        ))}
        {rolling && <span className="caption">rolling out: old → new</span>}
      </div>
      {problem && <div className="problem">{problem}</div>}
      {cause && <div className="caused">{cause}</div>}
    </div>
  );
}

// The grid of cards, with orthogonal wires drawn between them once laid out.
function Grid({ ns, highlight, children }: { ns: NamespaceView; highlight: Highlight; children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [paths, setPaths] = useState<{ d: string; from: string; to: string }[]>([]);
  const wiresKey = ns.wires.map((w) => w.join(">")).join(",");

  useLayoutEffect(() => {
    const grid = ref.current;
    if (!grid) return;
    const draw = () => {
      const g = grid.getBoundingClientRect();
      const box = (key: string) => grid.querySelector(`[data-key="${CSS.escape(key)}"]`)?.getBoundingClientRect();
      const out: { d: string; from: string; to: string }[] = [];
      for (const [from, to] of ns.wires) {
        const a = box(from);
        const b = box(to);
        if (!a || !b) continue;
        const x1 = a.right - g.left;
        const y1 = a.top + Math.min(a.height / 2, 18) - g.top;
        const x2 = b.left - g.left;
        const y2 = b.top + Math.min(b.height / 2, 18) - g.top;
        const mx = x1 + Math.max(12, (x2 - x1) / 2);
        const r = Math.min(6, Math.abs(y2 - y1) / 2);
        const dy = Math.sign(y2 - y1);
        const d =
          dy === 0
            ? `M${x1},${y1} H${x2}`
            : `M${x1},${y1} H${mx - r} Q${mx},${y1} ${mx},${y1 + dy * r} V${y2 - dy * r} Q${mx},${y2} ${mx + r},${y2} H${x2}`;
        out.push({ d, from: wireTarget(from), to: wireTarget(to) });
      }
      setPaths(out);
    };
    draw();
    const ro = new ResizeObserver(draw);
    ro.observe(grid);
    return () => ro.disconnect();
    // Redraw when the wiring changes; ns.wires is rebuilt on every update.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wiresKey]);

  const wireClass = (from: string, to: string) => {
    if (highlight.broken?.has(from) && highlight.broken.has(to)) return "incident";
    if (!highlight.lit) return "";
    return highlight.lit.has(from) && highlight.lit.has(to) ? "lit" : "dim";
  };
  return (
    <div className="grid" ref={ref}>
      {children}
      <svg className="wires" aria-hidden="true">
        {paths.map((p) => (
          <path key={`${p.from}>${p.to}`} d={p.d} className={wireClass(p.from, p.to)} />
        ))}
      </svg>
    </div>
  );
}
