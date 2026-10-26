import { splitServerPath } from '../router';

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

/**
 * Breadcrumb trail for a route path; the last crumb is the current page.
 * On a connected server the trail starts with the server and every link stays inside it.
 */
export function breadcrumb(path: string, serverName?: string): Crumb[] {
  const { slug, inner } = splitServerPath(path);
  if (serverName === undefined || slug === null) return localBreadcrumb(path);
  const prefix = `/s/${encodeURIComponent(slug)}`;
  const scoped = localBreadcrumb(inner).map((c) =>
    c.href === undefined ? c : { ...c, href: c.href === '/' ? prefix : prefix + c.href },
  );
  return [{ label: serverName, href: prefix }, ...scoped];
}

function localBreadcrumb(path: string): Crumb[] {
  if (path === '/storage') return [{ label: 'Storage' }];
  const storage = /^\/storage\/(images|volumes)\/([^/]+)$/.exec(path);
  if (storage) return [{ label: 'Storage', href: '/storage' }, { label: storage[1] === 'images' ? 'Images' : 'Volumes', href: `/storage?tab=${storage[1]}` }, { label: decode(storage[2]), mono: true }];
  if (path === '/backups') return [{ label: 'Backups' }];
  if (path === '/images') return [{ label: 'Images' }];
  const image = /^\/images\/([^/]+)$/.exec(path);
  if (image) return [{ label: 'Images', href: '/images' }, { label: decode(image[1]), mono: true }];
  if (path === '/') return [{ label: 'Stacks' }];
  if (path === '/all') return [{ label: 'All stacks' }];
  if (path === '/settings') return [{ label: 'Settings' }];
  if (path === '/stacks/new') return [stacks, { label: 'New stack' }];
  if (path === '/stacks/import') return [stacks, { label: 'Import' }];
  if (path === '/networks') return [{ label: 'Networks' }];
  const network = /^\/networks\/([^/]+)$/.exec(path);
  if (network) return [{ label: 'Networks', href: '/networks' }, { label: decode(network[1]), mono: true }];
  if (path === '/containers') return [{ label: 'Containers' }];
  const container = /^\/containers\/([^/]+)$/.exec(path);
  if (container) return [containers, { label: decode(container[1]), mono: true }];
  const detail = /^\/stacks\/([^/]+)$/.exec(path);
  if (detail) {
    return [stacks, { label: decode(detail[1]), mono: true }];
  }
  return [stacks, { label: 'Not found' }];
}
