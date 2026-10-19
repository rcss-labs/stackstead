import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const read = (name: string) => readFileSync(resolve(process.cwd(), name), 'utf8');
const pages = { 'index.html': read('index.html'), 'app.html': read('app.html') };

function headChildren(html: string): string[] {
  const head = /<head>([\s\S]*?)<\/head>/.exec(html)?.[1] ?? '';
  return head.match(/<(script|link|meta|title)\b[^>]*>/g) ?? [];
}

function fontPreloads(html: string): string[] {
  return [...html.matchAll(/<link rel="preload" as="font"[^>]*href="([^"]+)"/g)].map((m) => m[1]!);
}

describe.each(Object.entries(pages))('%s', (_name, html) => {
  it('loads /theme.js first in the head, before stylesheets and modules', () => {
    expect(headChildren(html)[0]).toContain('src="/theme.js"');
  });

  it('does not reference Google Fonts', () => {
    expect(html).not.toContain('googleapis');
  });
});

describe('app.html', () => {
  it('preloads the same three fonts as index.html', () => {
    const fonts = fontPreloads(pages['app.html']);
    expect(fonts).toHaveLength(3);
    expect(fonts).toEqual(fontPreloads(pages['index.html']));
  });

  it('mounts #app from the React entry', () => {
    expect(pages['app.html']).toContain('<div id="app"></div>');
    expect(pages['app.html']).toContain('<script type="module" src="/src/app/main.tsx"></script>');
  });
});
