import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { get, writable } from '$lib/store';
import type { Change, HostSnapshot, Operation, StackSummary } from '$lib/api/types';
import { startHost } from '$lib/stores/host';
import * as liveModule from '$lib/stores/live';
import { startRunningOps } from '$lib/stores/runningOps';
import { operationsRefresh, startStacks, stacks } from '$lib/stores/stacks';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import Overview from '../routes/Overview';

const stack = (name: string, over: Partial<StackSummary> = {}): StackSummary => ({
  name,
  path: `/srv/stacks/${name}/compose.yaml`,
  project: `proj-${name}`,
  origin: 'managed',
  status: 'running',
  serviceCount: 3,
  running: 3,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  ...over,
});

const hostSnapshot = (): HostSnapshot => ({
  hostname: 'box',
  os: 'linux',
  source: 'proc',
  sampledAt: new Date().toISOString(),
  containers: { total: 19, running: 17, exited: 2, bad: 0 },
  cpu: { percent: 18, cores: 8 },
  memory: { used: 4 * 1024 ** 3, total: 16 * 1024 ** 3, swapUsed: 0, swapTotal: 0 },
  disk: { path: '/srv/stacks', free: 400 * 1024 ** 3, total: 1000 * 1024 ** 3 },
  network: { rxBytesPerSec: 2048, txBytesPerSec: 1024, interface: 'eth0' },
  diskIO: null,
  volumes: null,
  uptime: { seconds: 3600, load1: 1, load5: 1, load15: 1 },
  restarts24h: null,
});

const setEvents = (v: unknown[]) => (liveModule as unknown as { __setEvents: (v: unknown) => void }).__setEvents(v);

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const store = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const eventStore = writable<unknown[]>([]);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    events: { subscribe: eventStore.subscribe },
    refreshStatus: vi.fn(),
    __setEvents: eventStore.set,
  };
});

let fake: FakeFetch;
let stops: (() => void)[] = [];
let stacksRes: FakeResponse;
/** Holds GET /api/stacks until released, to observe in-flight states. */
let gate: Promise<void> = Promise.resolve();

function install(items: StackSummary[] | FakeResponse, ops: Operation[] = []) {
  stacksRes = Array.isArray(items) ? { body: items } : items;
  fake = installFakeFetch({
    'GET /api/stacks': async () => {
      await gate;
      return stacksRes;
    },
    'GET /api/operations?state=running': { body: ops },
    'GET /api/host': { body: hostSnapshot() },
    'GET /api/alerts': { body: [] },
    'GET /api/metrics/summary': { body: { window: '15m', step: 15, from: '', to: '', stacks: {}, top: { cpu: [], mem: [] } } },
    'GET /api/storage': { body: { reclaimable: 0 } },
  });
  const changes = writable<(Change & { resync?: true }) | null>(null);
  const list = () =>
    fetch('/api/stacks').then(async (r) => {
      if (!r.ok) throw new Error(((await r.json()) as { error: string }).error);
      return r.json();
    });
  stops.push(startStacks({ list, changes }));
  stops.push(startHost({ host: () => fetch('/api/host').then((r) => r.json()) }));
  stops.push(startRunningOps({ list: () => fetch('/api/operations?state=running').then((r) => r.json()), changes, refresh: operationsRefresh }));
}

const mount = () =>
  render(
    <MemoryRouter>
      <Overview />
    </MemoryRouter>,
  );

const count = (path: string) => fake.calls.filter((c) => c.path === path).length;

afterEach(async () => {
  cleanup();
  vi.useRealTimers();
  stops.forEach((stop) => stop());
  stops = [];
  gate = Promise.resolve();
  setEvents([]);
  fake?.restore();
  await new Promise((r) => setTimeout(r, 5));
});

describe('Overview', () => {
  it('shows a loading spinner, then the status panel with the sentence as the h1', async () => {
    install([stack('alpha'), stack('beta')]);
    mount();
    expect(screen.getByText('Loading')).toBeTruthy();
    expect((await screen.findByRole('heading', { level: 1 })).textContent).toBe('All stacks are running');
    expect(screen.queryByText('Loading')).toBeNull();
  });

  it('first-load failure shows only the banner and a Retry that refetches', async () => {
    install({ status: 500, body: { error: 'boom' } });
    mount();
    expect(await screen.findByText('Could not load stacks')).toBeTruthy();
    expect(screen.getByText('boom')).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull();
    stacksRes = { body: [stack('alpha')] };
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect((await screen.findByRole('heading', { level: 1 })).textContent).toBe('All stacks are running');
    expect(screen.queryByText('Could not load stacks')).toBeNull();
  });

  it('shows a busy Retry while the refetch is in flight', async () => {
    install({ status: 500, body: { error: 'boom' } });
    mount();
    await screen.findByText('Could not load stacks');
    let release: () => void = () => {};
    gate = new Promise<void>((r) => (release = r));
    stacksRes = { body: [stack('alpha')] };
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(screen.getByRole('button', { name: 'Retry' }).getAttribute('aria-busy')).toBe('true');
    release();
    await screen.findByRole('heading', { level: 1 });
  });

  it('notes degraded status when every stack is unknown', async () => {
    install([stack('a', { status: 'unknown' }), stack('b', { status: 'unknown' })]);
    mount();
    expect(await screen.findByText('Container status is unavailable, so every stack shows as unknown.')).toBeTruthy();
  });

  it('does not show the degraded note when some stacks have a known status', async () => {
    install([stack('a', { status: 'unknown' }), stack('b')]);
    mount();
    await screen.findByRole('heading', { level: 1 });
    expect(screen.queryByText(/status is unavailable/i)).toBeNull();
  });

  it('shows the empty state with create and import links', async () => {
    install([]);
    mount();
    expect(await screen.findByRole('heading', { name: 'Create your first stack' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getByRole('link', { name: 'Import' }).getAttribute('href')).toBe('/stacks/import');
    expect(screen.queryByRole('table')).toBeNull();
    expect(await screen.findByRole('link', { name: /containers/i })).toBeTruthy();
  });

  it('requests host, alerts, summary and storage once each at mount and stops them on unmount', async () => {
    install([stack('alpha')]);
    const view = mount();
    await screen.findByRole('heading', { level: 1 });
    await waitFor(() => expect(count('/api/storage')).toBe(1));
    const once = ['/api/host', '/api/alerts', '/api/metrics/summary', '/api/storage'].map(count);
    expect(once).toEqual([1, 1, 1, 1]);
    vi.useFakeTimers();
    view.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5);
    });
    stops.forEach((stop) => stop());
    stops = [];
    await act(async () => {
      await vi.advanceTimersByTimeAsync(70_000);
    });
    expect(['/api/alerts', '/api/metrics/summary', '/api/storage'].map(count)).toEqual([1, 1, 1]);
  });

  it('status sub-line shows containers up from the host and last event from the events store', async () => {
    install([stack('alpha')]);
    setEvents([{ action: 'start', container: 'alpha-web-1', project: 'proj-alpha', service: 'web', image: 'x', time: new Date().toISOString() }]);
    mount();
    expect(await screen.findByText(/17 of 19 containers up/)).toBeTruthy();
    expect(screen.getByText(/last event/)).toBeTruthy();
    expect(screen.getByText('just now')).toBeTruthy();
  });

  it('Import and New stack links are present when stacks exist', async () => {
    install([stack('alpha')]);
    mount();
    await screen.findByRole('heading', { level: 1 });
    expect(screen.getByRole('link', { name: 'Import' }).getAttribute('href')).toBe('/stacks/import');
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(get(stacks).items).toHaveLength(1);
  });
});
