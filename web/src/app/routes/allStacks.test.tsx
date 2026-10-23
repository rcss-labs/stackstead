import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { api } from '$lib/api/client';
import { startHost } from '$lib/stores/host';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import type { FleetServer, FleetStack, FleetStacks, HostSnapshot, MetricsSummary, StackSummary } from '$lib/api/types';
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
  libraryUpdates: 0,
  ...over,
});

const local: FleetServer = { id: 'a0', name: 'here', slug: 'here', local: true, status: 'reachable', error: '', lastSeenAt: null, version: '1.5' };
const mini: FleetServer = { id: 'b1', name: 'mini-02', slug: 'mini-02', local: false, status: 'reachable', error: '', lastSeenAt: null, version: '1.5' };
const down: FleetServer = { id: 'c2', name: 'nas', slug: 'nas', local: false, status: 'unreachable', error: 'timeout', lastSeenAt: null, version: '' };
const old: FleetServer = { id: 'd3', name: 'attic', slug: 'attic', local: false, status: 'incompatible', error: '', lastSeenAt: null, version: '1.3' };

const fleetStack = (server: string, name: string): FleetStack => ({ ...summary(name), server });

function fleet(over: Partial<FleetStacks> = {}): FleetStacks {
  return {
    servers: [local, mini],
    stacks: [fleetStack('a0', 'web'), fleetStack('b1', 'pihole')],
    events: [{ time: '2026-10-20T10:00:00Z', action: 'start', container: 'pihole-1', server: 'b1' }],
    ...over,
  } as FleetStacks;
}

const usage = (cpuNow: number | null, memNow: number | null) => ({ cpu: [], mem: [], cpuNow, memNow, netRxBytesPerSec: null, netTxBytesPerSec: null });
const metrics = (stacks: MetricsSummary['stacks']): MetricsSummary => ({ window: '15m', step: 15, from: '', to: '', stacks, top: { cpu: [], mem: [] } });
const GB = 1024 ** 3;
const hostOf = (cpu: HostSnapshot['cpu'], memUsed: number, memTotal: number, diskFree: number, diskTotal: number): HostSnapshot => ({
  hostname: 'h',
  os: 'linux',
  source: 'proc',
  sampledAt: '2026-10-20T10:00:00Z',
  containers: null,
  cpu,
  memory: { used: memUsed, total: memTotal, swapUsed: 0, swapTotal: 0 },
  disk: { path: '/', free: diskFree, total: diskTotal },
  network: null,
  diskIO: null,
  volumes: null,
  uptime: null,
  restarts24h: null,
});

let fake: FakeFetch;
let stopHost: (() => void) | undefined;
let stream: FakeStream;
let view: ReturnType<typeof render>;

function Where() {
  const { pathname, search } = useLocation();
  return <p data-testid="location">{pathname + search}</p>;
}

