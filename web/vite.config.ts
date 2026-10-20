import { mkdirSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import react from '@vitejs/plugin-react';
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
  plugins: [react(), keepDistGitignore()],
  resolve: {
    alias: { $lib: fileURLToPath(new URL('./src/lib', import.meta.url)) },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    manifest: true,
    rollupOptions: {
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        // Rolldown stops merging entry-reachable common chunks into the entry once two lazy routes share modules
        // (+2 kB gz of glue); naming every statically reachable module keeps all eager code in one chunk.
        codeSplitting: {
          groups: [
            {
              debugName: 'eager-shell',
              name: (id, ctx) => {
                const seen = new Set<string>();
                const eager = (m: string): boolean => {
                  if (seen.has(m)) return false;
                  seen.add(m);
                  const info = ctx.getModuleInfo(m);
                  return !!info && (info.isEntry || info.importers.some(eager));
                };
                return eager(id) ? 'shell' : null;
              },
            },
          ],
        },
      },
    },
  },
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
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
  },
});
