// Builds .ds-pkg/: a throwaway, library-shaped view of src/app/components for
// the design-sync converter (the web app has no library build of its own).
// Run from web/: node .design-sync/build-pkg.mjs
import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const PKG = '.ds-pkg';
const COMPONENTS = 'src/app/components';
const STYLES = 'src/app/styles';

rmSync(PKG, { recursive: true, force: true });
mkdirSync(PKG, { recursive: true });

const modules = readdirSync(COMPONENTS)
  .filter((f) => f.endsWith('.tsx') && !/\.(test|spec|stories)\./.test(f))
  .map((f) => f.replace(/\.tsx$/, ''))
  .sort();

writeFileSync(join(PKG, 'index.ts'), modules.map((m) => `export * from '../${COMPONENTS}/${m}';`).join('\n') + '\n');

writeFileSync(join(PKG, 'package.json'), JSON.stringify({
  name: 'stackstead-ui',
  version: '0.0.0',
  private: true,
  module: 'index.ts',
  types: 'types/index.d.ts',
}, null, 2) + '\n');

writeFileSync(join(PKG, 'tsconfig.json'), JSON.stringify({
  extends: '../tsconfig.json',
  compilerOptions: {
    noEmit: false,
    declaration: true,
    emitDeclarationOnly: true,
    rootDir: '../src',
    outDir: 'types',
  },
  include: [`../${COMPONENTS}/*.tsx`, '../src/vite-env.d.ts'],
  exclude: [`../${COMPONENTS}/*.test.tsx`],
}, null, 2) + '\n');

execFileSync('npx', ['tsc', '-p', join(PKG, 'tsconfig.json')], { stdio: 'inherit' });

writeFileSync(join(PKG, 'types', 'index.d.ts'), modules.map((m) => `export * from './app/components/${m}';`).join('\n') + '\n');

// cssEntry must live under the package dir, and tokensGlob only reads from a tokens
// npm package, so tokens + base ship together as the cssEntry. fonts.css stays in
// place (extraFonts may reach into the repo) so its relative font urls keep resolving.
writeFileSync(join(PKG, 'global.css'), ['tokens.css', 'base.css'].map((f) => readFileSync(join(STYLES, f), 'utf8')).join('\n'));

console.error(`.ds-pkg: ${modules.length} component modules`);
