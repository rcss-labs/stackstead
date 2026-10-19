import { get } from '$lib/store';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { mediaQuery } from './media';

afterEach(() => vi.unstubAllGlobals());

describe('mediaQuery', () => {
  it('follows matchMedia changes', () => {
    const listeners = new Set<(e: MediaQueryListEvent) => void>();
    const mql = {
      matches: true,
      addEventListener: (_: string, fn: (e: MediaQueryListEvent) => void) => listeners.add(fn),
      removeEventListener: (_: string, fn: (e: MediaQueryListEvent) => void) => listeners.delete(fn),
    };
    const matchMedia = vi.fn(() => mql);
    vi.stubGlobal('matchMedia', matchMedia);
    const seen: boolean[] = [];
    const stop = mediaQuery('(min-width: 1100px)').subscribe((v) => seen.push(v));
    expect(matchMedia).toHaveBeenCalledWith('(min-width: 1100px)');
    expect(seen.at(-1)).toBe(true);
    listeners.forEach((fn) => fn({ matches: false } as MediaQueryListEvent));
    expect(seen.at(-1)).toBe(false);
    stop();
    expect(listeners.size).toBe(0);
  });

  it('is false without matchMedia', () => {
    vi.stubGlobal('matchMedia', undefined);
    expect(get(mediaQuery('(min-width: 1100px)'))).toBe(false);
  });
});
