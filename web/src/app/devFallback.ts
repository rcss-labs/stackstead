const NON_PAGE_PREFIXES = ['/api', '/metrics', '/@', '/src', '/node_modules'];

// A dev request that should be answered with app.html: a page navigation to a
// client-side route. Dots are allowed (container and volume names contain them).
export function isAppNavigation(method: string, accept: string | undefined, url: string): boolean {
  const path = url.split('?')[0] ?? '';
  return (
    (method === 'GET' || method === 'HEAD') &&
    (accept ?? '').includes('text/html') &&
    !NON_PAGE_PREFIXES.some((prefix) => path.startsWith(prefix)) &&
    !path.endsWith('.html')
  );
}
