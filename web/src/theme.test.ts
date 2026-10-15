import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const source = readFileSync(join(process.cwd(), 'public', 'theme.js'), 'utf8');
const runThemeScript = () => new Function(source)();

describe('theme.js', () => {
  beforeEach(() => {
    localStorage.clear();
    delete document.documentElement.dataset.theme;
    delete document.documentElement.dataset.density;
    history.replaceState(null, '', '/');
  });
  afterEach(() => vi.restoreAllMocks());

  it('applies stored dark theme and comfortable density', () => {
    localStorage.setItem('stackstead.theme', 'dark');
    localStorage.setItem('stackstead.density', 'comfortable');
    runThemeScript();
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(document.documentElement.dataset.density).toBe('comfortable');
  });

  it('absent keys leave no attributes (system, compact)', () => {
    runThemeScript();
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
    expect(document.documentElement.hasAttribute('data-density')).toBe(false);
  });

  it('ignores junk values', () => {
    localStorage.setItem('stackstead.theme', 'blue');
    localStorage.setItem('stackstead.density', 'huge');
    runThemeScript();
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
    expect(document.documentElement.hasAttribute('data-density')).toBe(false);
  });

  it('theme.js query override wins but is not stored', () => {
    localStorage.setItem('stackstead.theme', 'dark');
    history.replaceState(null, '', '/?theme=light');
    runThemeScript();
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(localStorage.getItem('stackstead.theme')).toBe('dark');
  });

  it('theme.js survives a throwing localStorage', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied');
    });
    history.replaceState(null, '', '/?theme=dark');
    expect(runThemeScript).not.toThrow();
    expect(document.documentElement.dataset.theme).toBe('dark');
  });
});
