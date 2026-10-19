import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { randomBytes } from 'node:crypto';
import { describe, expect, it } from 'vitest';
// @ts-expect-error plain .mjs script without types
import { ENTRY_BUDGET, FONT_BUDGET, measure, verdict } from '../scripts/check-bundle.mjs';

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

describe('font budget', () => {
  const manifest = { 'index.html': { file: 'assets/index.js', isEntry: true } };

  it('counts woff2 bytes against FONT_BUDGET and keeps them out of entry/total', () => {
    const dir = fakeDist({ 'assets/index.js': 1000, 'assets/a.woff2': 30000, 'assets/b.woff2': 20000 }, manifest);
    const s = measure(dir);
    expect(s.fonts).toBe(50000);
    expect(s.entry).toBeLessThan(1200);
    expect(s.total).toBeLessThan(1200);
    expect(verdict(s)).toEqual([]);
  });

  it('fails when fonts exceed the budget', () => {
    const dir = fakeDist({ 'assets/index.js': 100, 'assets/a.woff2': FONT_BUDGET + 1 }, manifest);
    expect(verdict(measure(dir)).join()).toMatch(/fonts \d+ B exceeds \d+ B/);
  });
});

describe('public logos', () => {
  const pub = (name: string) => join(process.cwd(), 'public', name);
  const pngWidth = (name: string) => readFileSync(pub(name)).readUInt32BE(16); // IHDR width

  it('containers.png stays within its size cap at 68 px', () => {
    expect(statSync(pub('containers.png')).size).toBeLessThanOrEqual(8192);
    expect(pngWidth('containers.png')).toBe(68);
  });

  it('logo-art.png is 336 px wide', () => {
    expect(pngWidth('logo-art.png')).toBe(336);
  });

  it('logo-art.png stays within its 40 960 B cap', () => {
    expect(statSync(pub('logo-art.png')).size).toBeLessThanOrEqual(40960);
  });
});

describe('react app build shape', () => {
  const dist = join(process.cwd(), 'dist-app');
  const manifestPath = join(dist, '.vite', 'manifest.json');
  const lazyNames = [
    'StackDetail', 'StackCreate', 'StackImport', 'ContainerList', 'ContainerDetail', 'NetworkList',
    'Storage', 'StorageImage', 'StorageVolume', 'Settings', 'Login', 'Setup', 'NotFound',
    'CommandPalette', 'cm',
  ];
  // Chunk files are named assets/<Name>-<hash>.js (see chunkFileNames in vite.app.config.ts).
  const chunkName = (file: string) => file.replace(/^assets\//, '').replace(/-[\w-]{8}\.js$/, '');

  it.skipIf(!existsSync(manifestPath))('keeps every screen and the palette out of the eager entry', () => {
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    expect(Object.keys(manifest).find((k) => manifest[k].isEntry)).toBe('app.html');

    const sizes = measure(dist);
    const eager = readdirSync(join(dist, 'assets'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => `assets/${f}`)
      .filter((f) => !sizes.lazy.some((l: { file: string }) => l.file === f))
      .map(chunkName);
    for (const name of lazyNames) expect(eager).not.toContain(name);

    const lazy = sizes.lazy.map((l: { file: string }) => chunkName(l.file));
    for (const name of lazyNames) expect(lazy).toContain(name);
    expect(verdict(sizes)).toEqual([]);
  });
});
