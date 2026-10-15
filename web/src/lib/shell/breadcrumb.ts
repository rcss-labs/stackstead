export interface Crumb {
  label: string;
  href?: string;
  mono?: boolean;
}

const stacks: Crumb = { label: 'Stacks', href: '/' };

/** Breadcrumb trail for a route path; the last crumb is the current page. */
export function breadcrumb(path: string): Crumb[] {
  if (path === '/') return [{ label: 'Stacks' }];
  if (path === '/settings') return [{ label: 'Settings' }];
  if (path === '/stacks/new') return [stacks, { label: 'New stack' }];
  if (path === '/stacks/import') return [stacks, { label: 'Import' }];
  const detail = /^\/stacks\/([^/]+)$/.exec(path);
  if (detail) {
    let name = detail[1];
    try {
      name = decodeURIComponent(name);
    } catch {
      /* keep the raw segment */
    }
    return [stacks, { label: name, mono: true }];
  }
  return [stacks, { label: 'Not found' }];
}
