import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { ServerContext, localScope } from '../servers/ServerContext';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { get, writable } from '$lib/store';
import type { Change, HostSnapshot, Operation, StackSummary } from '$lib/api/types';
import { startHost } from '$lib/stores/host';
import * as liveModule from '$lib/stores/live';
import { startRunningOps } from '$lib/stores/runningOps';
import { operationsRefresh, startStacks, stacks } from '$lib/stores/stacks';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import Overview from '../routes/Overview';

const noStorage = { count: 0, size: null, reclaimable: 0, sizesAt: null, refreshing: false, error: '' };

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
  libraryUpdates: 0,
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

const setLive = (patch: object) => (liveModule as unknown as { __setLive: (p: object) => void }).__setLive(patch);
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
    __setLive: (patch: object) => store.update((v) => ({ ...v, ...patch })),
  };
});

let fake: FakeFetch;
let stops: (() => void)[] = [];
let stacksRes: FakeResponse;
/** Holds GET /api/stacks until released, to observe in-flight states. */
let gate: Promise<void> = Promise.resolve();

function install(items: StackSummary[] | FakeResponse, ops: Operation[] = [], extra: Record<string, FakeResponse | (() => Promise<FakeResponse>)> = {}) {
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
    'GET /api/storage': { body: { images: noStorage, containers: noStorage, volumes: noStorage, buildCache: noStorage, topContainers: [], declarationsUnknown: [] } },
    ...extra,
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
  setLive({ docker: 'ok', connection: 'open' });
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

  it('first-run checklist with zero stacks and correct done states', async () => {
    install([], [], {
      'GET /api/backups/config': { body: { schedule: { enabled: false } } },
      'GET /api/settings/registries': { body: [{ id: 'r1', name: 'ghcr' }] },
    });
    mount();
    expect(await screen.findByRole('heading', { level: 1, name: /get your first stack in view/i })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getByRole('link', { name: 'Import stack' }).getAttribute('href')).toBe('/stacks/import');
    expect(screen.getByRole('link', { name: 'Open backups' }).getAttribute('href')).toBe('/backups');
    expect(screen.queryByRole('table')).toBeNull();
    const list = screen.getByRole('list', { name: 'Finish setting up' });
    // Registry connected: done once the lookup answers; schedule is off; no stack yet.
    await waitFor(() => expect(within(list).getByText('Connect a registry').closest('li')?.getAttribute('data-done')).toBe('true'));
    const done = (label: string) => within(list).getByText(label).closest('li')?.getAttribute('data-done');
    expect(done('Create admin account')).toBe('true');
    expect(done('Add your first stack')).toBe('false');
    expect(done('Schedule backups')).toBe('false');
    expect(screen.getByText('2 of 4 done')).toBeTruthy();
  });

  it('first-run leaves a step undecided while its lookup has not answered', async () => {
    install([], [], { 'GET /api/backups/config': { status: 500, body: { error: 'x' } }, 'GET /api/settings/registries': { status: 500, body: { error: 'x' } } });
    mount();
    const list = await screen.findByRole('list', { name: 'Finish setting up' });
    await new Promise((r) => setTimeout(r, 20));
    expect(within(list).getByText('Schedule backups').closest('li')?.getAttribute('data-done')).toBe('unknown');
    expect(screen.getByText('1 of 4 done')).toBeTruthy();
  });

  it('requests host, alerts, summary and storage once each at mount and stops them on unmount', async () => {
    // Fake timers before mount so the controller's intervals are the fake ones.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    install([stack('alpha')]);
    const view = mount();
    await screen.findByRole('heading', { level: 1 });
    await waitFor(() => expect(count('/api/storage')).toBe(1));
    const once = ['/api/host', '/api/alerts', '/api/metrics/summary', '/api/storage'].map(count);
    expect(once).toEqual([1, 1, 1, 1]);
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
    expect(await screen.findByText(/17 of 19 containers running/)).toBeTruthy();
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

  describe('headline and issues', () => {
    const alert = (over: object = {}) => ({ kind: 'exited', severity: 'danger', stack: 'alpha', service: 'web', message: 'web exited (1)', since: '2026-11-07T10:00:00Z', ...over });

    it('names the count of stacks needing attention', async () => {
      install([stack('alpha'), stack('beta'), stack('gamma')], [], { 'GET /api/alerts': { body: [alert(), alert({ stack: 'beta' })] } });
      mount();
      await waitFor(() => expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('2 stacks need attention'));
    });

    it('never says healthy while alerts are unloaded', async () => {
      let release: () => void = () => {};
      const held = new Promise<void>((r) => (release = r));
      install([stack('alpha')], [], { 'GET /api/alerts': async () => (await held, { body: [] }) });
      mount();
      expect((await screen.findByRole('heading', { level: 1 })).textContent).toBe('Health is not confirmed');
      release();
      await waitFor(() => expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('All stacks are running'));
    });

    it('never says healthy when alerts fail to load', async () => {
      install([stack('alpha')], [], { 'GET /api/alerts': { status: 500, body: { error: 'x' } } });
      mount();
      await screen.findByRole('heading', { level: 1 });
      await new Promise((r) => setTimeout(r, 30));
      expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Health is not confirmed');
    });

    it('Docker down headline with disabled actions and reasons', async () => {
      setLive({ docker: 'unavailable' });
      install([stack('alpha', { status: 'partial', running: 1 })], [], { 'GET /api/alerts': { body: [alert()] } });
      mount();
      expect((await screen.findByRole('heading', { level: 1 })).textContent).toBe('Docker is not responding');
      expect(screen.getByText(/Stackstead is still running/)).toBeTruthy();
      expect(screen.getByText('Cannot reach Docker')).toBeTruthy();
      expect(screen.queryByRole('region', { name: 'Needs attention' })).toBeNull();
      const restart = screen.getByRole('button', { name: 'Restart alpha' });
      expect(restart.getAttribute('aria-disabled')).toBe('true');
      expect(restart.getAttribute('title')).toBe('Docker is unreachable');
    });

    it('ranks danger before warning and offers Raise memory limit only with exit 137 and a limit', async () => {
      install([stack('paperless'), stack('gitea')], [], {
        'GET /api/alerts': {
          body: [
            alert({ kind: 'port_clash', severity: 'warning', stack: 'gitea', service: undefined, message: 'clash' }),
            alert({ kind: 'restart_loop', stack: 'paperless', service: 'tika', exitCode: 137, memLimit: 512 * 1024 ** 2, message: 'restarted 14 times' }),
          ],
        },
      });
      mount();
      const region = await screen.findByRole('region', { name: 'Needs attention' });
      const titles = within(region).getAllByRole('heading', { level: 3 }).map((h) => h.textContent);
      expect(titles).toEqual(['tika is restart looping', 'Port conflict']);
      const link = within(region).getByRole('link', { name: 'Raise memory limit' });
      expect(link.getAttribute('href')).toBe('/stacks/paperless?service=tika&edit=1');
      expect(within(region).queryAllByRole('link', { name: 'Raise memory limit' })).toHaveLength(1);
    });
  });

  describe('disk forecast', () => {
    it('shows the estimate when the server reports days left', async () => {
      install([stack('alpha')], [], { 'GET /api/host/disk-forecast': { body: { daysLeft: 41, sampledDays: 9 } } });
      mount();
      expect(await screen.findByText(/About 41 days until the disk is full/)).toBeTruthy();
    });

    it('is hidden when daysLeft is null', async () => {
      install([stack('alpha')], [], { 'GET /api/host/disk-forecast': { body: { daysLeft: null, sampledDays: 0 } } });
      mount();
      await screen.findByRole('heading', { level: 1 });
      await waitFor(() => expect(count('/api/host/disk-forecast')).toBe(1));
      expect(screen.queryByText(/until the disk is full|left at the current rate/)).toBeNull();
    });

    it('is not requested, and nothing is shown, when the server lacks the feature', async () => {
      install([stack('alpha')], [], { 'GET /api/host/disk-forecast': { body: { daysLeft: 41, sampledDays: 9 } } });
      render(
        <ServerContext.Provider value={{ ...localScope, features: [] }}>
          <MemoryRouter>
            <Overview />
          </MemoryRouter>
        </ServerContext.Provider>,
      );
      await screen.findByRole('heading', { level: 1 });
      await new Promise((r) => setTimeout(r, 20));
      expect(count('/api/host/disk-forecast')).toBe(0);
      expect(screen.queryByText(/until the disk is full/)).toBeNull();
    });

    it('is hidden, without an error banner, when the request fails', async () => {
      install([stack('alpha')], [], { 'GET /api/host/disk-forecast': { status: 404, body: { error: 'nope' } } });
      mount();
      await screen.findByRole('heading', { level: 1 });
      await waitFor(() => expect(count('/api/host/disk-forecast')).toBe(1));
      expect(screen.queryByText(/until the disk is full/)).toBeNull();
      expect(screen.queryByText('nope')).toBeNull();
    });
  });
});
