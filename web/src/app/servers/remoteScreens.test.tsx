import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { startPeers } from '$lib/servers/peers';
import { api } from '$lib/api/client';
import type { PeerView } from '$lib/api/types';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import RemoteServer from './RemoteServer';
import { row } from '../containers/containersTestKit';

const apiContainer = row('api');

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
  services: [{ name: 'app', config: { image: 'nginx:1', limits: {} }, readOnly: [], containers: [] }],
};

let fake: FakeFetch;
let stream: FakeStream;
let stopPeers: () => void;

function Where() {
  const { pathname, search } = useLocation();
  return <p data-testid="location">{pathname + search}</p>;
}

function mount(path: string) {
  stopPeers = startPeers({ list: api.peers });
  render(
    <MemoryRouter initialEntries={[path]}>
      <Where />
      <Routes>
        <Route path="/s/:server/*" element={<RemoteServer />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  fake = installFakeFetch({
    'GET /api/peers': { body: [peer] },
    'GET /api/peers/self': { body: { instanceId: 'x', name: 'here', slug: 'here', advertisedUrl: '', suggestedUrl: '', version: '1.5' } },
    [`GET ${BASE}/stacks`]: { body: [summary] },
    [`GET ${BASE}/stacks/web`]: { body: detail },
    [`GET ${BASE}/stacks/web/compose`]: { body: { content: 'services: {}\n', hash: 'c1' } },
    [`GET ${BASE}/stacks/web/operations`]: { body: [] },
    [`GET ${BASE}/stacks/web/metrics`]: { body: { points: [] } },
    [`GET ${BASE}/operations/op1`]: { body: { id: 'op1', stack: 'web', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z', lines: [] } },
    [`GET ${BASE}/containers`]: { body: [apiContainer] },
    [`GET ${BASE}/stacks/web/health`]: { body: { from: 0, to: 3600, services: [] } },
    [`GET ${BASE}/status`]: { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    [`GET ${BASE}/host`]: { body: {} },
    [`GET ${BASE}/operations?state=running`]: { body: [] },
    [`GET ${BASE}/fs`]: { body: [{ name: '/srv/app', isDir: true, size: 0 }] },
    [`GET ${BASE}/networks`]: { body: [] },
    [`POST ${BASE}/stacks`]: { status: 201, body: summary },
    [`POST ${BASE}/stacks/web/actions/up`]: { status: 202, body: { id: 'op1', stack: 'web', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' } },
    'GET /api/stacks': { body: [] },
    'GET /api/fs': { body: [] },
    'GET /api/operations?state=running': { body: [] },
  });
  stream = installFakeStream({
    [`${BASE}/events`]: [': ok\n\n'],
    [`${BASE}/stacks/web/logs`]: [': ok\n\n'],
    [`${BASE}/operations/op1/stream`]: [],
    [`${BASE}/stacks/web/metrics/live`]: [],
    [`${BASE}/containers/${apiContainer.id}/logs`]: [],
  });
  stopPeers = () => {};
});

afterEach(() => {
  cleanup();
  stopPeers();
  stream.restore();
  fake.restore();
});

/** Every API request and stream stays on the viewed peer. */
function expectNothingLocal() {
  const local = fake.calls.filter((c) => c.path.startsWith('/api/') && !c.path.startsWith('/api/peers') && !c.path.startsWith(`${BASE}/`));
  expect(local.map((c) => `${c.method} ${c.path}`)).toEqual([]);
  const localStreams = stream.requests.filter((r) => r.path.startsWith('/api/') && !r.path.startsWith(`${BASE}/`));
  expect(localStreams.map((r) => r.path)).toEqual([]);
}

/** Every in-page link stays under the peer's /s/ prefix. */
function expectLinksOnPeer() {
  const hrefs = Array.from(document.querySelectorAll('a[href]')).map((a) => a.getAttribute('href') ?? '');
  const local = hrefs.filter((h) => h.startsWith('/') && !h.startsWith('/s/mini-02'));
  expect(local).toEqual([]);
}

const requested = (method: string, path: string) => fake.calls.some((c) => c.method === method && c.path === path);

describe('screens on a connected server', () => {
  it('Overview loads from the peer and links under its prefix', async () => {
    mount('/s/mini-02');
    await screen.findAllByText('web');
    expect(requested('GET', `${BASE}/stacks`)).toBe(true);
    expectLinksOnPeer();
    expectNothingLocal();
  });

  it('stack detail loads the stack from the peer', async () => {
    mount('/s/mini-02/stacks/web');
    await waitFor(() => expect(requested('GET', `${BASE}/stacks/web`)).toBe(true));
    expectLinksOnPeer();
    expectNothingLocal();
  });

  it('stack detail logs stream from the peer', async () => {
    mount('/s/mini-02/stacks/web?tab=logs');
    await waitFor(() => expect(stream.requests.some((r) => r.path.startsWith(`${BASE}/stacks/web/logs`))).toBe(true));
    expectNothingLocal();
  });

  it('compose tab loads the compose file from the peer', async () => {
    mount('/s/mini-02/stacks/web?tab=compose');
    await waitFor(() => expect(requested('GET', `${BASE}/stacks/web/compose`)).toBe(true));
    expectNothingLocal();
  });

  it('new stack posts to the peer and lands on its detail page', async () => {
    mount('/s/mini-02/stacks/new');
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Stack name'), 'web');
    await user.type(screen.getByLabelText('Image'), 'nginx:1.27');
    await user.click(screen.getByRole('button', { name: 'Create stack' }));
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/s/mini-02/stacks/web?tab=operations&op=op1'));
    expect(requested('POST', `${BASE}/stacks`)).toBe(true);
    expect(requested('GET', `${BASE}/containers`)).toBe(true);
  });

  it('operation output streams from the peer', async () => {
    mount('/s/mini-02/stacks/web?tab=operations&op=op1');
    await waitFor(() => expect(stream.requests.some((r) => r.path.startsWith(`${BASE}/operations/op1/stream`))).toBe(true));
    expectNothingLocal();
  });

  it('live metrics stream from the peer', async () => {
    mount('/s/mini-02/stacks/web?service=app&tab=metrics');
    await waitFor(() => expect(stream.requests.some((r) => r.path.startsWith(`${BASE}/stacks/web/metrics/live`))).toBe(true));
    expectNothingLocal();
  });

  it('container log peek streams from the peer', async () => {
    mount('/s/mini-02/containers');
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: `Peek logs for ${apiContainer.name}` }));
    await waitFor(() => expect(stream.requests.some((r) => r.path.startsWith(`${BASE}/containers/${apiContainer.id}/logs`))).toBe(true));
    expectNothingLocal();
  });

  it('import browses the peer file system', async () => {
    mount('/s/mini-02/stacks/import');
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Browse for compose file' }));
    await waitFor(() => expect(requested('GET', `${BASE}/fs`)).toBe(true));
    expectNothingLocal();
  });
});
