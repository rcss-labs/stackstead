import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const read = (name: string) => readFileSync(resolve(process.cwd(), name), 'utf8');
const html = read('index.html');

function headChildren(page: string): string[] {
  const head = /<head>([\s\S]*?)<\/head>/.exec(page)?.[1] ?? '';
  return head.match(/<(script|link|meta|title)\b[^>]*>/g) ?? [];
}

function fontPreloads(page: string): string[] {
  return [...page.matchAll(/<link rel="preload" as="font"[^>]*href="([^"]+)"/g)].map((m) => m[1]!);
}

describe('index.html', () => {
  it('loads /theme.js first in the head, before stylesheets and modules', () => {
    expect(headChildren(html)[0]).toContain('src="/theme.js"');
  });

  it('does not reference Google Fonts', () => {
    expect(html).not.toContain('googleapis');
  });

  it('preloads exactly the three self-hosted fonts', () => {
    expect(fontPreloads(html)).toEqual([
      '/src/assets/fonts/IBMPlexSans-Regular-latin.woff2',
      '/src/assets/fonts/IBMPlexSans-Medium-latin.woff2',
      '/src/assets/fonts/GeistMono-Regular-latin.woff2',
    ]);
  });

  it('mounts #app from the React entry', () => {
    expect(html).toContain('<div id="app"></div>');
    expect(html).toContain('<script type="module" src="/src/app/main.tsx"></script>');
    expect(html).not.toContain('src="/src/main.ts"');
  });

  it('the second HTML entry and its config are gone', () => {
    expect(existsSync(resolve(process.cwd(), 'app.html'))).toBe(false);
    expect(existsSync(resolve(process.cwd(), 'vite.app.config.ts'))).toBe(false);
  });
});
