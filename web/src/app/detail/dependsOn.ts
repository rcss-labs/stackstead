import { dedent, serviceBlock } from '$lib/stacks/composeText';

const indentOf = (line: string) => line.length - line.trimStart().length;
const blank = (line: string) => line.trim() === '' || line.trimStart().startsWith('#');
const unquote = (text: string) => text.trim().replace(/^(["'])(.*)\1$/, '$2');
/** A comment starts at ` #` outside quotes; names never contain one, so quoted text is left alone. */
const stripComment = (text: string) => text.replace(/(["'][^"']*["'])|\s+#.*$/g, (_m, quoted) => quoted ?? '').trim();
/** YAML features a line scan cannot follow: aliases, anchors, tags, block scalars. */
const UNREADABLE = /^[*&!|>]/;

/** Splits flow content on commas at depth 0. */
function flowEntries(inner: string): string[] | undefined {
  const out: string[] = [];
  let depth = 0;
  let current = '';
  for (const ch of inner) {
    if (ch === '[' || ch === '{') depth++;
    if (ch === ']' || ch === '}') depth--;
    if (depth < 0) return undefined;
    if (ch === ',' && depth === 0) {
      out.push(current);
      current = '';
    } else current += ch;
  }
  if (depth !== 0) return undefined;
  out.push(current);
  return out.map((e) => e.trim()).filter(Boolean);
}

/**
 * The services one service lists under `depends_on`, read from its block in the compose text (list, map and inline
 * spellings). This is a line scan, not a YAML parse, which keeps the YAML library out of this page's bundle.
 * It answers undefined, never a guess, for anything it cannot follow reliably: aliases, anchors, tags, merge keys
 * or `extends` that may supply the key, multi-line flow collections, and a service block it cannot isolate.
 */
export function dependsOn(compose: string, service: string): string[] | undefined {
  const block = serviceBlock(compose, service);
  if (block === undefined) return undefined;
  const lines = dedent(block).split('\n').slice(1); // drop the `<service>:` line
  const first = lines.find((l) => !blank(l));
  if (first === undefined) return [];
  if (first.includes('\t')) return undefined;
  const keyIndent = indentOf(first); // the service's own keys sit here; deeper `depends_on` keys belong to other blocks
  const own = lines.filter((l) => !blank(l) && indentOf(l) === keyIndent);
  if (own.some((l) => /^\s*(<<|extends)\s*:/.test(l))) return undefined;

  const at = lines.findIndex((l) => indentOf(l) === keyIndent && /^\s*(["']?)depends_on\1\s*:/.test(l));
  if (at < 0) return [];
  const inline = stripComment(lines[at]!.replace(/^\s*["']?depends_on["']?\s*:/, ''));

  if (inline !== '') {
    if (UNREADABLE.test(inline)) return undefined;
    const map = inline.startsWith('{') && inline.endsWith('}');
    if ((inline.startsWith('[') && inline.endsWith(']')) || map) {
      return flowEntries(inline.slice(1, -1))?.map((e) => unquote(map ? e.split(':')[0]! : e));
    }
    return undefined; // a bare scalar, or a flow collection that continues on the next line
  }

  const body: string[] = [];
  for (const line of lines.slice(at + 1)) {
    if (blank(line)) continue;
    const indent = indentOf(line);
    // A list may sit at the key's own indent; anything else at or above it ends the value.
    if (indent < keyIndent || (indent === keyIndent && !/^-(\s|$)/.test(line.trimStart()))) break;
    body.push(line);
  }
  if (body.length === 0) return [];
  const itemIndent = indentOf(body[0]!);
  const items = body.filter((l) => indentOf(l) === itemIndent);
  if (body.some((l) => UNREADABLE.test(l.trim().replace(/^-\s+/, '')))) return undefined;

  if (items.every((l) => /^-(\s|$)/.test(l.trimStart()))) {
    const names = items.map((l) => stripComment(l.trimStart().slice(1)));
    return names.some((n) => n === '' || n.includes(': ')) ? undefined : names.map(unquote);
  }
  const names: string[] = [];
  for (const l of items) {
    const match = /^(["']?)([^"':]+)\1\s*:(\s|$)/.exec(l.trim());
    if (!match) return undefined;
    names.push(match[2]!);
  }
  return names;
}

/**
 * The services of the stack that list `service` under `depends_on`, in the stack's order. Undefined when any other
 * service could not be read reliably, because that one might need it.
 */
export function neededBy(compose: string, service: string, services: string[]): string[] | undefined {
  const out: string[] = [];
  for (const other of services) {
    if (other === service) continue;
    const deps = dependsOn(compose, other);
    if (deps === undefined) return undefined;
    if (deps.includes(service)) out.push(other);
  }
  return out;
}
