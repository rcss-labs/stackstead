import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const appDir = join(process.cwd(), 'src', 'app');
const stylesDir = join(appDir, 'styles');
const css = readFileSync(join(stylesDir, 'tokens.css'), 'utf8');

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

// Body of the first rule whose selector line equals `selector`, brace-matched.
function block(selector: string): string {
  const start = css.indexOf(`${selector} {`);
  if (start < 0) throw new Error(`no block for ${selector}`);
  const open = css.indexOf('{', start);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++;
    if (css[i] === '}' && --depth === 0) return css.slice(open + 1, i);
  }
  throw new Error(`unterminated block ${selector}`);
}

const parseTokens = (body: string): Record<string, string> =>
  Object.fromEntries([...body.matchAll(/(--[\w-]+):\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));

const light = parseTokens(block(':root'));
const dark = parseTokens(block(":root[data-theme='dark']"));

type RGB = [number, number, number];
function parseColor(v: string): { rgb: RGB; a: number } {
  const hex = /^#([0-9a-f]{6})$/i.exec(v);
  if (hex) {
    const n = parseInt(hex[1], 16);
    return { rgb: [(n >> 16) & 255, (n >> 8) & 255, n & 255], a: 1 };
  }
  const m = /^rgba\((\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\)$/.exec(v);
  if (!m) throw new Error(`unparseable colour ${v}`);
  return { rgb: [+m[1], +m[2], +m[3]], a: +m[4] };
}
const over = (fg: RGB, a: number, bg: RGB): RGB => fg.map((c, i) => c * a + bg[i] * (1 - a)) as RGB;
const L = ([r, g, b]: RGB) => {
  const f = (c: number) => ((c /= 255) <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
};
const ratio = (a: RGB, b: RGB) => (Math.max(L(a), L(b)) + 0.05) / (Math.min(L(a), L(b)) + 0.05);

describe('design tokens', () => {
  it('old token names are gone', () => {
    const old = /--(surface-raised|accent-contrast|font-sans|font-mono)\b/;
    const hits = walk(appDir)
      .filter((f) => /\.(tsx?|css)$/.test(f) && !f.endsWith('tokens.test.ts'))
      .filter((f) => old.test(readFileSync(f, 'utf8')));
    expect(hits).toEqual([]);
  });

  it('light and dark blocks define the same token names', () => {
    const colourNames = (t: Record<string, string>) => Object.keys(t).filter((k) => /^(--(bg|chassis|surface|raised|hover|border|border-strong|border-input|text|muted|faint|accent|accent-ink|accent-soft|success|warning|danger|info|shadow|shadow-pop|hi|overlay)|--(success|warning|danger|info|muted)-soft|--series-[1-8]|--kind-(images|volumes|layers|cache))$/.test(k)).sort();
    expect(colourNames(dark)).toEqual(colourNames(light));
    expect(colourNames(light)).toHaveLength(39);
    expect(Object.keys(light)).toEqual(expect.arrayContaining(['--sans', '--mono', '--pad', '--row', '--radius-sm', '--radius', '--radius-md', '--radius-lg', '--radius-pill', '--space-1', '--space-7', '--radius-xs', '--radius-led', '--overlay', '--sidebar-w', '--topbar-h', '--mobile-bar-h']));
  });

  it('media-query dark block matches the explicit dark block', () => {
    const media = css.slice(css.indexOf('@media (prefers-color-scheme: dark)'));
    const body = block(":root[data-theme='dark']");
    const mediaTokens = parseTokens(media.slice(media.indexOf('{', media.indexOf(':root:not')) + 1, media.indexOf('}', media.indexOf(':root:not'))));
    expect(mediaTokens).toEqual(parseTokens(body));
  });

  describe.each([
    ['light', light],
    ['dark', dark],
  ] as const)('contrast pairs meet AA (%s)', (_name, t) => {
    const c = (k: string) => parseColor(t[k]).rgb;
    const surfaces = ['--bg', '--surface', '--raised', '--hover'];
    const fgs = ['--text', '--muted', '--faint', '--accent', '--success', '--warning', '--danger', '--info'];
    for (const fg of fgs)
      for (const bg of surfaces)
        it(`${fg} on ${bg} >= 4.5`, () => expect(ratio(c(fg), c(bg))).toBeGreaterThanOrEqual(4.5));

    for (const fg of ['--text', '--muted', '--faint'])
      it(`${fg} on --chassis >= 4.5`, () => expect(ratio(c(fg), c('--chassis'))).toBeGreaterThanOrEqual(4.5));
    it('border-input vs chassis >= 3', () => expect(ratio(c('--border-input'), c('--chassis'))).toBeGreaterThanOrEqual(3));
    it('accent-ink on accent >= 4.5', () => expect(ratio(c('--accent-ink'), c('--accent'))).toBeGreaterThanOrEqual(4.5));
    it('border-input vs bg >= 3', () => expect(ratio(c('--border-input'), c('--bg'))).toBeGreaterThanOrEqual(3));
    for (const tone of ['success', 'warning', 'danger', 'info', 'muted', 'accent'])
      it(`${tone} on its soft fill over surface >= 4.5`, () => {
        const soft = parseColor(t[`--${tone}-soft`]);
        expect(ratio(c(`--${tone}`), over(soft.rgb, soft.a, c('--surface')))).toBeGreaterThanOrEqual(4.5);
      });
  });

  it('no literal colours outside tokens.css', () => {
    const literal = /#[0-9a-f]{3,8}\b|\brgba?\(|\bhsla?\(/i;
    const hits = walk(appDir)
      .filter((f) => f.endsWith('.css') && !f.endsWith('tokens.css'))
      .filter((f) => literal.test(readFileSync(f, 'utf8')));
    expect(hits).toEqual([]);
  });

  it('fonts.css declares exactly the five faces and no remote url', () => {
    const fonts = readFileSync(join(stylesDir, 'fonts.css'), 'utf8');
    expect(fonts.match(/@font-face/g)).toHaveLength(5);
    const urls = [...fonts.matchAll(/url\(([^)]*)\)/g)].map((m) => m[1].replace(/['"]/g, ''));
    expect(urls).toHaveLength(5);
    for (const u of urls) {
      expect(u).not.toContain('http');
      expect(u.startsWith('../../assets/fonts/')).toBe(true);
    }
  });
});
