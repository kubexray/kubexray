// Shape checks for what a replay file holds. A replay is untrusted (ADR-0008):
// Go decodes into typed structs, so a field of the wrong type fails there,
// but JSON.parse takes anything. These checks make the UI's assumptions true
// (a node always has a status, a name is a string) before anything renders.
import type { Set as ChangeSet } from "../gen/feed";
import type { Edge, Node } from "../gen/graph";

type Obj = Record<string, unknown>;

const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const isStr = (v: unknown): v is string => typeof v === "string";
const isNum = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);
const opt = (v: unknown, check: (v: unknown) => boolean) => v === undefined || v === null || check(v);
const strings = (v: unknown) => Array.isArray(v) && v.every(isStr);
const stringMap = (v: unknown) => isObj(v) && Object.values(v).every(isStr);
const objMap = (v: unknown) => isObj(v) && Object.values(v).every(isObj);

const change = (v: unknown) => isObj(v) && isStr(v.at) && isStr(v.by) && isStr(v.tool);
const origin = (v: unknown) => isObj(v) && isStr(v.tool) && opt(v.name, isStr) && opt(v.namespace, isStr);
const replicas = (v: unknown) => isObj(v) && isNum(v.desired) && isNum(v.ready) && opt(v.updated, isNum);
const container = (v: unknown) => isObj(v) && isStr(v.name) && isStr(v.image);

function status(v: unknown): boolean {
  return (
    isObj(v) &&
    typeof v.ready === "boolean" &&
    opt(v.phase, isStr) &&
    opt(v.reason, isStr) &&
    opt(v.message, isStr) &&
    opt(v.since, isStr) &&
    opt(v.container, isStr) &&
    opt(v.restarts, isNum) &&
    opt(v.exit, isNum) &&
    opt(v.memoryPct, isNum) &&
    opt(v.replicas, replicas) &&
    opt(v.restarted, strings)
  );
}

function spec(v: unknown): boolean {
  return isObj(v) && opt(v.containers, (c) => Array.isArray(c) && c.every(container)) && opt(v.volumes, strings) && opt(v.revision, isStr);
}

/** What's wrong with a node, or null if it has the shape the UI relies on. */
export function nodeProblem(v: unknown): string | null {
  if (!isObj(v) || !isStr(v.id) || !v.id) return "a node without an id";
  const checks: [string, boolean][] = [
    ["kind", isStr(v.kind)],
    ["name", isStr(v.name)],
    ["status", status(v.status)],
    ["created", isStr(v.created)],
    ["ns", opt(v.ns, isStr)],
    ["parent", opt(v.parent, isStr)],
    ["labels", opt(v.labels, stringMap)],
    ["selector", opt(v.selector, stringMap)],
    ["spec", opt(v.spec, spec)],
    ["change", opt(v.change, change)],
    ["earlier", opt(v.earlier, change)],
    ["origin", opt(v.origin, origin)],
    ["data", opt(v.data, objMap)],
    ["manifest", opt(v.manifest, objMap)],
    ["traffic", opt(v.traffic, isObj)],
  ];
  const bad = checks.find(([, ok]) => !ok);
  return bad ? `node ${v.id} has an invalid ${bad[0]}` : null;
}

export const isNode = (v: unknown): v is Node => nodeProblem(v) === null;

export const isEdge = (v: unknown): v is Edge => isObj(v) && isStr(v.id) && !!v.id && isStr(v.type) && isStr(v.from) && isStr(v.to);

const entry = (v: unknown) =>
  isObj(v) && isStr(v.at) && isStr(v.object) && isStr(v.kind) && isStr(v.name) && isStr(v.what) && isStr(v.summary) && opt(v.by, change) && opt(v.origin, origin);

/** A change set from the feed (ADR-0012), as the Changes view reads it. */
export const isChangeSet = (v: unknown): v is ChangeSet =>
  isObj(v) &&
  isStr(v.id) &&
  isStr(v.at) &&
  isStr(v.last) &&
  isStr(v.title) &&
  isStr(v.verdict) &&
  Array.isArray(v.entries) &&
  v.entries.every(entry) &&
  opt(v.apps, strings) &&
  opt(v.fixes, strings) &&
  opt(v.by, change) &&
  opt(v.origin, origin);
