import { lazy } from 'react';
import type { ComponentType, LazyExoticComponent } from 'react';
import { matchRoute } from '$lib/router';
import Overview from './routes/Overview';

export interface AppRoute {
  path: string;
  /** Static page name for `<title> · Stackstead`; entity routes set their own title. */
  title?: string;
  screen: LazyExoticComponent<ComponentType> | ComponentType;
}

// Only the overview is in the entry chunk; every other screen loads on first visit.
export const ROUTES: AppRoute[] = [
  { path: '/', title: 'Stacks', screen: Overview },
  { path: '/stacks/new', title: 'New stack', screen: lazy(() => import('./routes/StackCreate')) },
  { path: '/stacks/import', title: 'Import', screen: lazy(() => import('./routes/StackImport')) },
  { path: '/stacks/:name', screen: lazy(() => import('./routes/StackDetail')) },
  { path: '/containers', title: 'Containers', screen: lazy(() => import('./routes/ContainerList')) },
  { path: '/containers/:name', screen: lazy(() => import('./routes/ContainerDetail')) },
  { path: '/networks', title: 'Networks', screen: lazy(() => import('./routes/NetworkList')) },
  { path: '/storage', title: 'Storage', screen: lazy(() => import('./routes/Storage')) },
  { path: '/storage/images/:id', title: 'Storage', screen: lazy(() => import('./routes/StorageImage')) },
  { path: '/storage/volumes/:name', title: 'Storage', screen: lazy(() => import('./routes/StorageVolume')) },
  { path: '/settings', title: 'Settings', screen: lazy(() => import('./routes/Settings')) },
  { path: '/login', screen: lazy(() => import('./routes/Login')) },
  { path: '/setup', screen: lazy(() => import('./routes/Setup')) },
  { path: '*', title: 'Page not found', screen: lazy(() => import('./routes/NotFound')) },
];

/** The route a path renders; ROUTES lists static paths before the `:param` patterns that overlap them. */
export function findRoute(path: string): AppRoute | undefined {
  return ROUTES.find((r) => r.path === '*' || matchRoute(r.path, path) !== null);
}
