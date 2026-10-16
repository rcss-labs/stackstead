export interface Crumb {
  label: string;
  href?: string;
  mono?: boolean;
}

const stacks: Crumb = { label: 'Stacks', href: '/' };

const containers: Crumb = { label: 'Containers', href: '/containers' };

const decode = (segment: string) => {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment; // keep the raw segment
  }
};

/** Breadcrumb trail for a route path; the last crumb is the current page. */
export function breadcrumb(path: string): Crumb[] {
  if (path === '/') return [{ label: 'Stacks' }];
  if (path === '/settings') return [{ label: 'Settings' }];
  if (path === '/stacks/new') return [stacks, { label: 'New stack' }];
  if (path === '/stacks/import') return [stacks, { label: 'Import' }];
  if (path === '/networks') return [{ label: 'Networks' }];
  if (path === '/containers') return [{ label: 'Containers' }];
  const container = /^\/containers\/([^/]+)$/.exec(path);
  if (container) return [containers, { label: decode(container[1]), mono: true }];
  const detail = /^\/stacks\/([^/]+)$/.exec(path);
  if (detail) {
    return [stacks, { label: decode(detail[1]), mono: true }];
  }
  return [stacks, { label: 'Not found' }];
}
