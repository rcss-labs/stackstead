import { lazy } from 'react';
import type { ComponentType, LazyExoticComponent } from 'react';
import { matchRoute, splitServerPath } from '$lib/router';
import Overview from './routes/Overview';

export interface AppRoute {
  path: string;
  /** Static page name for `<title> · Stackstead`; entity routes set their own title. */
  title?: string;
  screen: LazyExoticComponent<ComponentType> | ComponentType;
}

// Both settings paths render one screen: the bare path redirects to the first section.
const Settings = lazy(() => import('./routes/Settings'));

// Shared by both network paths so moving between them keeps the screen mounted.
const NetworkList = lazy(() => import('./routes/NetworkList'));

// Only the overview is in the entry chunk; every other screen loads on first visit.
export const ROUTES: AppRoute[] = [
  { path: '/', title: 'Overview', screen: Overview },
  { path: '/stacks/new', title: 'New stack', screen: lazy(() => import('./routes/StackCreate')) },
  { path: '/stacks/import', title: 'Import stack', screen: lazy(() => import('./routes/StackImport')) },
  { path: '/stacks/:name', screen: lazy(() => import('./routes/StackDetail')) },
  { path: '/containers', title: 'Containers', screen: lazy(() => import('./routes/ContainerList')) },
  { path: '/containers/:name', screen: lazy(() => import('./routes/ContainerDetail')) },
  { path: '/networks', title: 'Networks', screen: NetworkList },
  { path: '/networks/:name', title: 'Networks', screen: NetworkList },
  { path: '/storage', title: 'Storage', screen: lazy(() => import('./routes/Storage')) },
  { path: '/storage/images/:id', title: 'Storage', screen: lazy(() => import('./routes/StorageImage')) },
  { path: '/storage/volumes/:name', title: 'Storage', screen: lazy(() => import('./routes/StorageVolume')) },
  { path: '/images', title: 'Images', screen: lazy(() => import('./routes/Images')) },
  { path: '/images/:slug', screen: lazy(() => import('./routes/ImageDetail')) },
  { path: '/backups', title: 'Backups', screen: lazy(() => import('./routes/Backups')) },
  { path: '/activity', title: 'Activity', screen: lazy(() => import('./routes/Activity')) },
  // Not nested under /networks: a network may be named "ports".
  { path: '/ports', title: 'Ports', screen: lazy(() => import('./routes/Ports')) },
  { path: '/more', title: 'More', screen: lazy(() => import('./routes/More')) },
  { path: '/settings', title: 'Settings', screen: Settings },
  { path: '/settings/:section', title: 'Settings', screen: Settings },
  { path: '/login', screen: lazy(() => import('./routes/Login')) },
  { path: '/setup', screen: lazy(() => import('./routes/Setup')) },
  { path: '/all', title: 'All stacks', screen: lazy(() => import('./routes/AllStacks')) },
  // Lazy: RemoteServer reads SERVER_ROUTES from this module.
  { path: '/s/:server/*', screen: lazy(() => import('./servers/RemoteServer')) },
  { path: '*', title: 'Page not found', screen: lazy(() => import('./routes/NotFound')) },
];

const NOT_ON_A_SERVER = new Set(['/more', '/settings', '/settings/:section', '/login', '/setup', '/all', '/s/:server/*', '*']);

/** The screens that also open on a connected server, with paths relative to `/s/{slug}` ('' = index). */
export const SERVER_ROUTES: AppRoute[] = ROUTES.filter((r) => !NOT_ON_A_SERVER.has(r.path)).map((r) => ({
  ...r,
  path: r.path === '/' ? '' : r.path.slice(1),
}));

const NOT_FOUND = ROUTES.find((r) => r.path === '*')!;

/** The route a path renders; ROUTES lists static paths before the `:param` patterns that overlap them. */
export function findRoute(path: string): AppRoute | undefined {
  const { slug, inner } = splitServerPath(path);
  if (slug === null) return ROUTES.find((r) => r.path === '*' || matchRoute(r.path, path) !== null);
  const known = ROUTES.find((r) => !NOT_ON_A_SERVER.has(r.path) && matchRoute(r.path, inner) !== null);
  return known ?? NOT_FOUND;
}
