import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { api } from '$lib/api/client';
import type { PeerView } from '$lib/api/types';
import { refreshPeers, startPeers } from '$lib/servers/peers';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import RemoteServer from './RemoteServer';

const peer = (over: Partial<PeerView> = {}): PeerView => ({
  id: 'b1',
  name: 'mini-02',
  slug: 'mini-02',
  url: 'https://mini-02:8080',
  version: '1.4',
  status: 'reachable',
  stackCount: 1,
  lastSeenAt: new Date(Date.now() - 5 * 60_000).toISOString(),
  lastError: '',
  compatible: true,
  plaintext: false,
  ...over,
});

const stack = {
  name: 'remote-web',
  path: '/srv/stacks/remote-web/compose.yaml',
  project: 'remote-web',
  origin: 'managed',
  status: 'running',
  serviceCount: 1,
  running: 1,
  health: { healthy: 1, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
};

let fake: FakeFetch;
let stream: FakeStream;
let peerRes: PeerView;
let renamed: PeerView | undefined;
let stopPeers: () => void;

let go: (path: string) => void = () => {};

function Leave() {
  const navigate = useNavigate();
  go = navigate;
  return (
    <>
      <button onClick={() => navigate('/')}>leave</button>
    </>
  );
}

function mount(path: string) {
  stopPeers = startPeers({ list: api.peers });
  render(
    <MemoryRouter initialEntries={[path]}>
      <Leave />
      <Routes>
        <Route path="/s/:server/*" element={<RemoteServer />} />
        <Route path="/" element={<p>local home</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  peerRes = peer();
  renamed = undefined;
  fake = installFakeFetch({
    'GET /api/peers': () => ({ body: renamed ? [peerRes, renamed] : [peerRes] }),
    'POST /api/peers/b1/check': () => ({ body: peerRes }),
    'GET /api/peers/self': { body: { instanceId: 'x', name: 'here', slug: 'here', advertisedUrl: '', suggestedUrl: '', version: '1.5' } },
    'GET /api/servers/b1/api/stacks': { body: [stack] },
    'GET /api/servers/b1/api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    'GET /api/servers/b1/api/host': { body: {} },
    'GET /api/servers/b1/api/operations?state=running': { body: [] },
    'GET /api/stacks': { body: [] },
  });
  stream = installFakeStream({ '/api/servers/b1/api/events': [': ok\n\n'] });
  stopPeers = () => {};
});

afterEach(() => {
  cleanup();
  stopPeers();
  stream.restore();
  fake.restore();
});

describe('RemoteServer', () => {
  it('remote Overview loads from the peer', async () => {
    mount('/s/mini-02');
    await screen.findAllByText('remote-web');
    expect(fake.calls.some((c) => c.path === '/api/servers/b1/api/stacks')).toBe(true);
    expect(fake.calls.some((c) => c.path === '/api/stacks')).toBe(false);
    expect(stream.requests.filter((r) => r.path === '/api/servers/b1/api/events')).toHaveLength(1);
  });

  it('a renamed server remounts its screens instead of keeping state on the old scope', async () => {
    mount('/s/mini-02');
    await screen.findAllByText('remote-web');
    await userEvent.type(screen.getByRole('searchbox', { name: 'Search stacks' }), 'rem');
    // While the rename propagates, the old and the new slug both resolve to the same server id.
    renamed = peer({ name: 'mini-03', slug: 'mini-03' });
    await act(() => refreshPeers());
    await act(async () => go('/s/mini-03'));
    await screen.findAllByText('remote-web');
    expect((screen.getByRole('searchbox', { name: 'Search stacks' }) as HTMLInputElement).value).toBe('');
  });

  it('leaving the peer closes its events stream', async () => {
    mount('/s/mini-02');
    await screen.findAllByText('remote-web');
    const signal = stream.requests.find((r) => r.path === '/api/servers/b1/api/events')?.signal;
    expect(signal?.aborted).toBe(false);
    await userEvent.click(screen.getByText('leave'));
    await screen.findByText('local home');
    await waitFor(() => expect(signal?.aborted).toBe(true));
  });

  it('unreachable peer shows the can\'t-reach state', async () => {
    peerRes = peer({ status: 'unreachable', lastError: 'connection refused' });
    mount('/s/mini-02');
    await screen.findByText(/Can't reach mini-02 \(last seen 5m ago\): connection refused/);
    expect(stream.requests).toHaveLength(0);
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === 'POST' && c.path === '/api/peers/b1/check')).toBe(true));
  });

  it('omits the missing parts of the unreachable copy', async () => {
    peerRes = peer({ status: 'unreachable', lastError: '', lastSeenAt: null });
    mount('/s/mini-02');
    await screen.findByText("Can't reach mini-02");
  });

  it('incompatible peer shows the update message', async () => {
    peerRes = peer({ status: 'incompatible', compatible: false, version: '1.4' });
    mount('/s/mini-02');
    await screen.findByText('mini-02 runs v1.4, this server v1.5. Update to match');
  });

  it('shows the removed state', async () => {
    peerRes = peer({ status: 'removed' });
    mount('/s/mini-02');
    await screen.findByText('mini-02 removed this connection');
  });

  it('Remove on a removed connection deletes it and leaves', async () => {
    peerRes = peer({ status: 'removed' });
    fake.restore();
    fake = installFakeFetch({
      'GET /api/peers': () => ({ body: [peerRes] }),
      'DELETE /api/peers/b1': () => {
        peerRes = peer({ id: 'zz', slug: 'zz' });
        return { status: 204 };
      },
    });
    mount('/s/mini-02');
    await userEvent.click(await screen.findByRole('button', { name: 'Remove' }));
    await screen.findByText('local home');
    expect(fake.calls.some((c) => c.method === 'DELETE' && c.path === '/api/peers/b1')).toBe(true);
  });

  it('/s/mini-02/settings is not found', async () => {
    mount('/s/mini-02/settings');
    await screen.findByRole('heading', { name: 'Page not found' });
  });

  it('an unknown server is not found', async () => {
    mount('/s/nope');
    await screen.findByRole('heading', { name: 'Page not found' });
  });
});
