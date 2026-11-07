import { dedent, serviceBlock } from '$lib/stacks/composeText';

const indentOf = (line: string) => line.length - line.trimStart().length;
const unquote = (text: string) => text.trim().replace(/^(["'])(.*)\1$/, '$2');

/**
 * The services one service lists under `depends_on`, read from its block in the compose text (list, map and
 * inline spellings). This is a line scan, not a YAML parse, to keep the YAML library out of this page's bundle.
 */
export function dependsOn(compose: string, service: string): string[] {
  const block = serviceBlock(compose, service);
  if (block === undefined) return [];
  const lines = dedent(block).split('\n').slice(1); // drop the `<service>:` line
  const at = lines.findIndex((l) => /^\s*depends_on:/.test(l));
  if (at < 0) return [];
  const keyIndent = indentOf(lines[at]!);
  const inline = lines[at]!.replace(/^\s*depends_on:/, '').replace(/\s+#.*$/, '').trim();
  if (inline.startsWith('[')) {
    return inline.replace(/^\[|\]$/g, '').split(',').map(unquote).filter(Boolean);
  }
  if (inline.startsWith('{')) {
    return inline.replace(/^\{|\}$/g, '').split(/,(?![^{]*\})/).map((entry) => unquote(entry.split(':')[0]!)).filter(Boolean);
  }
  const names: string[] = [];
  let childIndent: number | undefined;
  for (const line of lines.slice(at + 1)) {
    if (line.trim() === '' || line.trimStart().startsWith('#')) continue;
    const indent = indentOf(line);
    if (indent <= keyIndent && !line.trimStart().startsWith('- ')) break;
    childIndent ??= indent;
    if (indent !== childIndent) continue; // deeper lines are options of one dependency (condition, restart)
    const item = line.trim();
    if (item.startsWith('- ')) names.push(unquote(item.slice(2).replace(/\s+#.*$/, '')));
    else names.push(unquote(item.split(':')[0]!));
  }
  return names.filter(Boolean);
}

/** The services of the stack that list `service` under `depends_on`, in the stack's order. */
export function neededBy(compose: string, service: string, services: string[]): string[] {
  return services.filter((other) => other !== service && dependsOn(compose, other).includes(service));
}
