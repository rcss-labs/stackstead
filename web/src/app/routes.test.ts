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
