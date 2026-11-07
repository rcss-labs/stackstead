import { describe, expect, it } from 'vitest';
import { SERVER_ROUTES, findRoute } from './routes';

describe('image routes', () => {
  it('resolve on this server and on a connected one', () => {
    expect(findRoute('/images')?.path).toBe('/images');
    expect(findRoute('/images/web')?.path).toBe('/images/:slug');
    expect(findRoute('/s/x/images')?.path).toBe('/images');
    expect(findRoute('/s/x/images/web')?.path).toBe('/images/:slug');
  });

  it('are server routes', () => {
    expect(SERVER_ROUTES.map((r) => r.path)).toEqual(expect.arrayContaining(['images', 'images/:slug']));
  });
});

describe('shell routes', () => {
  it('/activity and /ports open on this server and under /s/:server', () => {
    expect(findRoute('/activity')?.path).toBe('/activity');
    expect(findRoute('/ports')?.path).toBe('/ports');
    expect(findRoute('/s/x/activity')?.path).toBe('/activity');
    expect(findRoute('/s/x/ports')?.path).toBe('/ports');
    expect(SERVER_ROUTES.map((r) => r.path)).toEqual(expect.arrayContaining(['activity', 'ports']));
  });

  it('/more and /settings are not per-server', () => {
    const paths = SERVER_ROUTES.map((r) => r.path);
    for (const local of ['more', 'settings', 'settings/:section']) expect(paths).not.toContain(local);
    expect(findRoute('/s/x/more')?.path).toBe('*');
    expect(findRoute('/s/x/settings/system')?.path).toBe('*');
    expect(findRoute('/more')?.path).toBe('/more');
    expect(findRoute('/settings/system')?.path).toBe('/settings/:section');
  });

  it('a network named ports and a stack named activity still open', () => {
    expect(findRoute('/networks/ports')?.path).toBe('/networks/:name');
    expect(findRoute('/networks/more')?.path).toBe('/networks/:name');
    expect(findRoute('/stacks/activity')?.path).toBe('/stacks/:name');
    expect(findRoute('/stacks/more')?.path).toBe('/stacks/:name');
    expect(findRoute('/s/x/networks/ports')?.path).toBe('/networks/:name');
    expect(findRoute('/s/x/stacks/activity')?.path).toBe('/stacks/:name');
    expect(findRoute('/containers/ports')?.path).toBe('/containers/:name');
  });
});