function mount(data: FleetStacks, extra: Record<string, { status?: number; body?: unknown }> = {}) {
  fake = installFakeFetch({
    'GET /api/fleet/stacks': { body: data },
    'GET /api/servers/b1/api/operations': { body: [] },
    ...extra,
  });
  stream = installFakeStream({});
  view = render(
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
  stopHost?.();
  stopHost = undefined;
  cleanup();
  toasts.reset();
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

  it('row action on a remote stack posts to /api/servers/b1/api/stacks/pihole/actions/restart and toasts', async () => {
    mount(fleet(), {
      'POST /api/servers/b1/api/stacks/pihole/actions/restart': {
        status: 202,
        body: { id: 'op9', stack: 'pihole', action: 'restart', state: 'running', startedAt: '2026-10-20T10:00:00Z', lines: [] },
      },
      'GET /api/servers/b1/api/operations/op9': { body: { id: 'op9', stack: 'pihole', action: 'restart', state: 'running', startedAt: '2026-10-20T10:00:00Z', lines: [] } },
    });
    await userEvent.click(await screen.findByRole('button', { name: 'Restart pihole' }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === 'POST' && c.path === '/api/servers/b1/api/stacks/pihole/actions/restart')).toBe(true));
    await waitFor(() => expect(get(toasts)).toHaveLength(1));
    expect(get(toasts)[0].link?.href).toBe('/s/mini-02/stacks/pihole?tab=operations&op=op9');
    expect(screen.getByTestId('location').textContent).toBe('/all');
  });

  it('settles a pending peer toast when the screen unmounts', async () => {
    mount(fleet(), {
      'POST /api/servers/b1/api/stacks/pihole/actions/restart': {
        status: 202,
        body: { id: 'op9', stack: 'pihole', action: 'restart', state: 'running', startedAt: '2026-10-20T10:00:00Z', lines: [] },
      },
      'GET /api/servers/b1/api/operations/op9': { body: { id: 'op9', stack: 'pihole', action: 'restart', state: 'running', startedAt: '2026-10-20T10:00:00Z', lines: [] } },
    });
    await userEvent.click(await screen.findByRole('button', { name: 'Restart pihole' }));
    await waitFor(() => expect(get(toasts)[0]?.pending).toBe(true));
    view.unmount();
    expect(get(toasts)[0]).toMatchObject({ message: 'Still running on mini-02', pending: undefined });
  });

  it('keeps same-named stacks of two servers apart', async () => {
    mount(fleet({ stacks: [fleetStack('a0', 'web'), fleetStack('b1', 'web')], events: [] }), {
      'POST /api/servers/b1/api/stacks/web/actions/restart': {
        status: 202,
        body: { id: 'op1', stack: 'web', action: 'restart', state: 'running', startedAt: '2026-10-20T10:00:00Z', lines: [] },
      },
      'GET /api/servers/b1/api/operations': { body: [{ id: 'op1', stack: 'web', action: 'restart', state: 'running', startedAt: '2026-10-20T10:00:00Z', lines: [] }] },
      'GET /api/servers/b1/api/operations/op1': { body: { id: 'op1', stack: 'web', action: 'restart', state: 'running', startedAt: '2026-10-20T10:00:00Z', lines: [] } },
    });
    const links = await screen.findAllByRole('link', { name: 'web' });
    expect(links.map((l) => l.getAttribute('href'))).toEqual(['/stacks/web', '/s/mini-02/stacks/web']);

    await userEvent.click(screen.getAllByRole('button', { name: 'Restart web' })[1]);
    await waitFor(() => expect(get(toasts)).toHaveLength(1));
    // Only the mini-02 row is busy.
    await waitFor(() => expect(screen.getAllByRole('button', { name: 'Restart web' })[1].getAttribute('aria-disabled')).toBe('true'));
    expect(screen.getAllByRole('button', { name: 'Restart web' })[0].getAttribute('aria-disabled')).toBeNull();

    await userEvent.click(screen.getByRole('button', { name: /^mini-02/ }));
    expect(screen.getAllByRole('link', { name: 'web' })).toHaveLength(1);
    expect(screen.getByRole('link', { name: 'web' }).getAttribute('href')).toBe('/s/mini-02/stacks/web');
  });

  it('shows only the banner when every answering server is empty', async () => {
    mount(fleet({ servers: [local, down], stacks: [], events: [] }));
    expect(await screen.findByText("Can't reach nas")).toBeTruthy();
    expect(screen.queryByText('No stacks yet')).toBeNull();
  });

  it('omits the version clause when the peer reports none', async () => {
    mount(fleet({ servers: [local, { ...old, version: '' }], stacks: [fleetStack('a0', 'web')] }));
    expect(await screen.findByText('attic runs a different version. Update to match')).toBeTruthy();
  });

  it('opens no event stream of its own', async () => {
    mount(fleet());
    await screen.findByRole('link', { name: 'web' });
    expect(stream.requests).toHaveLength(0);
  });

  it('shows each stack\'s own server\'s CPU and memory, even when two servers share a project name', async () => {
    mount(fleet({ stacks: [fleetStack('a0', 'shared'), fleetStack('b1', 'shared')] }), {
      'GET /api/metrics/summary': { body: metrics({ shared: usage(12.5, 100 * 1024 ** 2) }) },
      'GET /api/servers/b1/api/metrics/summary': { body: metrics({ shared: usage(34, 2 * GB) }) },
    });
    const rows = (await screen.findAllByRole('link', { name: 'shared' })).map((l) => l.closest('[role="row"]') as HTMLElement);
    const localRow = rows.find((r) => within(r).queryByText('here'))!;
    const peerRow = rows.find((r) => within(r).queryByText('mini-02'))!;
    await waitFor(() => expect(within(localRow).getByText('12.5%')).toBeTruthy());
    expect(within(localRow).getByText('100.0 MiB')).toBeTruthy();
    expect(within(peerRow).getByText('34.0%')).toBeTruthy();
    expect(within(peerRow).getByText('2.0 GiB')).toBeTruthy();
  });

  it('shows a dash for a server whose summary fails, and keeps the others', async () => {
    mount(fleet(), {
      'GET /api/metrics/summary': { body: metrics({ web: usage(7, GB) }) },
      'GET /api/servers/b1/api/metrics/summary': { status: 502, body: { error: 'peer down' } },
    });
    const web = (await screen.findByRole('link', { name: 'web' })).closest('[role="row"]') as HTMLElement;
    await waitFor(() => expect(within(web).getByText('7.0%')).toBeTruthy());
    const pihole = screen.getByRole('link', { name: 'pihole' }).closest('[role="row"]') as HTMLElement;
    expect(within(pihole).queryByText(/%$/)).toBeNull();
  });

  it('combines the host stats of every server that answers and names the ones left out', async () => {
    mount(fleet({ servers: [local, mini, down], stacks: [fleetStack('a0', 'web'), fleetStack('b1', 'pihole')] }), {
      'GET /api/host': { body: hostOf({ percent: 10, cores: 2 }, 2 * GB, 8 * GB, 60 * GB, 100 * GB) },
      'GET /api/servers/b1/api/host': { body: hostOf({ percent: 40, cores: 6 }, 4 * GB, 16 * GB, 100 * GB, 300 * GB) },
    });
    stopHost = startHost({ host: api.host });
    const cpu = await waitFor(() => {
      const cell = document.querySelector('[data-stat="cpu"]');
      expect(cell).toBeTruthy();
      return cell as HTMLElement;
    });
    expect(within(cpu).getByText('32.5%')).toBeTruthy();
    expect(within(cpu).getByText('8 cores')).toBeTruthy();
    const mem = document.querySelector('[data-stat="memory"]') as HTMLElement;
    expect(within(mem).getByText('6.0 GiB')).toBeTruthy();
    expect(within(mem).getByText('24.0 GiB')).toBeTruthy();
    expect(screen.getByText(/nas not included: unreachable/)).toBeTruthy();
    expect(screen.getByText(/Host stats combined from 2 servers/)).toBeTruthy();
  });

  it('leaves a server whose host request fails out of the combined stats with a note', async () => {
    mount(fleet(), {
      'GET /api/host': { body: hostOf({ percent: 10, cores: 2 }, 2 * GB, 8 * GB, 60 * GB, 100 * GB) },
      'GET /api/servers/b1/api/host': { status: 502, body: { error: 'peer down' } },
    });
    stopHost = startHost({ host: api.host });
    expect(await screen.findByText(/mini-02 not included: unavailable \(peer down\)/)).toBeTruthy();
    const cpu = document.querySelector('[data-stat="cpu"]') as HTMLElement;
    expect(within(cpu).getByText('10.0%')).toBeTruthy();
  });
});
