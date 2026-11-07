import { describe, expect, it } from 'vitest';
import { breadcrumb } from './breadcrumb';

describe('breadcrumb', () => {
  it.each([
    ['/', [{ label: 'Overview' }]],
    ['/stacks/new', [{ label: 'Overview', href: '/' }, { label: 'New stack' }]],
    ['/stacks/import', [{ label: 'Overview', href: '/' }, { label: 'Import' }]],
    ['/stacks/web', [{ label: 'Overview', href: '/' }, { label: 'web', mono: true }]],
    ['/stacks/my%20stack', [{ label: 'Overview', href: '/' }, { label: 'my stack', mono: true }]],
    ['/all', [{ label: 'All stacks' }]],
    ['/settings', [{ label: 'Settings' }]],
    ['/settings/system', [{ label: 'Settings', href: '/settings' }, { label: 'System' }]],
    ['/activity', [{ label: 'Activity' }]],
    ['/ports', [{ label: 'Ports' }]],
    ['/more', [{ label: 'More' }]],
    ['/networks/ports', [{ label: 'Networks', href: '/networks' }, { label: 'ports', mono: true }]],
    ['/stacks/activity', [{ label: 'Overview', href: '/' }, { label: 'activity', mono: true }]],
    ['/containers', [{ label: 'Containers' }]],
    ['/networks', [{ label: 'Networks' }]],
    ['/networks/my%20net', [{ label: 'Networks', href: '/networks' }, { label: 'my net', mono: true }]],
    ['/containers/redis', [{ label: 'Containers', href: '/containers' }, { label: 'redis', mono: true }]],
    ['/containers/my%20db', [{ label: 'Containers', href: '/containers' }, { label: 'my db', mono: true }]],
    ['/nope', [{ label: 'Overview', href: '/' }, { label: 'Not found' }]],
    ['/storage', [{ label: 'Storage' }]],
    ['/images', [{ label: 'Images' }]],
    ['/images/web', [{ label: 'Images', href: '/images' }, { label: 'web', mono: true }]],
    [
      '/storage/images/abc',
      [{ label: 'Storage', href: '/storage' }, { label: 'Images', href: '/storage?tab=images' }, { label: 'abc', mono: true }],
    ],
    [
      '/storage/volumes/media_db',
      [
        { label: 'Storage', href: '/storage' },
        { label: 'Volumes', href: '/storage?tab=volumes' },
        { label: 'media_db', mono: true },
      ],
    ],
  ])('%s', (path, crumbs) => {
    expect(breadcrumb(path)).toEqual(crumbs);
  });
});

describe('breadcrumb on a connected server', () => {
  it('prefixes the server and keeps links inside it', () => {
    expect(breadcrumb('/s/mini-02', 'mini-02')).toEqual([{ label: 'mini-02', href: '/s/mini-02' }, { label: 'Overview' }]);
    expect(breadcrumb('/s/mini-02/stacks/web', 'mini-02')).toEqual([
      { label: 'mini-02', href: '/s/mini-02' },
      { label: 'Overview', href: '/s/mini-02' },
      { label: 'web', mono: true },
    ]);
  });
});
