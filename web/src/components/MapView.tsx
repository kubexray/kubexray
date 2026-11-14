import { memo, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { Node } from "../gen/graph";
import { FeedSets } from "../lib/feedContext";
import { ago } from "../lib/format";
import { podHealth, podText } from "../lib/health";
import { highlightClasses, useHighlight } from "../lib/highlight";
import { useEscape } from "../lib/keys";
import { useNow } from "../lib/useNow";
import { driftOf, Helm, type Owner, ownerOf, releaseId, TOOL_NAME } from "../lib/helm";
import { incidentCount, type NamespaceView, type Row as ViewRow, type Workload, wireTarget, workloadHealth, workloadProblem } from "../lib/view";

export type { Highlight } from "../lib/highlight";

const KIND_LABEL: Record<string, string> = { PersistentVolumeClaim: "Volume" };

/** Pods drawn per app; the rest are a count. A 500-replica DaemonSet is
 * read from its numbers, not its dots. */
export const MAX_PODS = 20;

// What lights up comes from the highlight store (lib/highlight), so hovering
// renders only the cards whose look changes.
interface Props {
  view: NamespaceView[];
  open: Map<string, boolean>;
  causeOf: (appId: string) => string | null;
  onToggle: (ns: string, open: boolean) => void;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}

/** What each namespace needs: stable between updates, so namespaces that
 * didn't change skip rendering. */
type NamespaceProps = Omit<Props, "view" | "open"> & { ns: NamespaceView; isOpen?: boolean };

/** The open-state key of the group holding Kubernetes' own namespaces. */
export const SYSTEM = "_system";
const nsId = (name: string) => `ns-${name}`;

export const MapView = memo(function MapView(props: Props) {
  const apps = props.view.filter((ns) => !ns.system);
  const system = props.view.filter((ns) => ns.system);
  // The namespace in view, marked in the bar: the first whose card still
  // shows below the bar. After a jump, the one jumped to, while it scrolls.
  const map = useRef<HTMLDivElement>(null);
  const [current, setCurrent] = useState<string>();
  const settling = useRef(0);
  useEffect(() => {
    const el = map.current;
    if (!el) return;
    const spy = () => {
      if (performance.now() < settling.current) return;
      const top = el.getBoundingClientRect().top + (el.querySelector(".ns-bar")?.getBoundingClientRect().height ?? 0) + 16;
      const shown = [...el.querySelectorAll<HTMLElement>("details.ns[id^='ns-']")].find((ns) => ns.getBoundingClientRect().bottom > top);
      setCurrent(shown?.id.slice(3));
    };
    const frame = requestAnimationFrame(spy);
    el.addEventListener("scroll", spy, { passive: true });
    return () => {
      cancelAnimationFrame(frame);
      el.removeEventListener("scroll", spy);
    };
  }, []);
  // A namespace from the bar: opened, and scrolled to.
  const jump = (name: string) => {
    if (system.some((ns) => ns.name === name)) props.onToggle(SYSTEM, true);
    props.onToggle(name, true);
    setCurrent(name);
    settling.current = performance.now() + 900;
    requestAnimationFrame(() => document.getElementById(nsId(name))?.scrollIntoView({ block: "start", behavior: "smooth" }));
  };
  return (
    <div className="map" aria-label="Cluster map" ref={map}>
      {props.view.length > 1 && <NamespaceBar apps={apps} system={system} current={current} onJump={jump} />}
      {apps.map((ns) => (
        <Namespace key={ns.name} ns={ns} isOpen={props.open.get(ns.name)} causeOf={props.causeOf} onToggle={props.onToggle} onSelect={props.onSelect} onHover={props.onHover} />
      ))}
      {system.length > 0 && <SystemGroup system={system} {...props} />}
    </div>
  );
});

const chipCount = (ns: NamespaceView) =>
  ns.problems ? incidentCount(ns.problems) : ns.workloadCount ? String(ns.workloadCount) : ns.serviceCount ? `${ns.serviceCount} svc` : "empty";

/** Every namespace at a glance, always in view: a click opens one and
 * scrolls to it. Kubernetes' own namespaces sit in a menu. */
function NamespaceBar({ apps, system, current, onJump }: { apps: NamespaceView[]; system: NamespaceView[]; current?: string; onJump: (ns: string) => void }) {
  const [menu, setMenu] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!menu) return;
    const away = (e: PointerEvent) => !box.current?.contains(e.target as globalThis.Node) && setMenu(false);
    addEventListener("pointerdown", away);
    return () => removeEventListener("pointerdown", away);
  }, [menu]);
  useEscape(menu, () => setMenu(false));
  const sysProblems = system.reduce((n, ns) => n + ns.problems, 0);
  return (
    <nav className="ns-bar" aria-label="Namespaces">
      <div className="ns-bar-list">
        {apps.map((ns) => (
          <button
            key={ns.name}
            type="button"
            className={`ns-chip${ns.problems ? " bad" : ""}${current === ns.name ? " on" : ""}`}
            aria-current={current === ns.name || undefined}
            onClick={() => onJump(ns.name)}
          >
            <i aria-hidden="true" />
            {ns.name} <em>{chipCount(ns)}</em>
          </button>
        ))}
      </div>
      {system.length > 0 && (
        <>
          <span className="ns-sep" aria-hidden="true" />
          <div className="ns-sys" ref={box}>
            <button
              type="button"
              className={`ns-chip sys${sysProblems ? " bad" : ""}${menu ? " open" : ""}${system.some((ns) => ns.name === current) ? " on" : ""}`}
              aria-haspopup="menu"
              aria-expanded={menu}
              onClick={() => setMenu(!menu)}
            >
              System <em>{sysProblems ? incidentCount(sysProblems) : system.length}</em>
              <span aria-hidden="true">{menu ? "▴" : "▾"}</span>
            </button>
            {menu && (
              <div className="ns-menu" role="menu">
                {system.map((ns) => (
                  <button
                    key={ns.name}
                    type="button"
                    role="menuitem"
                    onClick={() => {
                      setMenu(false);
                      onJump(ns.name);
                    }}
                  >
                    <i className={ns.problems ? "bad" : undefined} aria-hidden="true" />
                    {ns.name}
                    <em>{ns.problems ? incidentCount(ns.problems) : ns.workloadCount ? `${ns.workloadCount} app${ns.workloadCount === 1 ? "" : "s"}` : "empty"}</em>
                  </button>
                ))}
                <p>Kubernetes&apos; own namespaces.</p>
              </div>
            )}
          </div>
        </>
      )}
    </nav>
  );
}

