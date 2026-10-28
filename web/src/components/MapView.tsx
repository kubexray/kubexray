import { memo, useContext, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { Node } from "../gen/graph";
import { FeedSets } from "../lib/feedContext";
import { verdictNote } from "../lib/feed";
import { ago, clock } from "../lib/format";
import { podHealth, podText } from "../lib/health";
import { useNow } from "../lib/useNow";
import { driftOf, Helm, type Owner, ownerOf, ownerText, revisionSet, TOOL_NAME } from "../lib/helm";
import { type NamespaceView, type Row as ViewRow, type Workload, wireTarget, workloadHealth, workloadProblem } from "../lib/view";

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

/** The open-state key of the group holding Kubernetes' own namespaces. */
const SYSTEM = "_system";
const nsId = (name: string) => `ns-${name}`;

export const MapView = memo(function MapView(props: Props) {
  const apps = props.view.filter((ns) => !ns.system);
  const system = props.view.filter((ns) => ns.system);
  // A namespace from the bar: opened, and scrolled to.
  const jump = (name: string) => {
    if (system.some((ns) => ns.name === name)) props.onToggle(SYSTEM, true);
    props.onToggle(name, true);
    requestAnimationFrame(() => document.getElementById(nsId(name))?.scrollIntoView({ block: "start", behavior: "smooth" }));
  };
  const current = props.highlight.selected?.split("/")[1]; // "Kind/namespace/name"
  return (
    <div className="map" aria-label="Cluster map">
      {props.view.length > 1 && <NamespaceBar apps={apps} system={system} current={current} onJump={jump} />}
      {apps.map((ns) => (
        <Namespace key={ns.name} ns={ns} {...props} />
      ))}
      {system.length > 0 && <SystemGroup system={system} {...props} />}
    </div>
  );
});

const chipCount = (ns: NamespaceView) =>
  ns.problems ? `${ns.problems} failing` : ns.workloadCount ? String(ns.workloadCount) : ns.serviceCount ? `${ns.serviceCount} svc` : "empty";

/** Every namespace at a glance, always in view: a click opens one and
 * scrolls to it. Kubernetes' own namespaces sit in a menu. */
function NamespaceBar({ apps, system, current, onJump }: { apps: NamespaceView[]; system: NamespaceView[]; current?: string; onJump: (ns: string) => void }) {
  const [menu, setMenu] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!menu) return;
    const away = (e: PointerEvent) => !box.current?.contains(e.target as globalThis.Node) && setMenu(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setMenu(false);
    addEventListener("pointerdown", away);
    addEventListener("keydown", esc);
    return () => {
      removeEventListener("pointerdown", away);
      removeEventListener("keydown", esc);
    };
  }, [menu]);
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
              System <em>{sysProblems ? `${sysProblems} failing` : system.length}</em>
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
                    <em>{ns.problems ? `${ns.problems} failing` : ns.workloadCount ? `${ns.workloadCount} app${ns.workloadCount === 1 ? "" : "s"}` : "empty"}</em>
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
          {system.length} namespaces · {apps} apps · {problems ? `${problems} need${problems === 1 ? "s" : ""} attention` : "all healthy"}
        </span>
      </summary>
      {isOpen && (
        <div className="ns-group">
          {system.map((ns) => (
            <Namespace key={ns.name} ns={ns} {...props} />
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

/** An outline around the rows a release (or a GitOps app) manages. */
export interface Release {
  key: string;
  label: string;
  /** The cards inside it, by their data-key. */
  cards: string[];
}

function releases(rows: ViewRow[], revisionsOf: (release: string, ns: string) => { version: number; status: string; at: string }[], now: number): Release[] {
  const out = new Map<string, Release>();
  for (const row of rows) {
    const o = rowOwner(row);
    if (!o) continue;
    let rel = out.get(ownerKey(o));
    if (!rel) {
      const deployed = o.tool === "helm" ? revisionsOf(o.name, o.ns ?? "").find((r) => r.status === "deployed") : undefined;
      const parts = [o.tool === "helm" ? `helm · ${o.name}` : `${TOOL_NAME[o.tool].toLowerCase()} · ${o.name}`];
      if (o.chart) parts.push(`chart ${o.chart}`);
      if (deployed) parts.push(`revision ${deployed.version}`, ago(deployed.at, now));
      rel = { key: ownerKey(o), label: parts.join(" · "), cards: [] };
      out.set(rel.key, rel);
    }
    rel.cards.push(...row.ingresses.map((n) => n.id), ...row.services.map((n) => n.id));
    if (row.workload) rel.cards.push(row.workload.node.id, ...[...row.workload.uses.keys()].map((id) => `${row.key}|${id}`));
  }
  return [...out.values()];
}

function Namespace({ ns, open, highlight, causeOf, onToggle, onSelect, onHover }: Props & { ns: NamespaceView }) {
  const { revisions } = useContext(Helm);
  const now = useNow();
  const rows = groupRows(ns.rows);
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
          <Grid ns={ns} highlight={highlight} releases={releases(rows, revisions, now)}>
            <ColHead title="Way in" sub="Ingress" />
            <ColHead title="Load balancer" sub="Service" />
            <ColHead title="App" sub="Deployment · StatefulSet · DaemonSet · Job. Each dot is a pod, one running copy." />
            <ColHead title="Config & storage" sub="ConfigMap · Secret · Volume" />
            {rows.map((row) => (
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
        <Managed n={w.node} />
      </div>
      {problem && <div className="problem">{problem}</div>}
      {cause && <div className="caused">{cause}</div>}
    </div>
  );
}

/** Who manages an app (a Helm release, an Argo CD app, a Flux object), and
 * a hand edit its next sync would undo. A Helm badge opens the release's
 * history. */
function Managed({ n }: { n: Node }) {
  const sets = useContext(FeedSets);
  const [anchor, setAnchor] = useState<DOMRect | null>(null);
  const badge = useRef<HTMLButtonElement>(null);
  const owner = ownerOf(n);
  if (!owner) return null;
  const drift = driftOf(sets, n.id);
  const stop = (e: React.SyntheticEvent) => e.stopPropagation(); // not the card's click
  return (
    <span className="managed">
      {drift && (
        <span className="drift-chip" title={drift.drift}>
          <i aria-hidden="true" />
          edited outside {TOOL_NAME[owner.tool]}
        </span>
      )}
      {owner.tool === "helm" ? (
        <button
          type="button"
          ref={badge}
          className="owner helm"
          aria-haspopup="dialog"
          aria-expanded={!!anchor}
          aria-label={`Helm release ${owner.name}${owner.chart ? `, chart ${owner.chart}` : ""}: show its history`}
          onClick={(e) => {
            stop(e);
            setAnchor(anchor ? null : e.currentTarget.getBoundingClientRect());
          }}
          onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && stop(e)}
        >
          <HelmIcon />
          {ownerText(owner)}
        </button>
      ) : (
        <span className="owner">{ownerText(owner)}</span>
      )}
      {anchor && (
        <ReleaseHistory
          owner={owner}
          anchor={anchor}
          onClose={() => {
            setAnchor(null);
            badge.current?.focus();
          }}
        />
      )}
    </span>
  );
}

const HelmIcon = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
    <circle cx="12" cy="12" r="8" />
    <path d="M12 2v20M2 12h20M5 5l14 14M19 5 5 19" />
  </svg>
);

/** A Helm release's revisions, from the records Helm keeps: what each did
 * when kubexray saw it, and a way back to just before it. */
function ReleaseHistory({ owner, anchor, onClose }: { owner: Owner; anchor: DOMRect; onClose: () => void }) {
  const { revisions, onRewind } = useContext(Helm);
  const sets = useContext(FeedSets);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    box.current?.focus(); // Escape, and Tab through its buttons, from here
    const away = (e: PointerEvent) => !box.current?.contains(e.target as globalThis.Node) && onClose();
    addEventListener("pointerdown", away);
    return () => removeEventListener("pointerdown", away);
  }, [onClose]);
  const revs = revisions(owner.name, owner.ns ?? "").slice(0, 6);
  const right = Math.max(8, innerWidth - anchor.right);
  return createPortal(
    <div
      className="release-history"
      role="dialog"
      aria-label={`Helm release ${owner.name}`}
      ref={box}
      tabIndex={-1}
      style={{ top: anchor.bottom + 6, right }}
      onKeyDown={(e) => {
        if (e.key !== "Escape") return;
        e.stopPropagation(); // closes this, not the map's selection
        onClose();
      }}
    >
      <h3>Helm release {owner.name}</h3>
      <p className="sub">{owner.chart ? `Chart ${owner.chart} · ` : ""}revisions, newest first</p>
      {revs.length === 0 ? (
        <p className="empty">Helm&apos;s revision records aren&apos;t visible to kubexray.</p>
      ) : (
        <ol>
          {revs.map((r) => {
            const set = revisionSet(sets, owner, r);
            const note = set && (set.verdict === "broke" || set.verdict === "fixed") ? verdictNote(set) : "";
            return (
              <li key={r.version} className={r.status === "failed" ? "failed" : undefined}>
                <b>#{r.version}</b>
                <span className="what">
                  <span>{set ? (r.version === 1 ? set.title.replace(/^Helm upgrade/, "Helm install") : set.title) : `revision ${r.version}`}</span>
                  <small>
                    {clock(r.at)}
                    {note && ` · ${note}`}
                  </small>
                </span>
                <span className={`status ${r.status === "deployed" ? "ok" : r.status === "failed" ? "bad" : ""}`}>{r.status}</span>
                {onRewind && (
                  <button
                    type="button"
                    onClick={() => {
                      onRewind(new Date(Date.parse(r.at) - 2000).toISOString());
                      onClose();
                    }}
                  >
                    Rewind to just before ›
                  </button>
                )}
              </li>
            );
          })}
        </ol>
      )}
    </div>,
    document.body,
  );
}

// The grid of cards, with orthogonal wires drawn between them once laid out.
function Grid({ ns, highlight, releases = [], children }: { ns: NamespaceView; highlight: Highlight; releases?: Release[]; children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [paths, setPaths] = useState<{ d: string; from: string; to: string }[]>([]);
  const [boxes, setBoxes] = useState<{ key: string; label: string; top: number; left: number; width: number; height: number }[]>([]);
  const wiresKey = ns.wires.map((w) => w.join(">")).join(",") + releases.map((r) => r.label + r.cards.join()).join(";");

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
      // Each release: the box around its cards, a little outside them.
      const pad = 5;
      setBoxes(
        releases.flatMap((r) => {
          const rects = r.cards.map(box).filter((b): b is DOMRect => !!b);
          if (!rects.length) return [];
          const top = Math.min(...rects.map((b) => b.top)) - g.top - pad;
          const bottom = Math.max(...rects.map((b) => b.bottom)) - g.top + pad;
          return [{ key: r.key, label: r.label, top, left: -10, width: g.width + 20, height: bottom - top }];
        }),
      );
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
      {boxes.map((b) => (
        <div key={b.key} className="release" style={{ top: b.top, left: b.left, width: b.width, height: b.height }} aria-hidden="true">
          <span>{b.label}</span>
        </div>
      ))}
      {children}
      <svg className="wires" aria-hidden="true">
        {paths.map((p) => (
          <path key={`${p.from}>${p.to}`} d={p.d} className={wireClass(p.from, p.to)} />
        ))}
      </svg>
    </div>
  );
}
