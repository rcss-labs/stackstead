import { cleanup, screen } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { member, mountAt, routes, row } from './networksTestKit';

// Phone width is stubbed for the whole file: useIsMobile caches its media query across renders in one module.
vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const live = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: live.subscribe },
    changes: { subscribe: changes.subscribe },
    refreshStatus: vi.fn(),
  };
});

const fleet = [row('shop_default', { containers: [member('web', { stack: 'shop' })] }), row('spare')];

let fake: FakeFetch;
beforeAll(() => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query === '(max-width: 759px)',
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
});
afterAll(() => vi.unstubAllGlobals());
afterEach(() => {
  cleanup();
  fake?.restore();
});

function mount(path: string) {
  fake = installFakeFetch(routes({ body: fleet }));
  mountAt(path);
}

it('mobile list and detail are separate screens: the list alone, without the empty detail', async () => {
  mount('/networks');
  await screen.findByRole('listbox', { name: 'Networks' });
  expect(screen.queryByRole('heading', { name: 'Select a network' })).toBeNull();
});

it('mobile detail screen has no list beside it', async () => {
  mount('/networks/shop_default');
  await screen.findByRole('heading', { level: 2, name: 'shop_default' });
  expect(screen.queryByRole('listbox', { name: 'Networks' })).toBeNull();
  expect(screen.queryByRole('tablist')).toBeNull();
});