/** Kubernetes' own namespaces, folded into one group: open when one of
 * them has a problem. */
function SystemGroup({ system, ...props }: Props & { system: NamespaceView[] }) {
  const problems = system.reduce((n, ns) => n + ns.problems, 0);
  const apps = system.reduce((n, ns) => n + ns.workloadCount, 0);
  const isOpen = props.open.get(SYSTEM) ?? system.some((ns) => !ns.allHealthy);
  return (
    <details
      className="ns system-group"
      open={isOpen}
      onToggle={(e) => {
        if (e.currentTarget.open !== isOpen) props.onToggle(SYSTEM, e.currentTarget.open);
      }}
    >
      <summary>
        <span className="ns-name">System</span>
        <span className={`ns-meta ${problems ? "bad" : ""}`}>
          {system.length} namespaces · {apps} apps · {problems ? incidentCount(problems) : system.every((ns) => ns.allHealthy) ? "all healthy" : "not all ready"}
        </span>
      </summary>
      {isOpen && (
        <div className="ns-group">
          {system.map((ns) => (
            <Namespace key={ns.name} ns={ns} isOpen={props.open.get(ns.name)} causeOf={props.causeOf} onToggle={props.onToggle} onSelect={props.onSelect} onHover={props.onHover} />
          ))}
        </div>
      )}
    </details>
  );
}

/** A closed namespace's pods, as dots: what's running at a glance. */
function PodDots({ ns }: { ns: NamespaceView }) {
  const pods = ns.rows.flatMap((r) => r.workload?.pods ?? []);
  if (!pods.length) return null;
  const shown = pods.slice(0, 40);
  const failing = pods.filter((p) => podHealth(p) === "crit").length;
  return (
    <span className="ns-pods" role="img" aria-label={`${pods.length} pod${pods.length === 1 ? "" : "s"}${failing ? `, ${failing} failing` : ""}`}>
      {shown.map((p) => (
        <i key={p.id} className={podHealth(p)} />
      ))}
      {pods.length > shown.length && <em>+{pods.length - shown.length}</em>}
    </span>
  );
}

