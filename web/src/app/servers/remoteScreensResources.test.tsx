import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { api } from '$lib/api/client';
import type { PeerView } from '$lib/api/types';
import { startPeers } from '$lib/servers/peers';
import { volume } from '$lib/storage/fixtures';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import { detail, row as containerRow } from '../containers/containersTestKit';
import { row as networkRow } from '../networks/networksTestKit';
import RemoteServer from './RemoteServer';

const REMOTE = '/api/servers/b1/api';
const webRow = containerRow('web-1');

const peer = (over: Partial<PeerView> = {}): PeerView => ({
  id: 'b1',
  name: 'mini-02',
  slug: 'mini-02',
  url: 'https://mini-02:8080',
  version: '1.4',
  status: 'reachable',
  stackCount: 0,
  lastSeenAt: new Date(Date.now() - 5 * 60_000).toISOString(),
  lastError: '',
  compatible: true,
  plaintext: false,
  ...over,
});

const cleanupPlan = {
  id: 'p_1',
  kind: 'volumes',
  createdAt: '2026-10-20T10:00:00Z',
  expiresAt: new Date(Date.now() + 300_000).toISOString(),
  items: [{ kind: 'volume', id: 'data', ref: 'data', bytes: 1024 }],
  protected: [],
  warnings: [],
  reclaim: { min: 1024, max: 1024, exact: true },
  confirmPhrase: 'delete 1 volume',
};

let fake: FakeFetch;
let stream: FakeStream;
let stopPeers: () => void;

function mount(path: string) {
  stopPeers = startPeers({ list: api.peers });
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/s/:server/*" element={<RemoteServer />} />
      </Routes>
    </MemoryRouter>,
  );
}

const hitRemote = (method: string, path: string) => fake.calls.some((c) => c.method === method && c.path === `${REMOTE}${path}`);
const hitLocal = (path: string) => fake.calls.some((c) => c.path === path);

beforeEach(() => {
  fake = installFakeFetch({
    'GET /api/peers': () => ({ body: [peer()] }),
    'GET /api/peers/self': { body: { instanceId: 'x', name: 'here', slug: 'here', advertisedUrl: '', suggestedUrl: '', version: '1.5' } },
    [`GET ${REMOTE}/status`]: { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    [`GET ${REMOTE}/stacks`]: { body: [] },
    [`GET ${REMOTE}/host`]: { body: {} },
    [`GET ${REMOTE}/operations?state=running`]: { body: [] },
    [`GET ${REMOTE}/containers`]: { body: [webRow] },
    [`GET ${REMOTE}/containers/${webRow.id}`]: { body: detail({ name: 'web-1', id: webRow.id }) },
    [`GET ${REMOTE}/networks`]: { body: [networkRow('lan')] },
    [`POST ${REMOTE}/networks`]: { status: 201, body: networkRow('new-net') },
    [`GET ${REMOTE}/storage`]: { body: { images: { count: 0, size: null, reclaimable: null, sizesAt: null, refreshing: false, error: '' }, containers: { count: 0, size: null, reclaimable: null, sizesAt: null, refreshing: false, error: '' }, volumes: { count: 0, size: null, reclaimable: null, sizesAt: null, refreshing: false, error: '' }, buildCache: { count: 0, size: null, reclaimable: null, sizesAt: null, refreshing: false, error: '' }, topContainers: [], declarationsUnknown: [] } },
    [`GET ${REMOTE}/storage/volumes/data`]: { body: { ...volume({ name: 'data', protection: null, declaredBy: [], status: 'unused' }), mountpoint: '/m' } },
    [`POST ${REMOTE}/storage/plans`]: { body: cleanupPlan },
    [`POST ${REMOTE}/storage/plans/p_1/confirm`]: { body: { id: 'op1', stack: '', action: 'storage-cleanup', state: 'running', startedAt: '2026-10-20T10:00:00Z' } },
  });
  stream = installFakeStream({ [`${REMOTE}/events`]: [': ok\n\n'] });
  stopPeers = () => {};
});

afterEach(() => {
  cleanup();
  stopPeers();
  stream.restore();
  fake.restore();
});

describe('connected server: containers, networks and storage', () => {
  it('containers list loads from the peer and links rows inside it', async () => {
    mount('/s/mini-02/containers');
    const link = await screen.findByRole('link', { name: 'web-1' });
    expect(link.getAttribute('href')).toBe('/s/mini-02/containers/web-1');
    expect(hitRemote('GET', '/containers')).toBe(true);
    expect(hitLocal('/api/containers')).toBe(false);
  });

  it('container detail loads the peer container', async () => {
    mount('/s/mini-02/containers/web-1');
    await waitFor(() => expect(hitRemote('GET', `/containers/${webRow.id}`)).toBe(true));
    expect(hitLocal(`/api/containers/${webRow.id}`)).toBe(false);
  });

  it('networks list loads from the peer', async () => {
    mount('/s/mini-02/networks');
    await screen.findByText(/^1 network · /);
    expect(hitRemote('GET', '/networks')).toBe(true);
    expect(hitLocal('/api/networks')).toBe(false);
  });

  it('creating a network posts to the peer', async () => {
    const user = userEvent.setup();
    mount('/s/mini-02/networks');
    await user.click(await screen.findByRole('button', { name: 'Create network' }));
    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByLabelText('Name'), 'new-net');
    await user.click(within(dialog).getByRole('button', { name: 'Create network' }));
    await waitFor(() => expect(hitRemote('POST', '/networks')).toBe(true));
    expect(hitLocal('/api/networks')).toBe(false);
  });

  it('storage overview loads from the peer', async () => {
    mount('/s/mini-02/storage');
    await waitFor(() => expect(hitRemote('GET', '/storage')).toBe(true));
    expect(hitLocal('/api/storage')).toBe(false);
  });

  it('storage refreshes from the peer change feed, not this server', async () => {
    mount('/s/mini-02/storage');
    await waitFor(() => expect(hitRemote('GET', '/storage')).toBe(true));
    const loads = () => fake.calls.filter((c) => c.method === 'GET' && c.path === `${REMOTE}/storage`).length;
    const before = loads();
    stream.push(`${REMOTE}/events`, 'event: change\ndata: {"project":"media","seq":1}\n\n');
    await waitFor(() => expect(loads()).toBeGreaterThan(before), { timeout: 3000 });
  });

  it('volume detail loads from the peer and its cleanup confirm posts to the peer', async () => {
    const user = userEvent.setup();
    mount('/s/mini-02/storage/volumes/data');
    await user.click(await screen.findByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    await user.type(await within(dialog).findByLabelText(/Type delete 1 volume to confirm/), 'delete 1 volume');
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(hitRemote('POST', '/storage/plans/p_1/confirm')).toBe(true));
    expect(hitLocal('/api/storage/plans/p_1/confirm')).toBe(false);
  });
});
