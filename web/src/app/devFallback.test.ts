import { describe, expect, it } from 'vitest';
import { isAppNavigation } from './devFallback';

const html = 'text/html,application/xhtml+xml';

describe('isAppNavigation', () => {
  it.each([
    ['GET', html, '/', true],
    ['GET', html, '/stacks/web', true],
    ['GET', html, '/containers/my.app', true],
    ['GET', html, '/storage/volumes/data.v1', true],
    ['GET', html, '/stacks/web?tab=logs', true],
    ['HEAD', html, '/stacks/web', true],
    ['GET', html, '/api/status', false],
    ['GET', html, '/metrics', false],
    ['GET', '*/*', '/src/app/main.tsx', false],
    ['GET', '*/*', '/assets/x.js', false],
    ['GET', html, '/app.html', false],
    ['POST', html, '/stacks/web', false],
    ['GET', undefined, '/stacks/web', false],
  ])('%s %s %s -> %s', (method, accept, url, expected) => {
    expect(isAppNavigation(method, accept, url)).toBe(expected);
  });
});
