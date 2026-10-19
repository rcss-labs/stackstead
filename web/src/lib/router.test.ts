import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { get } from '$lib/store';
import { link, matchRoute, navigate, route, safeNext, startRouter } from './router';

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

describe('navigation', () => {
  let stop: () => void;
  beforeEach(() => {
    window.history.replaceState(null, '', '/');
    stop = startRouter();
  });
  afterEach(() => stop());

  it('navigate pushes history and updates the store synchronously', () => {
    const before = window.history.length;
    navigate('/stacks/web?tab=logs');
    expect(window.history.length).toBe(before + 1);
    const r = get(route);
    expect(r.path).toBe('/stacks/web');
    expect(r.query.get('tab')).toBe('logs');
    expect(window.location.pathname + window.location.search).toBe('/stacks/web?tab=logs');
  });

  it('navigate with replace does not grow history', () => {
    const before = window.history.length;
    navigate('/login', { replace: true });
    expect(window.history.length).toBe(before);
    expect(get(route).path).toBe('/login');
  });

  it('popstate updates the store', () => {
    navigate('/a');
    navigate('/b');
    window.history.replaceState(null, '', '/c?x=1');
    window.dispatchEvent(new PopStateEvent('popstate'));
    expect(get(route).path).toBe('/c');
    expect(get(route).query.get('x')).toBe('1');
  });

  it('stop removes the popstate listener', () => {
    stop();
    window.history.replaceState(null, '', '/ignored');
    window.dispatchEvent(new PopStateEvent('popstate'));
    expect(get(route).path).not.toBe('/ignored');
    stop = startRouter();
  });
});

describe('link action', () => {
  let stop: () => void;
  let a: HTMLAnchorElement;
  let action: { destroy(): void };
  beforeEach(() => {
    window.history.replaceState(null, '', '/');
    stop = startRouter();
    a = document.createElement('a');
    a.setAttribute('href', '/stacks/web');
    document.body.appendChild(a);
    action = link(a);
  });
  afterEach(() => {
    action.destroy();
    a.remove();
    stop();
  });

  const click = (init: MouseEventInit = {}) => {
    const ev = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...init });
    // Record whether the router prevented the click, then block jsdom's unimplemented real navigation.
    let prevented = false;
    const sink = (e: Event) => {
      prevented = e.defaultPrevented;
      e.preventDefault();
    };
    document.addEventListener('click', sink);
    a.dispatchEvent(ev);
    document.removeEventListener('click', sink);
    return { defaultPrevented: prevented };
  };

  it('intercepts a plain click', () => {
    const ev = click();
    expect(ev.defaultPrevented).toBe(true);
    expect(get(route).path).toBe('/stacks/web');
  });

  it.each([
    ['ctrl', { ctrlKey: true }],
    ['meta', { metaKey: true }],
    ['shift', { shiftKey: true }],
    ['alt', { altKey: true }],
    ['middle button', { button: 1 }],
  ])('ignores %s clicks', (_n, init) => {
    const ev = click(init);
    expect(ev.defaultPrevented).toBe(false);
    expect(get(route).path).toBe('/');
  });

  it('ignores target=_blank, download and external hrefs', () => {
    a.setAttribute('target', '_blank');
    expect(click().defaultPrevented).toBe(false);
    a.removeAttribute('target');
    a.setAttribute('download', '');
    expect(click().defaultPrevented).toBe(false);
    a.removeAttribute('download');
    a.setAttribute('href', 'https://example.com/stacks/web');
    expect(click().defaultPrevented).toBe(false);
    expect(get(route).path).toBe('/');
  });

  it('does not intercept protocol-relative or javascript: hrefs', () => {
    a.setAttribute('href', '//evil.example/x');
    expect(click().defaultPrevented).toBe(false);
    a.setAttribute('href', 'javascript:void(0)');
    expect(click().defaultPrevented).toBe(false);
    expect(get(route).path).toBe('/');
  });

  it('leaves hash-only links to the browser', () => {
    a.setAttribute('href', '#main');
    const before = window.history.length;
    expect(click().defaultPrevented).toBe(false);
    expect(window.history.length).toBe(before);
  });

  it('does not push a duplicate entry for a link to the current URL', () => {
    navigate('/stacks/web');
    const before = window.history.length;
    expect(click().defaultPrevented).toBe(false);
    expect(window.history.length).toBe(before);
  });

  it('stops intercepting after destroy', () => {
    action.destroy();
    expect(click().defaultPrevented).toBe(false);
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
