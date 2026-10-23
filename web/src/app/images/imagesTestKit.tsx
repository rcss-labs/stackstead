// Shared fixtures and mount helper for the Images screen tests.
import { render } from '@testing-library/react';
import { Suspense } from 'react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import type { ImageFiles, LibraryImage, LibraryImageDetail, LibraryServer, LibraryVersion } from '$lib/api/imageTypes';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream, type Routes as FakeRoutes } from '../../vitest-setup';
import { ImageDetailScreen } from './ImageDetailScreen';
import { ImagesScreen } from './ImagesScreen';

export const localOwner: LibraryServer = { id: 'abcdef012345ffff', name: 'Here', slug: '', local: true, status: 'reachable' };
export const peerOwner: LibraryServer = { id: 'peer-1', name: 'Mini 02', slug: 'mini-02', local: false, status: 'reachable' };

export const version = (over: Partial<LibraryVersion> = {}): LibraryVersion => ({
  id: 'v-3',
  tag: '3',
  status: 'succeeded',
  revision: 4,
  commit: '',
  buildArgs: {},
  arch: 'amd64',
  imageId: 'sha256:aaa',
  size: 1000,
  startedAt: '2026-10-20T10:00:00Z',
  endedAt: '2026-10-20T10:01:00Z',
  error: '',
  operationId: 'op-b1',
  ref: 'stackstead.invalid/abcdef012345/web:3',
  pushes: [],
  ...over,
});

export const image = (over: Partial<LibraryImage> = {}): LibraryImage => ({
  slug: 'web',
  name: 'Web',
  source: 'upload',
  dockerfilePath: 'Dockerfile',
  owner: localOwner,
  buildArgs: {},
  retention: { keepLast: 0, keepDays: 0 },
  latestVersion: version(),
  lastBuild: version(),
  pushTargets: [],
  createdAt: '2026-10-01T10:00:00Z',
  updatedAt: '2026-10-20T10:00:00Z',
  ...over,
});

export const imageDetail = (over: Partial<LibraryImageDetail> = {}): LibraryImageDetail => ({
  ...image(),
  versions: [version()],
  suggestedTag: '4',
  tagHint: '',
  arch: 'amd64',
  revision: 4,
  ...over,
});

export const filesOf = (over: Partial<ImageFiles> = {}): ImageFiles => ({
  revision: 4,
  files: [
    { path: 'Dockerfile', size: 18 },
    { path: 'app/main.sh', size: 30 },
  ],
  ...over,
});

export interface ImagesHarness {
  fake: FakeFetch;
  stream: FakeStream;
  /** Calls with this method whose path equals `path`. */
  calls: (method: string, path: string) => FakeFetch['calls'];
  restore: () => Promise<void>;
}

/** Installs a fake backend: `routes` on top of quiet defaults for what every screen asks. */
export function installImagesBackend(routes: FakeRoutes): ImagesHarness {
  const fake = installFakeFetch({
    'GET /api/operations?state=running': { body: [] },
    'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    ...routes,
  });
  const stream = installFakeStream({ '/api/events': [], '/api/operations/op-b1/stream': [] });
  return {
    fake,
    stream,
    calls: (method, path) => fake.calls.filter((c) => c.method === method && c.path === path),
    restore: async () => {
      stream.restore();
      fake.restore();
      await new Promise((r) => setTimeout(r, 5));
    },
  };
}

function Probe() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname + location.search}</output>;
}

export function mountImages(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Suspense fallback={<p>loading page</p>}>
        <Routes>
          <Route path="/images" element={<ImagesScreen />} />
          <Route path="/images/:slug" element={<ImageDetailScreen />} />
          <Route path="/s/:server/images/:slug" element={<p>peer image</p>} />
        </Routes>
      </Suspense>
      <Probe />
    </MemoryRouter>,
  );
}
