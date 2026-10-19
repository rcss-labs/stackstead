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
    rollupOptions: { input: 'app.html' },
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8420',
      '/metrics': 'http://127.0.0.1:8420',
    },
  },
});
