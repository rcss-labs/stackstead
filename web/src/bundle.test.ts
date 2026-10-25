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
    const dir = fakeDist({ 'assets/index.js': 1000, 'assets/shared.js': 10, 'assets/lazy.js': 501 * 1024 }, manifest);
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

describe('build shape', () => {
  const dist = join(process.cwd(), 'dist');
  const manifestPath = join(dist, '.vite', 'manifest.json');
  const lazyNames = [
    'StackDetail', 'StackCreate', 'StackImport', 'ContainerList', 'ContainerDetail', 'NetworkList',
    'Storage', 'StorageImage', 'StorageVolume', 'Settings', 'Login', 'Setup', 'NotFound',
    'CommandPalette', 'cm', 'uPlot.esm',
  ];
  // Chunk files are named assets/<Name>-<hash>.js (see chunkFileNames in vite.config.ts).
  const chunkName = (file: string) => file.replace(/^assets\//, '').replace(/-[\w-]{8}\.js$/, '');

  it.skipIf(!existsSync(manifestPath))('keeps every screen and the palette out of the eager entry', () => {
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    expect(Object.keys(manifest).find((k) => manifest[k].isEntry)).toBe('index.html');

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

  it.skipIf(!existsSync(manifestPath))('keeps the container screens and the log console out of the eager entry', () => {
    const sizes = measure(dist);
    const lazyFiles = sizes.lazy.map((l: { file: string }) => l.file);
    const eagerFiles = readdirSync(join(dist, 'assets'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => `assets/${f}`)
      .filter((f) => !lazyFiles.includes(f));
    expect(eagerFiles.map(chunkName).some((name) => /Container|LogConsole|Peek/i.test(name))).toBe(false);
    // Ratchet below ENTRY_BUDGET: connected servers moved the server scope into the eager Overview.
    expect(sizes.entry).toBeLessThanOrEqual(110 * 1024);

    // The eager code is one merged chunk (codeSplitting group in vite.config.ts), so a chunk name cannot reveal
    // container code folded into it; a string literal unique to each lazy screen can.
    const screens = [
      { chunk: 'ContainerList', marker: 'Could not load containers' },
      { chunk: 'ContainerDetail', marker: 'This container no longer exists' },
    ];
    const read = (file: string) => readFileSync(join(dist, file), 'utf8');
    for (const { chunk, marker } of screens) {
      const lazyChunk = lazyFiles.find((f: string) => chunkName(f) === chunk);
      expect(lazyChunk, `${chunk} is a lazy chunk`).toBeDefined();
      expect(read(lazyChunk as string), `${chunk} carries its screen`).toContain(marker);
      for (const file of eagerFiles) expect(read(file), `${file} must not contain ${chunk} code`).not.toContain(marker);
    }
  });

  it.skipIf(!existsSync(manifestPath))('keeps the New stack and Import screens out of the eager entry', () => {
    const sizes = measure(dist);
    const lazyFiles = sizes.lazy.map((l: { file: string }) => l.file);
    const eagerFiles = readdirSync(join(dist, 'assets'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => `assets/${f}`)
      .filter((f) => !lazyFiles.includes(f));
    expect(eagerFiles.map(chunkName).some((name) => /StackCreate|StackImport|ServiceForm|FileBrowser|NetworkPicker|ComposePreview|portCheck|YamlEditor|^cm$/i.test(name))).toBe(false);
    expect(lazyFiles.some((f: string) => chunkName(f) === 'cm'), 'cm stays a lazy chunk').toBe(true);
    // Ratchet below ENTRY_BUDGET: connected servers moved the server scope into the eager Overview.
    expect(sizes.entry).toBeLessThanOrEqual(110 * 1024);

    // As above: eager code is one merged chunk, so unique strings are what reveal folded-in screens. ServiceForm,
    // FileBrowser and NetworkPicker are shared with StackDetail and may sit in a shared lazy chunk, so only their
    // absence from the eager files is asserted.
    const read = (file: string) => readFileSync(join(dist, file), 'utf8');
    const screens = [
      { chunk: 'StackCreate', marker: 'the stack was not created' },
      { chunk: 'StackImport', marker: 'the stack was not imported' },
    ];
    for (const { chunk, marker } of screens) {
      const lazyChunk = lazyFiles.find((f: string) => chunkName(f) === chunk);
      expect(lazyChunk, `${chunk} is a lazy chunk`).toBeDefined();
      expect(read(lazyChunk as string), `${chunk} carries its screen`).toContain(marker);
      for (const file of eagerFiles) expect(read(file), `${file} must not contain ${chunk} code`).not.toContain(marker);
    }
    const shared = [
      { name: 'ServiceForm', marker: 'Open raw YAML editor for ' },
      { name: 'FileBrowser', marker: 'This path is outside the allowed directories.' },
      { name: 'NetworkPicker', marker: 'Static IPv4 (optional)' },
    ];
    for (const { name, marker } of shared) {
      expect(lazyFiles.some((f: string) => read(f).includes(marker)), `${name} ships in a lazy chunk`).toBe(true);
      for (const file of eagerFiles) expect(read(file), `${file} must not contain ${name} code`).not.toContain(marker);
    }
  });

  it.skipIf(!existsSync(manifestPath))('keeps the storage screens and the cleanup dialog out of the eager entry', () => {
    const sizes = measure(dist);
    const lazyFiles = sizes.lazy.map((l: { file: string }) => l.file);
    const eagerFiles = readdirSync(join(dist, 'assets'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => `assets/${f}`)
      .filter((f) => !lazyFiles.includes(f));
    expect(eagerFiles.map(chunkName).some((name) => /Storage|CleanupDialog|UsageCard|usage/i.test(name))).toBe(false);
    // Ratchet below ENTRY_BUDGET: connected servers moved the server scope into the eager Overview.
    expect(sizes.entry).toBeLessThanOrEqual(110 * 1024);

    // As above: eager code is one merged chunk, so unique strings are what reveal folded-in code. CleanupDialog,
    // usage.ts and OperationPanel may sit in shared lazy chunks, so only their absence from the eager files is asserted.
    const read = (file: string) => readFileSync(join(dist, file), 'utf8');
    const screens = [
      { chunk: 'Storage', marker: 'Could not recalculate' },
      { chunk: 'StorageImage', marker: 'Back to images' },
      { chunk: 'StorageVolume', marker: 'Back to volumes' },
    ];
    for (const { chunk, marker } of screens) {
      const lazyChunk = lazyFiles.find((f: string) => chunkName(f) === chunk);
      expect(lazyChunk, `${chunk} is a lazy chunk`).toBeDefined();
      expect(read(lazyChunk as string), `${chunk} carries its screen`).toContain(marker);
      for (const file of eagerFiles) expect(read(file), `${file} must not contain ${chunk} code`).not.toContain(marker);
    }
    const shared = [
      { name: 'CleanupDialog', marker: 'Checking current state…' },
      { name: 'usage', marker: 'used by Docker' },
      { name: 'OperationPanel', marker: 'Operation no longer available' },
    ];
    for (const { name, marker } of shared) {
      expect(lazyFiles.some((f: string) => read(f).includes(marker)), `${name} ships in a lazy chunk`).toBe(true);
      for (const file of eagerFiles) expect(read(file), `${file} must not contain ${name} code`).not.toContain(marker);
    }
  });

  it.skipIf(!existsSync(manifestPath))('keeps the Dockerfile mode in the lazy cm chunk', () => {
    const sizes = measure(dist);
    const lazyFiles = sizes.lazy.map((l: { file: string }) => l.file);
    const eagerFiles = readdirSync(join(dist, 'assets'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => `assets/${f}`)
      .filter((f) => !lazyFiles.includes(f));
    // 'stopsignal' is an instruction in the legacy Dockerfile mode's keyword list; it only exists if the mode is bundled.
    const cmChunk = lazyFiles.find((f: string) => chunkName(f) === 'cm');
    expect(cmChunk, 'cm is a lazy chunk').toBeDefined();
    expect(readFileSync(join(dist, cmChunk as string), 'utf8'), 'cm carries the Dockerfile mode').toContain('stopsignal');
    for (const file of eagerFiles) expect(readFileSync(join(dist, file), 'utf8'), `${file} must not contain the Dockerfile mode`).not.toContain('stopsignal');
  });

  it.skipIf(!existsSync(manifestPath))('keeps the YAML library out of the eager entry', () => {
    const sizes = measure(dist);
    const lazyFiles = sizes.lazy.map((l: { file: string }) => l.file);
    const eagerFiles = readdirSync(join(dist, 'assets'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => `assets/${f}`)
      .filter((f) => !lazyFiles.includes(f));
    // One string from the yaml parser and one from stackYaml.ts. The eager code is one merged chunk, so they reveal folded-in code.
    const markers = ['Implicit map keys need to be followed by map values', 'A stack needs a services: section'];
    // The lazy New stack screen imports stackYaml.ts, so both strings must ship in some lazy chunk and in no eager one.
    for (const marker of markers) {
      expect(lazyFiles.some((f: string) => readFileSync(join(dist, f), 'utf8').includes(marker)), `a lazy chunk carries ${marker}`).toBe(true);
      for (const file of eagerFiles) expect(readFileSync(join(dist, file), 'utf8'), `${file} must not contain ${marker}`).not.toContain(marker);
    }
  });

  it.skipIf(!existsSync(manifestPath))('keeps the networks, settings and auth screens out of the eager entry', () => {
    const sizes = measure(dist);
    const lazyFiles = sizes.lazy.map((l: { file: string }) => l.file);
    const eagerFiles = readdirSync(join(dist, 'assets'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => `assets/${f}`)
      .filter((f) => !lazyFiles.includes(f));
    expect(eagerFiles.map(chunkName).some((name) => /NetworkList|NetworkCard|Settings|PasswordForm|UpdateCard|MetricsToken|Login|Setup|AuthCard|CreateNetworkDialog/i.test(name))).toBe(false);
    // Ratchet below ENTRY_BUDGET: connected servers moved the server scope into the eager Overview.
    expect(sizes.entry).toBeLessThanOrEqual(110 * 1024);

    // As above: eager code is one merged chunk, so unique strings are what reveal folded-in code. Login and Setup are
    // mounted by App from the session state but through lazy() routes, so they are lazy too. AuthCard, PasswordForm,
    // UpdateCard, the networks lib and CreateNetworkDialog may sit in shared lazy chunks, so only their absence from the
    // eager files is asserted.
    const read = (file: string) => readFileSync(join(dist, file), 'utf8');
    const screens = [
      { chunk: 'NetworkList', marker: 'Could not load networks' },
      { chunk: 'Settings', marker: 'Could not load the token status' },
      { chunk: 'Login', marker: 'Could not sign in. Check your connection and try again.' },
      { chunk: 'Setup', marker: 'Could not complete setup. Check your connection and try again.' },
    ];
    for (const { chunk, marker } of screens) {
      const lazyChunk = lazyFiles.find((f: string) => chunkName(f) === chunk);
      expect(lazyChunk, `${chunk} is a lazy chunk`).toBeDefined();
      expect(read(lazyChunk as string), `${chunk} carries its screen`).toContain(marker);
      for (const file of eagerFiles) expect(read(file), `${file} must not contain ${chunk} code`).not.toContain(marker);
    }
    const shared = [
      { name: 'AuthCard', marker: 'Localhost only. For remote access use a TLS reverse proxy.' },
      { name: 'PasswordForm', marker: 'Password changed. Other sessions were signed out.' },
      { name: 'UpdateCard', marker: 'Could not load the update status' },
      { name: 'networks lib', marker: 'no external route · internal' },
      { name: 'CreateNetworkDialog', marker: 'The Docker host itself cannot reach containers on this network.' },
    ];
    for (const { name, marker } of shared) {
      expect(lazyFiles.some((f: string) => read(f).includes(marker)), `${name} ships in a lazy chunk`).toBe(true);
      for (const file of eagerFiles) expect(read(file), `${file} must not contain ${name} code`).not.toContain(marker);
    }
  });
});
