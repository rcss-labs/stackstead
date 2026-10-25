import { splitServerPath } from '$lib/router';
import { breadcrumb } from '$lib/shell/breadcrumb';
import type { ICONS } from '$lib/shell/icons';

export interface NavEntry {
  href: string;
  label: string;
  icon: keyof typeof ICONS;
  match: (path: string) => boolean;
  group: 'main' | 'create' | 'settings';
}

interface BaseEntry extends Omit<NavEntry, 'href' | 'match'> {
  /** Path inside a server, as this server's own URL space has it. */
  path: string;
  match: (inner: string) => boolean;
}

const BASE: BaseEntry[] = [
  { path: '/', label: 'Stacks', icon: 'stacks', match: (p) => p === '/' || /^\/stacks\/(?!new$|import$)[^/]+$/.test(p), group: 'main' },
  { path: '/containers', label: 'Containers', icon: 'box', match: (p) => p === '/containers' || p.startsWith('/containers/'), group: 'main' },
  { path: '/networks', label: 'Networks', icon: 'network', match: (p) => p === '/networks' || p.startsWith('/networks/'), group: 'main' },
  { path: '/storage', label: 'Storage', icon: 'disk', match: (p) => p === '/storage' || p.startsWith('/storage/'), group: 'main' },
  { path: '/images', label: 'Images', icon: 'image', match: (p) => p === '/images' || p.startsWith('/images/'), group: 'main' },
  { path: '/stacks/new', label: 'New stack', icon: 'plus', match: (p) => p === '/stacks/new', group: 'create' },
  { path: '/stacks/import', label: 'Import', icon: 'import', match: (p) => p === '/stacks/import', group: 'create' },
];

/** The per-server entries for this server (null) or a connected one, linking and matching inside its URL space. */
export function serverNav(slug: string | null): NavEntry[] {
  const prefix = slug === null ? '' : `/s/${encodeURIComponent(slug)}`;
  return BASE.map(({ path, match, ...rest }) => ({
    ...rest,
    href: path === '/' ? prefix || '/' : prefix + path,
    match: (full) => {
      const here = splitServerPath(full);
      return here.slug === slug && match(here.inner);
    },
  }));
}

/** Settings belongs to this server only. */
export const SETTINGS: NavEntry = { href: '/settings', label: 'Settings', icon: 'settings', match: (p) => p === '/settings', group: 'settings' };

export const NAV: NavEntry[] = [...serverNav(null), SETTINGS];

/** Mobile tab bar: the viewed server's four screens (Storage before Networks), then this server's Settings. */
export function tabBarNav(slug: string | null): NavEntry[] {
  const [stacks, containers, networks, storage] = serverNav(slug);
  return [stacks, containers, storage, networks, SETTINGS];
}

/** Mobile top bar title: the last breadcrumb, mono when it is an entity name. */
export function pageTitle(path: string): { title: string; mono: boolean } {
  const crumbs = breadcrumb(path, splitServerPath(path).slug ?? undefined);
  const last = crumbs[crumbs.length - 1];
  return { title: last.label, mono: last.mono === true };
}
