import { describe, expect, it } from 'vitest';
import type { LibraryImage, LibraryServer } from '../api/imageTypes';
import { groupByOwner, imageHref, ownerIsViewed } from './library';

function server(id: string, name: string, local: boolean): LibraryServer {
  return { id, name, slug: name.toLowerCase(), local, status: 'reachable' };
}

function image(slug: string, owner: LibraryServer): LibraryImage {
  return {
    slug,
    name: slug,
    source: 'dockerfile',
    dockerfilePath: 'Dockerfile',
    owner,
    buildArgs: {},
    retention: { keepLast: 0, keepDays: 0 },
    pushTargets: [],
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
  };
}

const local = server('local-id', 'Home', true);
const mini = server('peer-a', 'mini-02', false);
const alpha = server('peer-b', 'Alpha', false);

describe('groupByOwner', () => {
  it('puts the local owner first, then peers by name, with images sorted by name', () => {
    const groups = groupByOwner([
      image('web', mini),
      image('api', alpha),
      image('worker', local),
      image('cache', local),
    ]);
    expect(groups.map((g) => g.owner.name)).toEqual(['Home', 'Alpha', 'mini-02']);
    expect(groups[0].images.map((i) => i.slug)).toEqual(['cache', 'worker']);
  });
});

describe('ownerIsViewed', () => {
  it('matches the owner id of the viewed server', () => {
    expect(ownerIsViewed(image('web', mini), 'peer-a')).toBe(true);
    expect(ownerIsViewed(image('web', mini), 'peer-b')).toBe(false);
  });

  it("maps 'local' to the local owner", () => {
    expect(ownerIsViewed(image('web', local), 'local')).toBe(true);
    expect(ownerIsViewed(image('web', mini), 'local')).toBe(false);
  });
});

describe('imageHref', () => {
  const scope = { id: 'peer-a', href: (p: string) => `/scoped${p}` };

  it('scopes an image owned by the viewed server', () => {
    expect(imageHref(image('web', mini), scope)).toBe('/scoped/images/web');
  });

  it('links a peer-owned image to its owner', () => {
    expect(imageHref(image('web', alpha), scope)).toBe('/s/alpha/images/web');
  });
});
