import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import {
  density,
  effectiveTheme,
  initAppearance,
  setDensity,
  setTheme,
  theme
} from './appearance';

function fakeMedia(matches: boolean) {
  const listeners = new Set<() => void>();
  const mql = {
    matches,
    addEventListener: vi.fn((_: string, l: () => void) => listeners.add(l)),
    removeEventListener: vi.fn((_: string, l: () => void) => listeners.delete(l))
  };
  return {
    mql: mql as unknown as MediaQueryList,
    listeners,
    fire(next: boolean) {
      mql.matches = next;
      [...listeners].forEach((l) => l());
    }
  };
}

describe('appearance store', () => {
  let root: HTMLElement;
  let store: Storage;
  let media: ReturnType<typeof fakeMedia>;
  let dispose: () => void;

  const init = (storage: () => Storage | null = () => store) => {
    dispose = initAppearance({ root, storage, matchMedia: () => media.mql });
  };

  beforeEach(() => {
    root = document.createElement('html');
    localStorage.clear();
    store = localStorage;
    media = fakeMedia(false);
  });
  afterEach(() => dispose?.());

  it('reads initial attributes set by theme.js', () => {
    root.dataset.theme = 'light';
    root.dataset.density = 'comfortable';
    init();
    expect(get(theme)).toBe('light');
    expect(get(density)).toBe('comfortable');
    expect(get(effectiveTheme)).toBe('light');
  });

  it('defaults to system and compact', () => {
    media = fakeMedia(true);
    init();
    expect(get(theme)).toBe('system');
    expect(get(density)).toBe('compact');
    expect(get(effectiveTheme)).toBe('dark');
  });

  it("setTheme('light') writes attribute and storage", () => {
    init();
    setTheme('light');
    expect(root.dataset.theme).toBe('light');
    expect(store.getItem('stackstead.theme')).toBe('light');
    expect(get(theme)).toBe('light');
    expect(get(effectiveTheme)).toBe('light');
  });

  it('system removes attribute and key', () => {
    init();
    setTheme('dark');
    setTheme('system');
    expect(root.dataset.theme).toBeUndefined();
    expect(store.getItem('stackstead.theme')).toBeNull();
    expect(get(theme)).toBe('system');
  });

  it('setDensity round trip', () => {
    init();
    setDensity('comfortable');
    expect(root.dataset.density).toBe('comfortable');
    expect(store.getItem('stackstead.density')).toBe('comfortable');
    setDensity('compact');
    expect(root.dataset.density).toBeUndefined();
    expect(store.getItem('stackstead.density')).toBeNull();
    expect(get(density)).toBe('compact');
  });

  it('setTheme with throwing storage still updates the DOM', () => {
    const throwing = {
      getItem: () => { throw new Error('denied'); },
      setItem: () => { throw new Error('denied'); },
      removeItem: () => { throw new Error('denied'); }
    } as unknown as Storage;
    init(() => throwing);
    expect(() => setTheme('dark')).not.toThrow();
    expect(() => setDensity('comfortable')).not.toThrow();
    expect(root.dataset.theme).toBe('dark');
    expect(root.dataset.density).toBe('comfortable');
    expect(get(theme)).toBe('dark');
    expect(() => setTheme('system')).not.toThrow();
  });

  it('survives a storage accessor that throws', () => {
    init(() => { throw new Error('blocked'); });
    expect(() => setTheme('light')).not.toThrow();
    expect(root.dataset.theme).toBe('light');
  });

  it('query override is visible but never written', () => {
    root.dataset.theme = 'dark';
    init();
    expect(get(theme)).toBe('dark');
    expect(store.getItem('stackstead.theme')).toBeNull();
    expect(store.length).toBe(0);
  });

  it('media change flips effectiveTheme only in system', () => {
    init();
    expect(get(effectiveTheme)).toBe('light');
    media.fire(true);
    expect(get(effectiveTheme)).toBe('dark');
    setTheme('light');
    media.fire(false);
    media.fire(true);
    expect(get(effectiveTheme)).toBe('light');
  });

  it('falls back to light when matchMedia is missing', () => {
    dispose = initAppearance({ root, storage: () => store, matchMedia: undefined });
    expect(get(effectiveTheme)).toBe('light');
  });

  it('disposer removes the media listener', () => {
    init();
    expect(media.listeners.size).toBe(1);
    dispose();
    expect(media.listeners.size).toBe(0);
  });
});
