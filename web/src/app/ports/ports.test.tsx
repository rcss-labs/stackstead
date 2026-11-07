import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it } from 'vitest';
import type { StackSummary } from '$lib/api/types';
import { writable } from '$lib/store';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { localScope, ServerContext } from '../servers/ServerContext';
import { PortsScreen } from './PortsScreen';

let fake: FakeFetch | undefined;
afterEach(() => {
  cleanup();
  fake?.restore();
});

const stack = { name: 'media', path: '', project: 'media', origin: 'managed', status: 'running', serviceCount: 1, running: 1, health: {}, ports: ['8001-8003', '80'], libraryUpdates: 0 } as unknown as StackSummary;
const redis = { id: 'redis', name: 'redis', image: 'redis:7', imageId: '', state: 'running', status: '', health: 'none', exitCode: null, createdAt: '', project: '', service: '', stack: '', managed: 'none', ports: ['0.0.0.0:6379->6379/tcp'], networks: {}, cpu: null, mem: null, memLimit: null, restarts24h: 0 };

function mount(containers: unknown[] | 'fail' = [redis]) {
  fake = installFakeFetch({ 'GET /api/containers': containers === 'fail' ? { status: 500, body: { error: 'down' } } : { body: containers } });
  const scope = { ...localScope, stacks: writable({ items: [stack], loading: false }), changes: writable(null), href: (p: string) => `/s/mini${p}` };
  return render(
    <MemoryRouter>
      <ServerContext.Provider value={scope}>
        <PortsScreen />
      </ServerContext.Provider>
    </MemoryRouter>,
  );
}

it('lists ranges and unmanaged containers, linking through href()', async () => {
  mount();
  expect(await screen.findByText(':6379')).toBeTruthy();
  expect(screen.getByText(':8001-8003')).toBeTruthy();
  const stackLinks = screen.getAllByRole('link', { name: 'media' });
  expect(stackLinks[0].getAttribute('href')).toBe('/s/mini/stacks/media');
  const redisLink = screen.getByRole('link', { name: 'redis' });
  expect(redisLink.getAttribute('href')).toBe('/s/mini/containers/redis');
  // The unmanaged container has no stack link.
  const row = redisLink.closest('tr')!;
  expect(row.querySelector('a[href*="/stacks/"]')).toBeNull();
});

it('shows the next free port above 8000, skipping ranges', async () => {
  mount();
  expect(await screen.findByText('8004')).toBeTruthy();
});

it('filters by port, stack or service', async () => {
  const user = userEvent.setup();
  mount();
  await screen.findByText(':6379');
  await user.type(screen.getByRole('searchbox', { name: 'Filter ports' }), 'redis');
  expect(screen.queryByText(':8001-8003')).toBeNull();
  expect(screen.getByText(':6379')).toBeTruthy();
});

it('a failing container list shows a notice and keeps the stack ports', async () => {
  mount('fail');
  expect(await screen.findByText(/Could not read running containers/)).toBeTruthy();
  expect(screen.getByText(':8001-8003')).toBeTruthy();
});
