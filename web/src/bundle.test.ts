import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { describe, expect, it } from 'vitest';
// @ts-expect-error plain .mjs script without types
import { ENTRY_BUDGET, measure, verdict } from '../scripts/check-bundle.mjs';

function fakeDist(files: Record<string, number>, manifest: Record<string, unknown>) {
  const dir = mkdtempSync(join(tmpdir(), 'bundle-'));
  mkdirSync(join(dir, '.vite'));
  mkdirSync(join(dir, 'assets'));
  for (const [name, bytes] of Object.entries(files)) writeFileSync(join(dir, name), randomBytes(bytes));
  writeFileSync(join(dir, '.vite', 'manifest.json'), JSON.stringify(manifest));
  return dir;
}

describe('bundle budget', () => {
  const manifest = {
    'index.html': { file: 'assets/index.js', isEntry: true, imports: ['shared'], dynamicImports: ['lazy'] },
    shared: { file: 'assets/shared.js' },
    lazy: { file: 'assets/lazy.js' },
  };

  it('counts the entry with its static imports but not lazy chunks', () => {
    const dir = fakeDist({ 'assets/index.js': 1000, 'assets/shared.js': 2000, 'assets/lazy.js': 5000 }, manifest);
    const s = measure(dir);
    expect(s.entry).toBeGreaterThan(3000);
    expect(s.entry).toBeLessThan(3200);
    expect(s.total).toBeGreaterThan(8000);
    expect(s.lazy.map((l: { file: string }) => l.file)).toEqual(['assets/lazy.js']);
    expect(verdict(s)).toEqual([]);
  });

  it('fails when the eager bundle is over budget even if the total is fine', () => {
    const dir = fakeDist({ 'assets/index.js': ENTRY_BUDGET + 5000, 'assets/shared.js': 10, 'assets/lazy.js': 10 }, manifest);
    expect(verdict(measure(dir)).join()).toMatch(/entry/);
  });

  it('fails when the total is over budget', () => {
    const dir = fakeDist({ 'assets/index.js': 1000, 'assets/shared.js': 10, 'assets/lazy.js': 401 * 1024 }, manifest);
    expect(verdict(measure(dir)).join()).toMatch(/total/);
  });

  it('fails when a CodeMirror or uPlot chunk is loaded eagerly', () => {
    const eager = {
      'index.html': { file: 'assets/index.js', isEntry: true, imports: ['cm'] },
      cm: { file: 'assets/cm-abc.js', name: 'cm' },
    };
    const dir = fakeDist({ 'assets/index.js': 100, 'assets/cm-abc.js': 100 }, eager);
    expect(verdict(measure(dir)).join()).toMatch(/lazy/);
  });
});
