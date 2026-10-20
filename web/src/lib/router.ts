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
