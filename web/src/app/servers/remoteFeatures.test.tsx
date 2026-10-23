import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';
import { startPeers } from '$lib/servers/peers';
import { api } from '$lib/api/client';
import type { PeerView } from '$lib/api/types';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import RemoteServer from './RemoteServer';

const BASE = '/api/servers/b1/api';

const peer: PeerView = {
  id: 'b1',
  name: 'mini-02',
  slug: 'mini-02',
  url: 'https://mini-02:8080',
  version: '1.5',
  status: 'reachable',
  stackCount: 1,
  lastSeenAt: new Date().toISOString(),
  lastError: '',
  compatible: true,
  plaintext: false,
  features: [],
};

const summary = {
  name: 'web',
  path: '/srv/stacks/web/compose.yaml',
  project: 'web',
  origin: 'managed',
  status: 'running',
  serviceCount: 1,
  running: 1,
  health: { healthy: 1, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
};

const detail = {
  ...summary,
  hash: 'h1',
  services: [{ name: 'app', config: { build: { context: '/srv/app' }, limits: {} }, readOnly: [], containers: [] }],
};

let fake: FakeFetch;
let stream: FakeStream;
let stopPeers: () => void = () => {};

function mount(path: string, features: string[] | undefined) {
  const shown = { ...peer, ...(features ? { features } : {}) } as PeerView;
  if (!features) delete (shown as Partial<PeerView>).features; // an older gateway-visible peer view has no list at all
  fake = installFakeFetch({
    'GET /api/peers': { body: [shown] },
    'GET /api/peers/self': { body: { instanceId: 'x', name: 'here', slug: 'here', advertisedUrl: '', suggestedUrl: '', version: '1.5' } },
    [`GET ${BASE}/stacks`]: { body: [summary] },
    [`GET ${BASE}/stacks/web`]: { body: detail },
    [`GET ${BASE}/stacks/web/operations`]: { body: [] },
    [`GET ${BASE}/stacks/web/metrics`]: { body: { points: [] } },
    [`GET ${BASE}/stacks/web/health`]: { body: { from: 0, to: 3600, services: [] } },
    [`GET ${BASE}/stacks/web/services/app/dockerfile`]: { body: { editable: false, reason: 'not_found' } },
    [`GET ${BASE}/containers`]: { body: [] },
    [`GET ${BASE}/status`]: { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    [`GET ${BASE}/host`]: { body: {} },
    [`GET ${BASE}/operations?state=running`]: { body: [] },
    [`GET ${BASE}/networks`]: { body: [] },
    [`GET ${BASE}/fs`]: { body: [] },
  });
  stream = installFakeStream({ [`${BASE}/events`]: [': ok\n\n'], [`${BASE}/stacks/web/metrics/live`]: [] });
  stopPeers = startPeers({ list: api.peers });
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/s/:server/*" element={<RemoteServer />} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  stopPeers();
  stream.restore();
  fake.restore();
});

const BOTH = ['yaml-create', 'managed-dockerfile'];

describe('features of a connected server', () => {
  it('hides YAML mode and the Dockerfile source for a peer that advertises nothing', async () => {
    mount('/s/mini-02/stacks/new', undefined);
    await screen.findByLabelText('Stack name');
    expect(screen.queryByRole('radiogroup', { name: 'Editor mode' })).toBeNull();
    const source = screen.getByRole('radiogroup', { name: 'Source type' });
    expect(within(source).getByRole('radio', { name: /Existing build/ })).toBeTruthy();
    expect(within(source).queryByRole('radio', { name: 'Write a Dockerfile' })).toBeNull();
  });

  it('shows YAML mode and the Dockerfile source for a peer that advertises them', async () => {
    mount('/s/mini-02/stacks/new', BOTH);
    await screen.findByLabelText('Stack name');
    expect(screen.getByRole('radiogroup', { name: 'Editor mode' })).toBeTruthy();
    expect(screen.getByRole('radio', { name: 'Write a Dockerfile' })).toBeTruthy();
  });

  it('shows only the features the peer advertises', async () => {
    mount('/s/mini-02/stacks/new', ['yaml-create']);
    await screen.findByLabelText('Stack name');
    expect(screen.getByRole('radiogroup', { name: 'Editor mode' })).toBeTruthy();
    expect(screen.queryByRole('radio', { name: 'Write a Dockerfile' })).toBeNull();
  });

  it('has no Dockerfile tab and sends no Dockerfile request for a peer without the feature', async () => {
    mount('/s/mini-02/stacks/web?service=app&tab=dockerfile', undefined);
    await screen.findByRole('tab', { name: 'Overview' });
    expect(screen.queryByRole('tab', { name: 'Dockerfile' })).toBeNull();
    expect(fake.calls.some((c) => c.path.endsWith('/dockerfile'))).toBe(false);
  });

  it('shows the Dockerfile tab for a peer that advertises it', async () => {
    mount('/s/mini-02/stacks/web?service=app', BOTH);
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Dockerfile' })).toBeTruthy());
  });
});
