import { splitServerPath } from '$lib/router';
import { breadcrumb } from '$lib/shell/breadcrumb';

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
