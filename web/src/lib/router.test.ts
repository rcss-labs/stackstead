import { describe, expect, it } from 'vitest';
import { matchRoute, safeNext } from './router';

describe('matchRoute', () => {
  it.each([
    ['/', '/', {}],
    ['/stacks/:name', '/stacks/web', { name: 'web' }],
    ['/stacks/:name', '/stacks/my-stack_2', { name: 'my-stack_2' }],
    ['/stacks/:name', '/stacks/a%2Fb', { name: 'a/b' }],
    ['/stacks/:name', '/stacks/web/', { name: 'web' }],
    ['/stacks/:name/edit', '/stacks/web/edit', { name: 'web' }],
  ])('%s matches %s', (pattern, path, params) => {
    expect(matchRoute(pattern, path)).toEqual(params);
  });

  it.each([
    ['/stacks/:name', '/stacks/a/b'],
    ['/stacks/:name', '/stacks'],
    ['/stacks/:name', '/other/web'],
    ['/', '/stacks'],
  ])('%s does not match %s', (pattern, path) => {
    expect(matchRoute(pattern, path)).toBeNull();
  });

  it('does not throw on malformed percent-encoding', () => {
    expect(matchRoute('/stacks/:name', '/stacks/%E0%A4%A')).toBeNull();
  });
});

describe('safeNext', () => {
  it('keeps same-origin paths', () => {
    expect(safeNext('/stacks/web')).toBe('/stacks/web');
    expect(safeNext('/stacks?x=1#a')).toBe('/stacks?x=1#a');
    expect(safeNext('/%2f%2fevil')).toBe('/%2f%2fevil'); // still a path on this origin
  });
  it.each([
    'https://evil',
    '//evil',
    '/\\evil',
    '\\\\evil',
    'javascript:alert(1)',
    'stacks',
    '/\t/evil',
    '/a\\b',
    '/\n/evil',
    '/.//evil',
    '/a/..//evil',
    'JAVASCRIPT:alert(1)',
    'data:text/html,x',
    '',
    null,
  ])('rejects %j', (v) => {
    expect(safeNext(v)).toBe('/');
  });
});
