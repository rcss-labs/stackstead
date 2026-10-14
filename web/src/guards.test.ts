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
    return /\.(svelte|ts)$/.test(e.name) && p !== thisFile ? [p] : [];
  });
}

function findHtmlTag(files: Record<string, string>): string[] {
  return Object.entries(files)
    .filter(([, text]) => text.includes('{@html'))
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
  it('flags raw HTML interpolation', () => {
    expect(findHtmlTag({ 'a.svelte': '<p>{@html x}</p>', 'b.svelte': '<p>{x}</p>' })).toEqual(['a.svelte']);
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
  it('has no {@html anywhere in src/', () => {
    const files = Object.fromEntries(sourceFiles(srcDir).map((p) => [p, readFileSync(p, 'utf8')]));
    expect(findHtmlTag(files)).toEqual([]);
  });

  it('pins every dependency to an exact version', () => {
    const pkg = JSON.parse(readFileSync(pkgPath, 'utf8')) as Record<string, unknown>;
    expect(findRangedVersions(pkg)).toEqual([]);
  });
});
