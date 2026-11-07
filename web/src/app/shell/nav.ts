import { splitServerPath } from '$lib/router';
import { breadcrumb } from '$lib/shell/breadcrumb';
import type { ICONS } from '$lib/shell/icons';

export interface NavEntry {
  href: string;
  label: string;
  icon: keyof typeof ICONS;
  match: (path: string) => boolean;
  /** `more` entries are not in the sidebar; the phone's More page and the palette list them. */
  group: 'main' | 'resources' | 'more' | 'create' | 'settings';
}

interface BaseEntry extends Omit<NavEntry, 'href' | 'match'> {
  /** Path inside a server, as this server's own URL space has it. */
  path: string;
  match: (inner: string) => boolean;
}

const under = (prefix: string) => (p: string) => p === prefix || p.startsWith(`${prefix}/`);

const BASE: BaseEntry[] = [
  { path: '/', label: 'Overview', icon: 'home', match: (p) => p === '/' || /^\/stacks\/(?!new$|import$)[^/]+$/.test(p), group: 'main' },
  { path: '/containers', label: 'Containers', icon: 'box', match: under('/containers'), group: 'main' },
  { path: '/activity', label: 'Activity', icon: 'activity', match: (p) => p === '/activity', group: 'main' },
  { path: '/images', label: 'Images', icon: 'image', match: under('/images'), group: 'resources' },
  { path: '/networks', label: 'Networks', icon: 'network', match: under('/networks'), group: 'resources' },
  { path: '/storage', label: 'Storage', icon: 'disk', match: under('/storage'), group: 'resources' },
  { path: '/backups', label: 'Backups', icon: 'copy', match: (p) => p === '/backups', group: 'resources' },
  { path: '/ports', label: 'Ports', icon: 'server', match: (p) => p === '/ports', group: 'more' },
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

/** Settings belongs to this server only; the bare path redirects to its first section. */
export const SETTINGS: NavEntry = { href: '/settings', label: 'Settings', icon: 'settings', match: under('/settings'), group: 'settings' };

export const NAV: NavEntry[] = [...serverNav(null), SETTINGS];

/** Where the switcher sends people to pair another server. */
export const CONNECT_SERVER_HREF = '/settings/servers';

export type TabKey = 'home' | 'containers' | 'search' | 'activity' | 'more';

export interface TabEntry {
  key: TabKey;
  label: string;
  icon: keyof typeof ICONS;
  /** Search is a button that opens the palette, not a page. */
  href?: string;
}

/** Mobile tab bar: exactly five. More is this app's own page, so it carries the viewed server in the query. */
export function tabBarNav(slug: string | null): TabEntry[] {
  const [overview, containers, activity] = serverNav(slug);
  return [
    { key: 'home', label: 'Home', icon: 'home', href: overview.href },
    { key: 'containers', label: 'Containers', icon: 'box', href: containers.href },
    { key: 'search', label: 'Search', icon: 'search' },
    { key: 'activity', label: 'Activity', icon: 'activity', href: activity.href },
    { key: 'more', label: 'More', icon: 'more', href: slug === null ? '/more' : `/more?server=${encodeURIComponent(slug)}` },
  ];
}

/** The tab a page belongs to; everything that is not Home, Containers or Activity is under More. */
export function activeTab(path: string): TabKey {
  const entries = serverNav(splitServerPath(path).slug);
  const on = (...labels: string[]) => entries.some((e) => labels.includes(e.label) && e.match(path));
  if (on('Activity')) return 'activity';
  if (on('Containers')) return 'containers';
  if (on('Overview', 'New stack', 'Import')) return 'home';
  return 'more';
}

/** Mobile top bar title: the last breadcrumb, mono when it is an entity name. */
export function pageTitle(path: string): { title: string; mono: boolean } {
  const crumbs = breadcrumb(path, splitServerPath(path).slug ?? undefined);
  const last = crumbs[crumbs.length - 1];
  return { title: last.label, mono: last.mono === true };
}

/** Where the mobile back button goes: the parent crumb, or More for a settings section. Undefined on a top-level page. */
export function pageBack(path: string): string | undefined {
  const { slug, inner } = splitServerPath(path);
  if (/^\/settings\/[^/]+$/.test(inner)) return '/more';
  const crumbs = breadcrumb(inner);
  const parent = crumbs.length > 1 ? crumbs[crumbs.length - 2].href : undefined;
  if (parent === undefined) return undefined;
  if (slug === null) return parent;
  const prefix = `/s/${encodeURIComponent(slug)}`;
  return parent === '/' ? prefix : prefix + parent;
}
