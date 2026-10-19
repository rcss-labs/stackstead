import { readable, type Readable } from '$lib/store';

/** Whether a CSS media query currently matches; false where matchMedia does not exist (tests, old engines). */
export function mediaQuery(query: string): Readable<boolean> {
  return readable(false, (set) => {
    if (typeof globalThis.matchMedia !== 'function') return;
    const mql = globalThis.matchMedia(query);
    set(mql.matches);
    const onChange = (e: MediaQueryListEvent) => set(e.matches);
    mql.addEventListener('change', onChange);
    return () => mql.removeEventListener('change', onChange);
  });
}
