// Manifests and ConfigMap contents as kubexray records them (ADR-0017,
// ADR-0018): each field by its path, "spec.rules[0].host", with its text
// when values are on and masked where it looks like a credential.
import type { Value } from "../gen/graph";

type Fields = Record<string, Value> | undefined;

const SHORT = 40;

/** One line per changed field, "spec.maxReplicas 10 → 3", for the compare
 * panel. Values that aren't shown say only that they changed. */
export function fieldChanges(a: Fields, b: Fields): string[] {
  const out: string[] = [];
  const was = a ?? {};
  const now = b ?? {};
  for (const k of Object.keys(now).sort()) {
    const p = was[k];
    const v = now[k];
    if (!v) continue;
    if (!p) out.push(`${k} added`);
    else if (p.hash !== v.hash) {
      const short = p.text && v.text && p.text.length + v.text.length <= SHORT && !`${p.text}${v.text}`.includes("\n");
      out.push(short ? `${k} ${p.text} → ${v.text}` : `${k} changed`);
    }
  }
  for (const k of Object.keys(was).sort()) if (!(k in now)) out.push(`${k} removed`);
  return out;
}

/** Whether any field has text to show. */
export function hasText(fields: Fields): boolean {
  return Object.values(fields ?? {}).some((v) => v.text !== undefined && v.text !== "");
}

interface Tree {
  /** Members in order; list items are keyed "[label]". */
  kids: Map<string, Tree>;
  value?: Value;
}

const SEGMENT = /^(?:\.?([A-Za-z_][\w-]*)|\["((?:[^"\\]|\\.)*)"\]|\[([^\]]+)\])/;

/** Splits "spec.rules[0].http[\"a.b\"]" into keys and list items. */
export function segments(path: string): string[] {
  const out: string[] = [];
  let rest = path;
  while (rest) {
    const m = SEGMENT.exec(rest);
    if (!m) return [...out, rest];
    if (m[1] !== undefined) out.push(m[1]);
    else if (m[2] !== undefined) out.push(JSON.parse(`"${m[2]}"`) as string);
    else out.push(`[${m[3]}]`);
    rest = rest.slice(m[0].length);
  }
  return out;
}

/** The fields as YAML, rebuilt from their paths. Hidden values read
 * ‹hidden›. */
export function toYaml(fields: Fields): string {
  const root: Tree = { kids: new Map() };
  for (const path of Object.keys(fields ?? {}).sort(byPath)) {
    let t = root;
    for (const s of segments(path)) {
      let next = t.kids.get(s);
      if (!next) t.kids.set(s, (next = { kids: new Map() }));
      t = next;
    }
    t.value = fields?.[path];
  }
  return render(root, "").join("\n");
}

/** Paths in order, with list indexes as numbers: [2] before [10]. */
function byPath(a: string, b: string): number {
  return a.localeCompare(b, undefined, { numeric: true });
}

function scalar(v: Value | undefined): string {
  if (!v) return "";
  if (v.text === undefined || v.text === "") return v.masked === "binary" ? "‹binary›" : v.masked ? "‹hidden›" : '""';
  return v.masked === "size" ? `${v.text}…` : v.text;
}

function render(t: Tree, indent: string): string[] {
  const out: string[] = [];
  for (const [key, kid] of t.kids) {
    const item = key.startsWith("[");
    const head = item ? `${indent}-` : `${indent}${key}:`;
    if (kid.kids.size === 0) {
      const s = scalar(kid.value);
      if (s.includes("\n")) {
        out.push(`${head} |`);
        for (const line of s.split("\n")) out.push(`${indent}  ${line}`);
      } else out.push(s ? `${head} ${s}` : head);
      continue;
    }
    const body = render(kid, `${indent}  `);
    if (item && body.length) {
      // "- name: api" on one line, as YAML writes list items.
      out.push(`${indent}- ${(body[0] ?? "").slice(indent.length + 2)}`, ...body.slice(1));
    } else out.push(head, ...body);
  }
  return out;
}
