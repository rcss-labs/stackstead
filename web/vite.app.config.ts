import { fileURLToPath } from 'node:url';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';
import { isAppNavigation } from './src/app/devFallback';

// Dev only: client-side routes (/stacks/web, /settings) must answer with app.html.
function appHtmlFallback(): Plugin {
  return {
    name: 'app-html-fallback',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use((req, _res, next) => {
        const isNavigation = isAppNavigation(req.method ?? '', req.headers.accept, req.url ?? '');
        if (isNavigation) req.url = '/app.html';
        next();
      });
    },
  };
}

export default defineConfig({
  plugins: [react(), appHtmlFallback()],
  resolve: {
    alias: { $lib: fileURLToPath(new URL('./src/lib', import.meta.url)) },
  },
  build: {
    outDir: 'dist-app',
    emptyOutDir: true,
    target: 'es2022',
    manifest: true,
    rollupOptions: {
      input: 'app.html',
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        // Rolldown stops merging entry-reachable common chunks into the entry once two lazy routes share modules
        // (+2 kB gz of glue); naming every statically reachable module keeps all eager code in one chunk.
        codeSplitting: {
          groups: [
            {
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
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8420',
      '/metrics': 'http://127.0.0.1:8420',
    },
  },
});
