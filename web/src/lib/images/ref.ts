// Library reference: stackstead.invalid/<owner-id>/<slug>:<tag>. Mirrors internal/imageref.
const DOMAIN = 'stackstead.invalid';
const OWNER = /^[0-9a-f]{12}$/;
const SLUG = /^[a-z0-9]+(?:[-_][a-z0-9]+)*$/;
const TAG = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/;
const SLUG_MAX = 40;

export function parseLibraryRef(s: string): { owner: string; slug: string; tag: string } | null {
  const m = new RegExp(`^${DOMAIN.replace(/\./g, '\\.')}/([^/:]+)/([^/:]+):([^/:]+)$`).exec(s);
  if (!m) return null;
  const [, owner, slug, tag] = m;
  if (!OWNER.test(owner) || slug.length > SLUG_MAX || !SLUG.test(slug) || !TAG.test(tag)) return null;
  return { owner, slug, tag };
}

export function formatLibraryRef(owner: string, slug: string, tag: string): string {
  return `${DOMAIN}/${owner}/${slug}:${tag}`;
}
