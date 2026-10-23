// Fails the build when the shipped JS exceeds the size budget (spec §1: fast and light).
//   entry  = the entry chunk plus every chunk it imports statically (what the stack list loads)
//   total  = every JS chunk, including lazy ones (CodeMirror, uPlot)
// Sizes are gzip level 9 of the emitted files. Usage: node scripts/check-bundle.mjs [distDir]
import { readdirSync, readFileSync, existsSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { gzipSync } from 'node:zlib';

export const ENTRY_BUDGET = 120 * 1024;
export const TOTAL_BUDGET = 500 * 1024;
// Fonts are woff2 (already compressed), so they are counted raw, not gzipped.
export const FONT_BUDGET = 200 * 1024;

export function measure(distDir) {
  const manifest = JSON.parse(readFileSync(join(distDir, '.vite', 'manifest.json'), 'utf8'));
  const gz = (file) => gzipSync(readFileSync(join(distDir, file)), { level: 9 }).length;

  const entryKey = Object.keys(manifest).find((k) => manifest[k].isEntry);
  if (!entryKey) throw new Error('no entry chunk in the manifest');

  const eager = new Set();
  const visit = (key) => {
    if (eager.has(key)) return;
    eager.add(key);
    for (const dep of manifest[key].imports ?? []) visit(dep);
  };
  visit(entryKey);

  const js = (chunk) => chunk.file.endsWith('.js');
  const all = Object.values(manifest).filter(js);
  const entry = [...eager].map((k) => manifest[k]).filter(js);
  const heavy = entry.filter((c) => /(^|\/)(cm|uplot)[-.]/i.test(c.file) || /^(cm|uplot)/i.test(c.name ?? ''));
  const assetsDir = join(distDir, 'assets');
  const fonts = existsSync(assetsDir)
    ? readdirSync(assetsDir)
        .filter((f) => f.endsWith('.woff2'))
        .reduce((n, f) => n + readFileSync(join(assetsDir, f)).length, 0)
    : 0;
  return {
    fonts,
    heavyInEntry: heavy.map((c) => c.file),
    entry: entry.reduce((n, c) => n + gz(c.file), 0),
    total: all.reduce((n, c) => n + gz(c.file), 0),
    lazy: all.filter((c) => !entry.includes(c)).map((c) => ({ file: c.file, gz: gz(c.file) })),
  };
}

export function verdict(sizes) {
  const problems = [];
  if (sizes.entry > ENTRY_BUDGET) problems.push(`entry ${sizes.entry} B gz exceeds ${ENTRY_BUDGET} B`);
  if (sizes.total > TOTAL_BUDGET) problems.push(`total ${sizes.total} B gz exceeds ${TOTAL_BUDGET} B`);
  if (sizes.fonts > FONT_BUDGET) problems.push(`fonts ${sizes.fonts} B exceeds ${FONT_BUDGET} B`);
  for (const f of sizes.heavyInEntry ?? []) problems.push(`${f} must be a lazy chunk but is loaded eagerly`);
  return problems;
}

if (import.meta.url === `file://${process.argv[1]}` || process.argv[1]?.endsWith('check-bundle.mjs')) {
  const dist = resolve(process.argv[2] ?? 'dist');
  const kb = (n) => (n / 1024).toFixed(1);
  const sizes = measure(dist);
  console.log(`bundle: entry ${kb(sizes.entry)} kB gz (budget ${kb(ENTRY_BUDGET)}), total ${kb(sizes.total)} kB gz (budget ${kb(TOTAL_BUDGET)}), fonts ${kb(sizes.fonts)} kB (budget ${kb(FONT_BUDGET)})`);
  for (const l of sizes.lazy.sort((a, b) => b.gz - a.gz)) console.log(`  lazy ${l.file}: ${kb(l.gz)} kB gz`);
  const problems = verdict(sizes);
  if (problems.length > 0) {
    console.error(`bundle budget exceeded:\n  ${problems.join('\n  ')}`);
    process.exit(1);
  }
}