/** The tool managing a row: its app's, else its Service's or Ingress's. */
function rowOwner(row: ViewRow): Owner | undefined {
  const n = row.workload?.node ?? row.services[0] ?? row.ingresses[0];
  return n ? ownerOf(n) : undefined;
}
const ownerKey = (o: Owner) => `${o.tool}/${o.name}`;

/** Rows grouped by what manages them, in the order each first appears;
 * rows nothing manages stay last, in their order. */
function groupRows(rows: ViewRow[]): ViewRow[] {
  const order = new Map<string, number>();
  for (const r of rows) {
    const o = rowOwner(r);
    if (o && !order.has(ownerKey(o))) order.set(ownerKey(o), order.size);
  }
  const rank = (r: ViewRow) => {
    const o = rowOwner(r);
    return o ? (order.get(ownerKey(o)) ?? 0) : order.size;
  };
  return rows.map((r, i) => ({ r, i })).sort((a, b) => rank(a.r) - rank(b.r) || a.i - b.i).map(({ r }) => r);
}

/** The heading line a row starts, if any: a release's label before its
 * first row, and "not managed" before the first row no tool manages. */
function startsGroup(rows: ViewRow[], i: number): "release" | "loose" | null {
  const o = rows[i] && rowOwner(rows[i]);
  const prev = i > 0 && rows[i - 1] ? rowOwner(rows[i - 1] as ViewRow) : undefined;
  if (o) return !prev || ownerKey(prev) !== ownerKey(o) ? "release" : null;
  return prev ? "loose" : null;
}

/** An outline around the rows a release (or a GitOps app) manages. */
export interface Release {
  key: string;
  /** What selecting its label selects. */
  id: string;
  tool: Owner["tool"];
  /** "helm · storefront", then "chart 1.2.1 · revision 4", then when the
   * deployed revision was made, shown as "2d ago". */
  name: string;
  detail: string;
  deployedAt?: string;
  /** The cards inside it, by their data-key. */
  cards: string[];
}

function releases(ns: string, rows: ViewRow[], revisionsOf: (release: string, ns: string) => { version: number; status: string; at: string }[]): Release[] {
  const out = new Map<string, Release>();
  for (const row of rows) {
    const o = rowOwner(row);
    if (!o) continue;
    let rel = out.get(ownerKey(o));
    if (!rel) {
      const deployed = o.tool === "helm" ? revisionsOf(o.name, o.ns ?? "").find((r) => r.status === "deployed") : undefined;
      const detail: string[] = [];
      if (o.chart) detail.push(`chart ${o.chart}`);
      if (deployed) detail.push(`revision ${deployed.version}`);
      rel = { key: ownerKey(o), id: releaseId(o, ns), tool: o.tool, name: `${TOOL_NAME[o.tool].toLowerCase()} · ${o.name}`, detail: detail.join(" · "), deployedAt: deployed?.at, cards: [] };
      out.set(rel.key, rel);
    }
    rel.cards.push(...row.ingresses.map((n) => n.id), ...row.services.map((n) => n.id));
    if (row.workload) rel.cards.push(row.workload.node.id, ...[...row.workload.uses.keys()].map((id) => `${row.key}|${id}`));
  }
  return [...out.values()];
}

