import type { LibraryImage, LibraryServer } from '../api/imageTypes';

/** Local owner first, then peers by name; images by name within each owner. */
export function groupByOwner(images: LibraryImage[]): { owner: LibraryServer; images: LibraryImage[] }[] {
  const groups = new Map<string, { owner: LibraryServer; images: LibraryImage[] }>();
  for (const image of images) {
    const group = groups.get(image.owner.id) ?? { owner: image.owner, images: [] };
    group.images.push(image);
    groups.set(image.owner.id, group);
  }
  const ordered = [...groups.values()].sort((a, b) => {
    if (a.owner.local !== b.owner.local) return a.owner.local ? -1 : 1;
    return a.owner.name.localeCompare(b.owner.name);
  });
  for (const group of ordered) group.images.sort((a, b) => a.name.localeCompare(b.name));
  return ordered;
}

/** `viewedServerId` 'local' stands for this server's own owner. */
export function ownerIsViewed(image: LibraryImage, viewedServerId: string): boolean {
  return image.owner.id === viewedServerId || (viewedServerId === 'local' && image.owner.local);
}

/** Owned by the viewed server: the scoped route. Owned by a peer: that peer's route. */
export function imageHref(image: LibraryImage, scope: { id: string; href(p: string): string }): string {
  if (ownerIsViewed(image, scope.id)) return scope.href(`/images/${image.slug}`);
  return `/s/${image.owner.slug}/images/${image.slug}`;
}
