import { readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// Vitest runs from web/ (npm test); jsdom's import.meta.url is not a file URL.
const srcDir = resolve(process.cwd(), 'src');
const pkgPath = resolve(process.cwd(), 'package.json');
const thisFile = join(srcDir, 'guards.test.ts');

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name);
    if (e.isDirectory()) return sourceFiles(p);
    return /\.(ts|tsx)$/.test(e.name) && p !== thisFile ? [p] : [];
  });
}

function findRawHtml(files: Record<string, string>): string[] {
  return Object.entries(files)
    .filter(([, text]) => /\bdangerouslySetInnerHTML\b/.test(text))
    .map(([name]) => name);
}

function findHandoffReferences(files: Record<string, string>): string[] {
  return Object.entries(files)
    .filter(([, text]) => text.includes('design_handoff_stackstead_react'))
    .map(([name]) => name);
}

function findSvelteStoreImports(files: Record<string, string>): string[] {
  return Object.entries(files)
    .filter(([, text]) => /(?:\bfrom\s*|\bimport\s*\(\s*)['"]svelte(?:\/[^'"]*)?['"]/.test(text))
    .map(([name]) => name);
}

function findRangedVersions(pkg: Record<string, unknown>): string[] {
  const out: string[] = [];
  for (const section of ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies']) {
    const deps = (pkg[section] ?? {}) as Record<string, string>;
    for (const [name, version] of Object.entries(deps)) {
      if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) out.push(`${section}.${name}@${version}`);
    }
  }
  return out;
}

describe('guards: detectors', () => {
  it('flags dangerouslySetInnerHTML in tsx and ts but not plain markup', () => {
    expect(
      findRawHtml({
        'a.tsx': 'const x = <div dangerouslySetInnerHTML={{ __html: y }} />;',
        'b.tsx': 'const x = <div>{y}</div>;',
        'c.ts': 'const p = { dangerouslySetInnerHTML: 1 };',
      }),
    ).toEqual(['a.tsx', 'c.ts']);
  });
  it('flags references to the design handoff folder', () => {
    expect(
      findHandoffReferences({
        'a.tsx': "import x from '../../docs/superpowers/design_handoff_stackstead_react/x';",
        'b.tsx': 'const x = 1;',
      }),
    ).toEqual(['a.tsx']);
  });
  it('flags svelte imports (any subpath) but not $lib/store', () => {
    expect(
      findSvelteStoreImports({
        'a.ts': "import { writable } from 'svelte/store';",
        'b.ts': "import { writable } from '$lib/store';",
        'c.tsx': 'import { get } from "svelte/store";',
        'd.ts': "const { writable } = await import('svelte/store');",
        'e.ts': "import { mount } from 'svelte';",
        'f.ts': "import x from 'svelte/internal';",
        'g.ts': "import x from 'svelte-check';",
      }),
    ).toEqual(['a.ts', 'c.tsx', 'd.ts', 'e.ts', 'f.ts']);
  });
  it('flags anything that is not an exact version', () => {
    const bad = findRangedVersions({
      dependencies: { a: '^1.0.0', b: '~2.0.0', c: '3.0.0', d: 'latest' },
      devDependencies: { e: '>=1', f: '1.2.3', g: '1.x', h: '1.0.0-rc.1' },
    });
    expect(bad).toEqual([
      'dependencies.a@^1.0.0',
      'dependencies.b@~2.0.0',
      'dependencies.d@latest',
      'devDependencies.e@>=1',
      'devDependencies.g@1.x',
    ]);
  });
});

describe('guards: repository', () => {
  it('has no dangerouslySetInnerHTML in src/', () => {
    const files = Object.fromEntries(sourceFiles(srcDir).map((p) => [p, readFileSync(p, 'utf8')]));
    expect(findRawHtml(files)).toEqual([]);
  });

  it('never references the design handoff folder', () => {
    const files = Object.fromEntries(sourceFiles(srcDir).map((p) => [p, readFileSync(p, 'utf8')]));
    expect(findHandoffReferences(files)).toEqual([]);
  });

  it('imports nothing from svelte anywhere in src/', () => {
    const files = Object.fromEntries(sourceFiles(srcDir).map((p) => [p, readFileSync(p, 'utf8')]));
    expect(findSvelteStoreImports(files)).toEqual([]);
  });

  it('pins every dependency to an exact version', () => {
    const pkg = JSON.parse(readFileSync(pkgPath, 'utf8')) as Record<string, unknown>;
    expect(findRangedVersions(pkg)).toEqual([]);
  });
});