const Namespace = memo(function Namespace({ ns, isOpen: openState, causeOf, onToggle, onSelect, onHover }: NamespaceProps) {
  const { revisions } = useContext(Helm);
  const rows = useMemo(() => groupRows(ns.rows), [ns.rows]);
  const rels = useMemo(() => releases(ns.name, rows, revisions), [ns.name, rows, revisions]);
  const relOf = new Map(rels.map((r) => [r.key, r]));
  const isOpen = openState ?? (!ns.system || !ns.allHealthy);
  const released = rels.length ? ` · ${rels.length} release${rels.length === 1 ? "" : "s"}` : "";
  const apps = `${ns.workloadCount} app${ns.workloadCount === 1 ? "" : "s"}${released}`;
  const meta = ns.problems
    ? `${apps} · ${incidentCount(ns.problems)}`
    : ns.workloadCount
      ? `${apps} · ${ns.allHealthy ? "all healthy" : "not all ready"}`
      : `${ns.serviceCount} service${ns.serviceCount === 1 ? "" : "s"}`;
  const card = (n: Node, kind: string, key = n.id, extra = "") => <NodeCard key={key} n={n} kind={kind} dataKey={key} extra={extra} onSelect={onSelect} onHover={onHover} />;
  return (
    <details
      id={nsId(ns.name)}
      className="ns"
      open={isOpen}
      onToggle={(e) => {
        // React setting `open` fires toggle too; only remember what the user did.
        if (e.currentTarget.open !== isOpen) onToggle(ns.name, e.currentTarget.open);
      }}
    >
      <summary>
        <span className="ns-name">{ns.name}</span>
        <span className={`ns-meta ${ns.problems ? "bad" : ""}`}>{meta}</span>
        {!isOpen && <PodDots ns={ns} />}
      </summary>
      {isOpen && (
        <div className="lanes">
          <Grid ns={ns} releases={rels}>
            <ColHead title="Way in" sub="Ingress" />
            <ColHead title="Load balancer" sub="Service" />
            <ColHead title="App" sub="Deployment · StatefulSet · DaemonSet · Job. Each dot is a pod, one running copy." />
            <ColHead title="Config & storage" sub="ConfigMap · Secret · Volume" />
            {rows.map((row, i) => {
              const starts = startsGroup(rows, i);
              const rel = starts === "release" ? relOf.get(ownerKey(rowOwner(row) as Owner)) : undefined;
              return (
              <Row key={row.key}>
                {rel && <ReleaseHead rel={rel} onSelect={onSelect} />}
                {starts === "loose" && <div className="release-head loose">Not managed by a tool</div>}
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
                    <WorkloadCard w={row.workload} cause={causeOf(row.workload.node.id)} onSelect={onSelect} onHover={onHover} />
                  )}
                </div>
                <div className="cell uses">
                  {row.workload &&
                    [...row.workload.uses.values()].map((u) =>
                      card(u, KIND_LABEL[u.kind] ?? u.kind, `${row.key}|${u.id}`, u.created ? "chip" : "chip missing"),
                    )}
                </div>
              </Row>
              );
            })}
          </Grid>
        </div>
      )}
    </details>
  );
});

/** A release's label above its first row. */
function ReleaseHead({ rel, onSelect }: { rel: Release; onSelect: (id: string) => void }) {
  const selected = useHighlight((h) => h.selected === rel.id);
  return (
    <div className={`release-head ${rel.tool}`} data-key={`release|${rel.key}`}>
      <button type="button" className={`release-tag${selected ? " selected" : ""}`} onClick={() => onSelect(rel.id)}>
        {rel.tool === "helm" && <HelmIcon />}
        <b>{rel.name}</b>
        {(rel.detail || rel.deployedAt) && (
          <span>
            {rel.detail}
            {rel.deployedAt && <Ago at={rel.deployedAt} prefix={rel.detail ? " · " : ""} />}
          </span>
        )}
      </button>
    </div>
  );
}

