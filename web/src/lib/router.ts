import { writable, type Readable } from '$lib/store';

export interface RouteMatch {
  params: Record<string, string>;
  query: URLSearchParams;
  path: string;
}

interface RouteState {
  path: string;
  query: URLSearchParams;
}

function segments(path: string): string[] {
  return path.split('/').filter((s) => s !== '');
}

/** Matches a pattern like '/stacks/:name' against a path; ':x' matches exactly one decoded segment. */
export function matchRoute(pattern: string, path: string): Record<string, string> | null {
  const want = segments(pattern);
  const got = segments(path);
  if (want.length !== got.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < want.length; i++) {
    let value: string;
    try {
      value = decodeURIComponent(got[i]);
    } catch {
      return null;
    }
    if (want[i].startsWith(':')) {
      params[want[i].slice(1)] = value;
    } else if (want[i] !== value) {
      return null;
    }
  }
  return params;
}

function current(): RouteState {
  return { path: window.location.pathname, query: new URLSearchParams(window.location.search) };
}

const state = writable<RouteState>(current());

/** Current location. `query` is shared between subscribers: copy it (new URLSearchParams(q)) before mutating. */
export const route: Readable<RouteState> = { subscribe: state.subscribe };

export function navigate(to: string, opts: { replace?: boolean } = {}): void {
  if (opts.replace) window.history.replaceState(null, '', to);
  else window.history.pushState(null, '', to);
  state.set(current());
}

export function link(node: HTMLAnchorElement): { destroy(): void } {
  const onClick = (ev: MouseEvent) => {
    if (ev.defaultPrevented || ev.button !== 0) return;
    if (ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.altKey) return;
    const target = node.getAttribute('target');
    if ((target && target !== '_self') || node.hasAttribute('download')) return;
    const href = node.getAttribute('href');
    if (!href) return;
    let url: URL;
    try {
      url = new URL(href, window.location.href);
    } catch {
      return;
    }
    if (url.origin !== window.location.origin) return;
    // Same path+query (hash-only skip links, or a link to the current page): let the browser handle it.
    if (url.pathname === window.location.pathname && url.search === window.location.search) return;
    ev.preventDefault();
    navigate(url.pathname + url.search + url.hash);
  };
  node.addEventListener('click', onClick);
  return { destroy: () => node.removeEventListener('click', onClick) };
}

export function startRouter(): () => void {
  const onPop = () => state.set(current());
  window.addEventListener('popstate', onPop);
  state.set(current());
  return () => window.removeEventListener('popstate', onPop);
}

/**
 * Returns `next` only when it is a same-origin path. Anything else (absolute URLs, `//host`,
 * `/\host`, `javascript:`, control characters) falls back to '/', so ?next= cannot be an open redirect.
 */
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith('/') || next.startsWith('//')) return '/';
  // Backslashes and control chars are treated as '/' or stripped by browsers, so '/\evil' can become '//evil'.
  // eslint-disable-next-line no-control-regex
  if (/[\\\u0000-\u001f\u007f]/.test(next)) return '/';
  try {
    const url = new URL(next, window.location.origin);
    if (url.origin !== window.location.origin) return '/';
    // Normalisation can collapse '/.//evil' into the protocol-relative '//evil'.
    if (url.pathname.startsWith('//') || url.pathname.includes('\\')) return '/';
    return url.pathname + url.search + url.hash;
  } catch {
    return '/';
  }
}
