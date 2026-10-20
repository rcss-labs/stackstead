import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { startPeers } from '$lib/servers/peers';
import { api } from '$lib/api/client';
import type { FleetServer, FleetStack, FleetStacks, PeerView, StackSummary } from '$lib/api/types';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import AllStacks from './AllStacks';

const summary = (name: string, over: Partial<StackSummary> = {}): StackSummary => ({
  name,
  path: `/srv/stacks/${name}/compose.yaml`,
  project: name,
  origin: 'managed',
  status: 'running',
  serviceCount: 2,
  running: 2,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  ...over,
});

const local: FleetServer = { id: 'a0', name: 'here', slug: 'here', local: true, status: 'reachable', error: '', lastSeenAt: null };
const mini: FleetServer = { id: 'b1', name: 'mini-02', slug: 'mini-02', local: false, status: 'reachable', error: '', lastSeenAt: null };
const down: FleetServer = { id: 'c2', name: 'nas', slug: 'nas', local: false, status: 'unreachable', error: 'timeout', lastSeenAt: null };
const old: FleetServer = { id: 'd3', name: 'attic', slug: 'attic', local: false, status: 'incompatible', error: '', lastSeenAt: null };

const peerView = (s: FleetServer, version: string): PeerView => ({
  id: s.id,
  name: s.name,
  slug: s.slug,
  url: `https://${s.slug}:8080`,
  version,
  status: s.status as PeerView['status'],
  stackCount: 0,
  lastSeenAt: null,
  lastError: '',
  compatible: s.status !== 'incompatible',
  plaintext: false,
});

const fleetStack = (server: string, name: string): FleetStack => ({ ...summary(name), server });

function fleet(over: Partial<FleetStacks> = {}): FleetStacks {
  return {
    servers: [local, mini],
    stacks: [fleetStack('a0', 'web'), fleetStack('b1', 'pihole')],
    events: [{ time: '2026-10-20T10:00:00Z', action: 'start', container: 'pihole-1', server: 'b1' }],
    ...over,
  } as FleetStacks;
}

let fake: FakeFetch;
let stream: FakeStream;
let stopPeers: () => void;

function Where() {
  const { pathname, search } = useLocation();
  return <p data-testid="location">{pathname + search}</p>;
}

function mount(data: FleetStacks, extra: Record<string, { status?: number; body?: unknown }> = {}) {
  fake = installFakeFetch({
    'GET /api/fleet/stacks': { body: data },
    'GET /api/peers': { body: [peerView(mini, '1.5'), peerView(old, '1.3')] },
    'GET /api/peers/self': { body: { instanceId: 'x', name: 'here', slug: 'here', advertisedUrl: '', suggestedUrl: '', version: '1.5' } },
    ...extra,
  });
  stream = installFakeStream({});
  stopPeers = startPeers({ list: api.peers });
  render(
    <MemoryRouter initialEntries={['/all']}>
      <Where />
      <Routes>
        <Route path="/all" element={<AllStacks />} />
        <Route path="*" element={null} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  stopPeers();
  fake.restore();
  stream.restore();
});

describe('All stacks', () => {
  beforeEach(() => undefined);

  it("lists both servers' stacks with a Server column", async () => {
    mount(fleet());
    const web = await screen.findByRole('link', { name: 'web' });
    expect(web.getAttribute('href')).toBe('/stacks/web');
    expect(screen.getByRole('link', { name: 'pihole' }).getAttribute('href')).toBe('/s/mini-02/stacks/pihole');
    expect(screen.getByRole('columnheader', { name: 'Server' })).toBeTruthy();
    const piholeRow = screen.getByRole('link', { name: 'pihole' }).closest('[role="row"]') as HTMLElement;
    expect(within(piholeRow).getByText('mini-02')).toBeTruthy();
    // The status panel's towers span both servers.
    expect(screen.getByTitle('web · running').getAttribute('type')).toBe('button');
    expect(screen.getByTitle('pihole · running')).toBeTruthy();
  });

  it('tags side-column events with the server name', async () => {
    mount(fleet());
    const activity = (await screen.findByRole('heading', { name: /Activity/ })).closest('section') as HTMLElement;
    expect(within(activity).getByText('mini-02')).toBeTruthy();
    expect(within(activity).getByRole('link', { name: 'pihole-1' }).getAttribute('href')).toBe('/s/mini-02/containers/pihole-1');
  });

  it("filter chip shows only that server's stacks", async () => {
    mount(fleet());
    await screen.findByRole('link', { name: 'web' });
    const all = screen.getByRole('button', { name: /^All/ });
    expect(all.getAttribute('aria-pressed')).toBe('true');

    await userEvent.click(screen.getByRole('button', { name: /^mini-02/ }));
    expect(screen.getByRole('button', { name: /^mini-02/ }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.queryByRole('link', { name: 'web' })).toBeNull();
    expect(screen.getByRole('link', { name: 'pihole' })).toBeTruthy();

    await userEvent.click(screen.getByRole('button', { name: /^All/ }));
    expect(screen.getByRole('link', { name: 'web' })).toBeTruthy();
  });

  it('unreachable server shows a banner and omits its stacks', async () => {
    mount(fleet({ servers: [local, mini, down], stacks: [fleetStack('a0', 'web'), fleetStack('c2', 'stale')] }));
    expect(await screen.findByText("Can't reach nas")).toBeTruthy();
    expect(screen.getByRole('link', { name: 'web' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'stale' })).toBeNull();
  });

  it('incompatible server shows the version banner', async () => {
    mount(fleet({ servers: [local, old], stacks: [fleetStack('a0', 'web')] }));
    expect(await screen.findByText('attic runs v1.3, this server v1.5. Update to match')).toBeTruthy();
  });

  it('row action on a remote stack posts to /api/servers/b1/api/stacks/pihole/actions/restart', async () => {
    mount(fleet(), {
      'POST /api/servers/b1/api/stacks/pihole/actions/restart': {
        status: 202,
        body: { id: 'op9', stack: 'pihole', action: 'restart', state: 'running', startedAt: '2026-10-20T10:00:00Z', lines: [] },
      },
    });
    await userEvent.click(await screen.findByRole('button', { name: 'Restart pihole' }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === 'POST' && c.path === '/api/servers/b1/api/stacks/pihole/actions/restart')).toBe(true));
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/s/mini-02/stacks/pihole?tab=operations&op=op9'));
  });

  it('opens no event stream of its own', async () => {
    mount(fleet());
    await screen.findByRole('link', { name: 'web' });
    expect(stream.requests).toHaveLength(0);
  });
});
