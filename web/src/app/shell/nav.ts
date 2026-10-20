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

const stacks: NavEntry = {
  href: '/',
  label: 'Stacks',
  icon: 'stacks',
  match: (p) => p === '/' || /^\/stacks\/(?!new$|import$)[^/]+$/.test(p),
  group: 'main',
};
const containers: NavEntry = {
  href: '/containers',
  label: 'Containers',
  icon: 'box',
  match: (p) => p === '/containers' || p.startsWith('/containers/'),
  group: 'main',
};
const networks: NavEntry = { href: '/networks', label: 'Networks', icon: 'network', match: (p) => p === '/networks', group: 'main' };
const storage: NavEntry = {
  href: '/storage',
  label: 'Storage',
  icon: 'disk',
  match: (p) => p === '/storage' || p.startsWith('/storage/'),
  group: 'main',
};
const settings: NavEntry = { href: '/settings', label: 'Settings', icon: 'settings', match: (p) => p === '/settings', group: 'settings' };

export const NAV: NavEntry[] = [
  stacks,
  containers,
  networks,
  storage,
  { href: '/stacks/new', label: 'New stack', icon: 'plus', match: (p) => p === '/stacks/new', group: 'create' },
  { href: '/stacks/import', label: 'Import', icon: 'import', match: (p) => p === '/stacks/import', group: 'create' },
  settings,
];

export const TAB_BAR: NavEntry[] = [stacks, containers, storage, networks, settings];

/** Mobile top bar title: the last breadcrumb, mono when it is an entity name. */
export function pageTitle(path: string): { title: string; mono: boolean } {
  const crumbs = breadcrumb(path, splitServerPath(path).slug ?? undefined);
  const last = crumbs[crumbs.length - 1];
  return { title: last.label, mono: last.mono === true };
}
