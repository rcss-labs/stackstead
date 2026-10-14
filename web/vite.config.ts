import { mkdirSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';

const outDir = fileURLToPath(new URL('./dist', import.meta.url));

// emptyOutDir wipes dist/.gitignore, which keeps the directory in git while
// ignoring build output; put it back once the bundle is written.
function keepDistGitignore(): Plugin {
  return {
    name: 'keep-dist-gitignore',
    apply: 'build',
    closeBundle() {
      mkdirSync(outDir, { recursive: true });
      writeFileSync(`${outDir}/.gitignore`, '*\n!.gitignore\n');
    },
  };
}

export default defineConfig({
  plugins: [svelte(), keepDistGitignore()],
  resolve: {
    alias: { $lib: fileURLToPath(new URL('./src/lib', import.meta.url)) },
    // Svelte 5 resolves to its server build under node unless told otherwise;
    // tests mount components in jsdom and need the browser build.
    ...(process.env.VITEST ? { conditions: ['browser'] } : {}),
  },
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022', manifest: true },
  // Dev goes through this proxy so the SameSite=Strict session cookie stays
  // same-origin with the page. SSE passes through unchanged.
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8420',
      '/metrics': 'http://127.0.0.1:8420',
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['src/vitest-setup.ts'],
    include: ['src/**/*.test.ts'],
  },
});