/** "2d ago", kept current: the only part of a namespace that ticks. */
function Ago({ at, prefix = "" }: { at: string; prefix?: string }) {
  const now = useNow();
  return (
    <>
      {prefix}
      {ago(at, now)}
    </>
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

interface CardProps {
  n: Node;
  kind: string;
  dataKey: string;
  extra: string;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}

const NodeCard = memo(function NodeCard({ n, kind, dataKey, extra, onSelect, onHover }: CardProps) {
  const className = useHighlight((h) => highlightClasses(n.id, h, `node ${extra}`));
  return (
    <button
      type="button"
      className={className}
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
});

interface WorkloadProps {
  w: Workload;
  cause: string | null;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}

const WorkloadCard = memo(function WorkloadCard({ w, cause, onSelect, onHover }: WorkloadProps) {
  const health = workloadHealth(w);
  const r = w.node.status.replicas;
  const ready = r ? `${r.ready}/${r.desired}` : w.node.kind === "Job" ? w.node.status.phase ?? "" : "";
  const problem = workloadProblem(w);
  const className = useHighlight((h) => highlightClasses(w.node.id, h, `node workload ${health}`));

  return (
    // The card's name row is the button; the pods are buttons of their own
    // beside it, not inside it. A click anywhere else on the card selects
    // the app too (the pointer's shortcut for the same button).
    <div
      className={className}
      data-key={w.node.id}
      onClick={() => onSelect(w.node.id)}
      onMouseEnter={() => onHover(w.node.id)}
      onMouseLeave={() => onHover(null)}
    >
      <button
        type="button"
        className="top"
        aria-label={`${w.node.kind} ${w.node.name}, ${ready} ready${problem ? `, ${problem}` : ""}`}
        onClick={(e) => {
          e.stopPropagation();
          onSelect(w.node.id);
        }}
        onFocus={() => onHover(w.node.id)}
        onBlur={() => onHover(null)}
      >
        <span className="n">{w.node.name}</span>
        <span className="k">{w.node.kind}</span>
        <span className="ready">{ready}</span>
      </button>
      <div className="pods">
        <Pods w={w} onSelect={onSelect} />
        <Drift n={w.node} />
      </div>
      {problem && <div className="problem">{problem}</div>}
      {cause && <div className="caused">{cause}</div>}
    </div>
  );
});

const HEALTH_RANK = { crit: 0, warn: 1, dying: 2, ok: 3 } as Record<string, number>;

/** The pods drawn when there are too many: problems first, so a failing pod
 * is never one of those folded into the count. */
function podsShown(pods: Node[]): Set<Node> | null {
  if (pods.length <= MAX_PODS) return null;
  const ranked = [...pods].sort((a, b) => (HEALTH_RANK[podHealth(a)] ?? 3) - (HEALTH_RANK[podHealth(b)] ?? 3));
  return new Set(ranked.slice(0, MAX_PODS));
}

/** An app's pods as dots, grouped by ReplicaSet during a rollout. */
const Pods = memo(function Pods({ w, onSelect }: { w: Workload; onSelect: (id: string) => void }) {
  // Only a selection among these pods renders them again.
  const selected = useHighlight((h) => (h.selected && w.pods.some((p) => p.id === h.selected) ? h.selected : null));
  const { ordered, rolling, shown } = useMemo(() => {
    // During a rollout, pods belong to different ReplicaSets: old ones first.
    const groups = new Map<string, Node[]>();
    for (const p of w.pods) {
      const rs = p.name.slice(0, p.name.lastIndexOf("-"));
      const g = groups.get(rs);
      if (g) g.push(p);
      else groups.set(rs, [p]);
    }
    const ordered = [...groups.values()].sort((a, b) => (a[0]?.created ?? "").localeCompare(b[0]?.created ?? ""));
    return { ordered, rolling: w.node.kind === "Deployment" && ordered.length > 1, shown: podsShown(w.pods) };
  }, [w]);
  const hidden = shown ? w.pods.length - shown.size : 0;

  return (
    <>
      {(rolling ? ordered : [w.pods]).map((group, i) => {
        const pods = shown ? group.filter((p) => shown.has(p)) : group;
        return (
          <span key={i} className="group">
            {i > 0 && <span className="sep" />}
            {pods.map((p) => (
              <button
                type="button"
                key={p.id}
                className={`pod ${podHealth(p)}${selected === p.id ? " selected" : ""}`}
                title={`${p.name} · ${podText(p)}`}
                aria-label={`Pod ${p.name}: ${podText(p)}`}
                onClick={(e) => {
                  e.stopPropagation();
                  onSelect(p.id);
                }}
              />
            ))}
          </span>
        );
      })}
      {hidden > 0 && (
        <em className="more" title={`${hidden} more pods; select the app to see them all`}>
          {`+${hidden}`}
        </em>
      )}
      {rolling && <span className="caption">rolling out: old → new</span>}
    </>
  );
});

/** A hand edit the next sync of the tool managing an app would undo. Who
 * manages it shows on its release's outline. */
function Drift({ n }: { n: Node }) {
  const sets = useContext(FeedSets);
  const owner = ownerOf(n);
  const drift = owner && driftOf(sets, n.id);
  if (!owner || !drift) return null;
  return (
    <span className="drift-chip" title={drift.drift}>
      <i aria-hidden="true" />
      edited outside {TOOL_NAME[owner.tool]}
    </span>
  );
}

const HelmIcon = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
    <circle cx="12" cy="12" r="8" />
    <path d="M12 2v20M2 12h20M5 5l14 14M19 5 5 19" />
  </svg>
);

interface Path {
  d: string;
  from: string;
  to: string;
}
interface Box {
  key: string;
  tool: string;
  top: number;
  height: number;
}
const pathsKey = (paths: Path[]) => paths.map((p) => p.d).join();
const boxesKey = (boxes: Box[]) => boxes.map((b) => `${b.key}:${b.top}:${b.height}`).join();

// The grid of cards, with orthogonal wires drawn between them once laid out.
function Grid({ ns, releases = [], children }: { ns: NamespaceView; releases?: Release[]; children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [paths, setPaths] = useState<Path[]>([]);
  const [boxes, setBoxes] = useState<Box[]>([]);
  const wiresKey = ns.wires.map((w) => w.join(">")).join(",") + releases.map((r) => r.key + r.cards.join()).join(";");

  useLayoutEffect(() => {
    const grid = ref.current;
    if (!grid) return;
    // The cards, found once per wiring rather than once per wire per
    // redraw; a card replaced since is looked up again.
    const cards = new Map<string, Element>();
    for (const el of grid.querySelectorAll("[data-key]")) cards.set(el.getAttribute("data-key") ?? "", el);
    const card = (key: string) => {
      let el = cards.get(key);
      if (!el?.isConnected) {
        el = grid.querySelector(`[data-key="${CSS.escape(key)}"]`) ?? undefined;
        if (el) cards.set(key, el);
      }
      return el;
    };
    let shownPaths = "";
    let shownBoxes = "";
    const draw = () => {
      const g = grid.getBoundingClientRect();
      // Each card measured once per redraw, however many wires it ends.
      const rects = new Map<string, DOMRect | undefined>();
      const box = (key: string) => {
        if (!rects.has(key)) rects.set(key, card(key)?.getBoundingClientRect());
        return rects.get(key);
      };
      const out: Path[] = [];
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
      // Each release: an outline across the rows, from the middle of its
      // label down to just below its cards.
      const outlines = releases.flatMap((r) => {
        const head = box(`release|${r.key}`);
        const cardRects = r.cards.map(box).filter((b): b is DOMRect => !!b);
        if (!head || !cardRects.length) return [];
        const top = head.top + head.height / 2 - g.top;
        const bottom = Math.max(...cardRects.map((b) => b.bottom)) - g.top + 10;
        return [{ key: r.key, tool: r.tool, top, height: bottom - top }];
      });
      // A resize that moves nothing draws nothing.
      const pk = pathsKey(out);
      if (pk !== shownPaths) {
        shownPaths = pk;
        setPaths(out);
      }
      const bk = boxesKey(outlines);
      if (bk !== shownBoxes) {
        shownBoxes = bk;
        setBoxes(outlines);
      }
    };
    draw();
    const ro = new ResizeObserver(draw);
    ro.observe(grid);
    return () => ro.disconnect();
    // Redraw when the wiring changes; ns.wires is rebuilt on every update.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wiresKey]);

  return (
    <div className="grid" ref={ref}>
      {boxes.map((b) => (
        <div key={b.key} className={`release ${b.tool}`} style={{ top: b.top, height: b.height }} aria-hidden="true" />
      ))}
      {children}
      <Wires paths={paths} />
    </div>
  );
}

/** The wires, lit and dimmed with the hover on their own. */
function Wires({ paths }: { paths: Path[] }) {
  const lit = useHighlight((h) => h.lit);
  const broken = useHighlight((h) => h.broken);
  const wireClass = (from: string, to: string) => {
    if (broken?.has(from) && broken.has(to)) return "incident";
    if (!lit) return "";
    return lit.has(from) && lit.has(to) ? "lit" : "dim";
  };
  return (
    <svg className="wires" aria-hidden="true">
      {paths.map((p) => (
        <path key={`${p.from}>${p.to}`} d={p.d} className={wireClass(p.from, p.to)} />
      ))}
    </svg>
  );
}
