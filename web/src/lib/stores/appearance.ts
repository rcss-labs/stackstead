// Theme and density. web/public/theme.js sets data-theme / data-density on
// <html> before first paint; this store reads them at init and owns every
// change afterwards (DOM attribute + localStorage), so the rail toggle and
// Settings always agree.
import { derived, writable, type Readable } from 'svelte/store';

export type ThemeChoice = 'dark' | 'light' | 'system';
export type EffectiveTheme = 'dark' | 'light';
export type Density = 'compact' | 'comfortable';

const THEME_KEY = 'stackstead.theme';
const DENSITY_KEY = 'stackstead.density';

const themeStore = writable<ThemeChoice>('system');
const densityStore = writable<Density>('compact');
const systemDark = writable(false);

export const theme: Readable<ThemeChoice> = { subscribe: themeStore.subscribe };
export const density: Readable<Density> = { subscribe: densityStore.subscribe };
export const effectiveTheme: Readable<EffectiveTheme> = derived(
  [themeStore, systemDark],
  ([$theme, $systemDark]) => ($theme === 'system' ? ($systemDark ? 'dark' : 'light') : $theme)
);

export interface AppearanceDeps {
  root?: HTMLElement;
  storage?: () => Storage | null;
  matchMedia?: (q: string) => MediaQueryList;
}

let activeRoot: HTMLElement | null = null;
let activeStorage: () => Storage | null = defaultStorage;

function defaultStorage(): Storage | null {
  return typeof localStorage === 'undefined' ? null : localStorage;
}

// Storage can be missing or throw (private mode, blocked site data); the DOM
// and stores must still update, so failures are swallowed here.
function persist(key: string, value: string | null): void {
  try {
    const s = activeStorage();
    if (!s) return;
    if (value === null) s.removeItem(key);
    else s.setItem(key, value);
  } catch {
    /* ignore */
  }
}

export function setTheme(t: ThemeChoice): void {
  if (activeRoot) {
    if (t === 'system') delete activeRoot.dataset.theme;
    else activeRoot.dataset.theme = t;
  }
  persist(THEME_KEY, t === 'system' ? null : t);
  themeStore.set(t);
}

export function setDensity(d: Density): void {
  if (activeRoot) {
    if (d === 'compact') delete activeRoot.dataset.density;
    else activeRoot.dataset.density = d;
  }
  persist(DENSITY_KEY, d === 'compact' ? null : d);
  densityStore.set(d);
}

/** Reads what theme.js set; listens to the colour-scheme media query. Returns a disposer. */
export function initAppearance(deps: AppearanceDeps = {}): () => void {
  const root = deps.root ?? document.documentElement;
  activeRoot = root;
  activeStorage = deps.storage ?? defaultStorage;

  const attrTheme = root.dataset.theme;
  themeStore.set(attrTheme === 'dark' || attrTheme === 'light' ? attrTheme : 'system');
  densityStore.set(root.dataset.density === 'comfortable' ? 'comfortable' : 'compact');

  const mm = 'matchMedia' in deps ? deps.matchMedia : globalThis.matchMedia?.bind(globalThis);
  const mql = mm ? mm('(prefers-color-scheme: dark)') : null;
  systemDark.set(mql?.matches ?? false);
  const onChange = () => systemDark.set(mql?.matches ?? false);
  mql?.addEventListener('change', onChange);
  return () => mql?.removeEventListener('change', onChange);
}
