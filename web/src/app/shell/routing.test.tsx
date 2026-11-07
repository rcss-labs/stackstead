import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { Suspense } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PeerView } from '$lib/api/types';
import { startPeers } from '$lib/servers/peers';
import { findRoute } from '../routes';

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

afterEach(async () => {
  cleanup();
  vi.unstubAllGlobals();
  await sleep(5);
});

function Where() {
  const { pathname, search, hash } = useLocation();
  return <span data-testid="where">{pathname + search + hash}</span>;
}

const stubViewport = (matches: boolean) =>
  vi.stubGlobal('matchMedia', (query: string) => ({ matches, media: query, addEventListener() {}, removeEventListener() {} }));

/** Renders the real route elements for the given patterns, so a redirect or a screen is exercised as App mounts it. */
function renderRoutes(at: string, patterns: string[]) {
  return render(
    <MemoryRouter initialEntries={[at]}>
      <Suspense fallback={null}>
        <Routes>
          {patterns.map((pattern) => {
            const Screen = findRoute(pattern)!.screen;
            return <Route key={pattern} path={pattern} element={<Screen />} />;
          })}
          <Route path="*" element={<span>landed</span>} />
        </Routes>
      </Suspense>
      <Where />
    </MemoryRouter>,
  );
}

describe('/settings', () => {
  it('redirects to /settings/system and keeps a fragment from the single-page layout', async () => {
    renderRoutes('/settings#settings-servers', ['/settings']);
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/settings/system#settings-servers'));
  });
});

describe('/more', () => {
  it('redirects to / on desktop width', async () => {
    stubViewport(false);
    renderRoutes('/more', ['/more']);
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/'));
  });

  it('lists every page the tab bar does not on a phone, for this server', async () => {
    stubViewport(true);
    renderRoutes('/more', ['/more']);
    await screen.findByRole('link', { name: 'Images' });
    const hrefs = Object.fromEntries(screen.getAllByRole('link').map((a) => [a.textContent, a.getAttribute('href')]));
    expect(hrefs).toMatchObject({
      Images: '/images',
      Networks: '/networks',
      Storage: '/storage',
      Backups: '/backups',
      Ports: '/ports',
      'New stack': '/stacks/new',
      Import: '/stacks/import',
      Settings: '/settings',
    });
    expect(screen.queryByRole('link', { name: 'All servers' })).toBeNull();
  });

  it('keeps the connected server it was opened from', async () => {
    stubViewport(true);
    renderRoutes('/more?server=mini-02', ['/more']);
    expect((await screen.findByRole('link', { name: 'Storage' })).getAttribute('href')).toBe('/s/mini-02/storage');
    expect(screen.getByRole('link', { name: 'Ports' }).getAttribute('href')).toBe('/s/mini-02/ports');
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/s/mini-02/stacks/new');
    expect(screen.getByRole('link', { name: 'Settings' }).getAttribute('href')).toBe('/settings');
  });

  it('offers All servers and Connected servers once a server is connected', async () => {
    stubViewport(true);
    const peer: PeerView = {
      id: 'b1', name: 'mini-02', slug: 'mini-02', url: 'https://mini-02:8080', version: '1.5', status: 'reachable',
      stackCount: 4, lastSeenAt: null, lastError: '', compatible: true, plaintext: false,
    };
    const stop = startPeers({ list: async () => [peer], self: async () => ({ name: 'ratsrv01' }) as never });
    try {
      renderRoutes('/more', ['/more']);
      const all = await screen.findByRole('link', { name: 'All servers' });
      expect(all.getAttribute('href')).toBe('/all');
      expect(screen.getByRole('link', { name: 'Connected servers' }).getAttribute('href')).toBe('/settings/servers');
    } finally {
      cleanup();
      stop();
      await sleep(5);
    }
  });
});
