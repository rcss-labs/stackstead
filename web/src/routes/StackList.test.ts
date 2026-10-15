import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { writable } from 'svelte/store';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Change, Operation, StackSummary } from '$lib/api/types';
import { navigate } from '$lib/router';
import { refreshRunningOps, refreshStacks, startStacks } from '$lib/stores/stacks';
import * as liveModule from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { get } from 'svelte/store';
import { installFakeFetch, type FakeFetch } from '../vitest-setup';
import StackList from './StackList.svelte';

const stack = (name: string, over: Partial<StackSummary> = {}): StackSummary => ({
  name,
  path: `/srv/stacks/${name}/compose.yaml`,
  project: `proj-${name}`,
  origin: 'managed',
  status: 'running',
  serviceCount: 3,
  running: 2,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  ...over,
});
const op = (name: string): Operation => ({ id: 'op1', stack: name, action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' });

const setLiveState = (v: Record<string, unknown>) => (liveModule as unknown as { __set: (v: unknown) => void }).__set(v);
const setEvents = (v: unknown[]) => (liveModule as unknown as { __setEvents: (v: unknown) => void }).__setEvents(v);
let fake: FakeFetch;
let stop: (() => void) | undefined;

async function mount(items: StackSummary[], ops: Operation[] = [], extra: Parameters<typeof installFakeFetch>[0] = {}) {
  fake = installFakeFetch({
    'GET /api/stacks': { body: items },
    'GET /api/operations?state=running': { body: ops },
    ...extra,
  });
  stop = startStacks({
    list: () => fetch('/api/stacks').then((r) => r.json()),
    changes: writable<(Change & { resync?: true }) | null>(null),
  });
  const view = render(StackList);
  await waitFor(() => expect(screen.queryByText('Loading')).toBeNull());
  return view;
}

afterEach(() => {
  toasts.reset();
  vi.mocked(navigate).mockClear();
  setLiveState(liveState.value);
  setEvents([]);
  stop?.();
  stop = undefined;
  fake?.restore();
});

const liveState = vi.hoisted(() => ({ value: { connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' } as Record<string, unknown> }));
vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('svelte/store');
  const store = writable(liveState.value);
  const eventStore = writable<unknown[]>([]);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    events: { subscribe: eventStore.subscribe },
    refreshStatus: vi.fn(),
    __set: store.set,
    __setEvents: eventStore.set,
  };
});

vi.mock('$lib/router', async (orig) => ({ ...(await orig<typeof import('$lib/router')>()), navigate: vi.fn() }));

describe('StackList', () => {
  it('renders rows sorted by name with badge, counts, origin, project and linked name', async () => {
    await mount([stack('zeta', { status: 'stopped', running: 0 }), stack('alpha')]);
    const rows = screen.getAllByRole('row').slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByRole('link', { name: 'alpha' }).getAttribute('href')).toBe('/stacks/alpha');
    expect(within(rows[0]).getByText('running')).toBeTruthy();
    expect(within(rows[0]).getByText('2/3')).toBeTruthy();
    expect(within(rows[0]).getByText('managed')).toBeTruthy();
    expect(within(rows[0]).getByRole('link', { name: 'alpha' }).getAttribute('title')).toBe('project proj-alpha');
    expect(within(rows[0]).getByText(/compose\.yaml/).getAttribute('title')).toBe('/srv/stacks/alpha/compose.yaml');
    expect(within(rows[1]).getByText('stopped')).toBeTruthy();
    expect(screen.getByRole('button', { name: /^All\s*2$/ })).toBeTruthy();
  });

  it('shows the empty state with create and import links', async () => {
    await mount([]);
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getByRole('link', { name: 'Import' }).getAttribute('href')).toBe('/stacks/import');
  });

  it('search filters by name or project', async () => {
    await mount([stack('alpha'), stack('beta', { project: 'shared-db' })]);
    const user = userEvent.setup();
    await user.type(screen.getByRole('searchbox', { name: /search/i }), 'shared');
    expect(screen.queryByRole('link', { name: 'alpha' })).toBeNull();
    expect(screen.getByRole('link', { name: 'beta' })).toBeTruthy();
    await user.clear(screen.getByRole('searchbox', { name: /search/i }));
    await user.type(screen.getByRole('searchbox', { name: /search/i }), 'ALP');
    expect(screen.getByRole('link', { name: 'alpha' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'beta' })).toBeNull();
  });

  it('shows a running-operation spinner only for stacks with a running operation', async () => {
    await mount([stack('alpha'), stack('beta')], [op('beta')]);
    const rows = screen.getAllByRole('row').slice(1);
    await waitFor(() => expect(within(rows[1]).getByText('Operation running on beta')).toBeTruthy());
    expect(within(rows[0]).queryByText(/Operation running/)).toBeNull();
  });

  it('flags unavailable stacks with a warning that links to the detail page', async () => {
    await mount([stack('broken', { status: 'unavailable' })]);
    expect(screen.getByText('unavailable')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'broken' }).getAttribute('href')).toBe('/stacks/broken');
  });

  it('notes degraded status when every stack is unknown', async () => {
    await mount([stack('a', { status: 'unknown' }), stack('b', { status: 'unknown' })]);
    expect(screen.getByText(/status is unavailable/i)).toBeTruthy();
  });

  it('does not show the degraded note when some stacks have a known status', async () => {
    await mount([stack('a', { status: 'unknown' }), stack('b')]);
    expect(screen.queryByText(/status is unavailable/i)).toBeNull();
  });

  it('encodes names with spaces and # in links', async () => {
    await mount([stack('my stack#1')]);
    expect(screen.getByRole('link', { name: 'my stack#1' }).getAttribute('href')).toBe('/stacks/my%20stack%231');
  });

  it('names the table and keeps table roles', async () => {
    await mount([stack('alpha')]);
    expect(screen.getByRole('table', { name: 'Stacks' })).toBeTruthy();
    expect(screen.getAllByRole('columnheader').length).toBe(9);
  });

  it('shows no-match text only for a non-empty query', async () => {
    await mount([stack('alpha')]);
    expect(screen.queryByText(/No stacks match/)).toBeNull();
    await userEvent.setup().type(screen.getByRole('searchbox'), 'zzz');
    expect(screen.getByText(/No stacks match/)).toBeTruthy();
  });

  it('rechecks running operations with backoff until none run, and on demand', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let running = [op('alpha')];
      fake = installFakeFetch({
        'GET /api/stacks': { body: [stack('alpha')] },
        'GET /api/operations?state=running': () => ({ body: running }),
      });
      stop = startStacks({
        list: () => fetch('/api/stacks').then((r) => r.json()),
        changes: writable<(Change & { resync?: true }) | null>(null),
      });
      render(StackList);
      await waitFor(() => expect(screen.getByText('Operation running on alpha')).toBeTruthy());
      const opsCalls = () => fake.calls.filter((c) => c.path.startsWith('/api/operations')).length;
      const before = opsCalls();
      running = [];
      await vi.advanceTimersByTimeAsync(2100);
      await waitFor(() => expect(screen.queryByText(/Operation running/)).toBeNull());
      const after = opsCalls();
      expect(after).toBeGreaterThan(before);
      await vi.advanceTimersByTimeAsync(30000);
      expect(opsCalls()).toBe(after); // stopped polling once nothing runs
      running = [op('alpha')];
      refreshRunningOps();
      await waitFor(() => expect(screen.getByText('Operation running on alpha')).toBeTruthy());
    } finally {
      vi.useRealTimers();
    }
  });

  it('first-load failure shows only the banner and a Retry that refetches', async () => {
    let fail = true;
    fake = installFakeFetch({
      'GET /api/stacks': () => (fail ? { status: 500, body: { error: 'boom' } } : { body: [stack('alpha')] }),
      'GET /api/operations?state=running': { body: [] },
    });
    stop = startStacks({
      list: async () => {
        const r = await fetch('/api/stacks');
        if (!r.ok) throw new Error('boom');
        return r.json();
      },
      changes: writable<(Change & { resync?: true }) | null>(null),
    });
    render(StackList);
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'));
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByText(/No stacks match/)).toBeNull();
    fail = false;
    await userEvent.setup().click(screen.getByRole('button', { name: /retry/i }));
    await waitFor(() => expect(screen.getByRole('link', { name: 'alpha' })).toBeTruthy());
  });

  it('shows a busy Retry while the refetch is in flight', async () => {
    let release: (() => void) | undefined;
    let fail = true;
    fake = installFakeFetch({
      'GET /api/stacks': async () => {
        if (fail) return { status: 500, body: { error: 'boom' } };
        await new Promise<void>((r) => (release = r));
        return { body: [stack('alpha')] };
      },
      'GET /api/operations?state=running': { body: [] },
    });
    stop = startStacks({
      list: async () => {
        const r = await fetch('/api/stacks');
        if (!r.ok) throw new Error('boom');
        return r.json();
      },
      changes: writable<(Change & { resync?: true }) | null>(null),
    });
    render(StackList);
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'));
    fail = false;
    await userEvent.setup().click(screen.getByRole('button', { name: /retry/i }));
    await waitFor(() => expect(screen.getByRole('button', { name: /retry/i }).getAttribute('aria-busy')).toBe('true'));
    release?.();
    await waitFor(() => expect(screen.getByRole('link', { name: 'alpha' })).toBeTruthy());
  });

  it('chips filter and count', async () => {
    await mount([stack('a'), stack('b', { status: 'stopped' }), stack('c', { status: 'partial' })]);
    const user = userEvent.setup();
    expect(screen.getByRole('button', { name: /^Running\s*1$/ }).getAttribute('aria-pressed')).toBe('false');
    expect(screen.getByRole('button', { name: /^All\s*3$/ }).getAttribute('aria-pressed')).toBe('true');
    await user.click(screen.getByRole('button', { name: /^Stopped\s*1$/ }));
    expect(screen.getByRole('button', { name: /^Stopped/ }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.queryByRole('link', { name: 'a' })).toBeNull();
    expect(screen.getByRole('link', { name: 'b' })).toBeTruthy();
  });

  it('shows the no-match text for a chip with no stacks', async () => {
    await mount([stack('a')]);
    await userEvent.setup().click(screen.getByRole('button', { name: /^Partial/ }));
    expect(screen.getByText('No stacks match the current filter.')).toBeTruthy();
  });

  it('unavailable chip only when present', async () => {
    const { unmount } = await mount([stack('a')]);
    expect(screen.queryByRole('button', { name: /^Unavailable/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /^Unknown/ })).toBeNull();
    unmount();
    stop?.();
    fake.restore();
    await mount([stack('a'), stack('b', { status: 'unavailable' })]);
    expect(screen.getByRole('button', { name: /^Unavailable\s*1$/ })).toBeTruthy();
  });

  it('search matches path', async () => {
    await mount([stack('alpha', { path: '/data/one/compose.yaml' }), stack('beta')]);
    await userEvent.setup().type(screen.getByRole('searchbox', { name: 'Search stacks' }), '/data/one');
    expect(screen.getByRole('link', { name: 'alpha' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'beta' })).toBeNull();
  });

  it('health cell numbers and dash', async () => {
    await mount([
      stack('alpha', { health: { healthy: 2, unhealthy: 1, starting: 1, none: 0, failed: 0 } }),
      stack('beta'),
    ]);
    const rows = screen.getAllByRole('row').slice(1);
    const cell = within(rows[0]).getByTitle(/2 healthy, 1 unhealthy, 1 starting/);
    expect(cell.querySelector('.good')?.textContent).toBe('2');
    expect(cell.querySelector('.bad')?.textContent).toBe('1');
    expect(cell.querySelector('.starting')?.textContent).toBe('1');
    expect(within(rows[1]).getByTitle(/0 healthy/).textContent).toBe('—');
  });

  it('ports +N with title', async () => {
    await mount([stack('alpha', { ports: ['1', '2', '3', '4', '5'] })]);
    const more = screen.getByText('+2');
    expect(more.closest('[title]')?.getAttribute('title')).toBe('1, 2, 3, 4, 5');
    expect(screen.queryByText('4')).toBeNull();
  });

  describe('inline actions', () => {
    const setLive = (compose = '2.29.0') => setLiveState({ ...liveState.value, compose });
    const started = { id: 'op9', stack: 'alpha', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' };

    it('inline Up calls api.act and opens the operation', async () => {
      setLive();
      await mount([stack('alpha', { status: 'stopped' })], [], { 'POST /api/stacks/alpha/actions/up': { body: started } });
      await userEvent.setup().click(screen.getByRole('button', { name: 'Up alpha' }));
      await waitFor(() => expect(navigate).toHaveBeenCalledWith('/stacks/alpha?tab=operations&op=op9'));
    });

    it('inline action is disabled while an operation runs on that stack', async () => {
      setLive();
      await mount([stack('alpha'), stack('beta')], [op('alpha')]);
      await waitFor(() => expect(screen.getByRole('button', { name: 'Down alpha' }).getAttribute('aria-disabled')).toBe('true'));
      expect(screen.getByRole('button', { name: 'Down alpha' }).getAttribute('title')).toBe('an operation is already running');
      expect(screen.getByRole('button', { name: 'Down beta' }).getAttribute('aria-disabled')).toBeNull();
    });

    it('inline actions disabled when compose is unavailable', async () => {
      setLive('unavailable');
      await mount([stack('alpha')]);
      expect(screen.getByRole('button', { name: 'Restart alpha' }).getAttribute('title')).toBe('docker compose plugin not found');
    });

    it('unavailable stacks get the parse reason', async () => {
      setLive();
      await mount([stack('alpha', { status: 'unavailable' })]);
      expect(screen.getByRole('button', { name: 'Up alpha' }).getAttribute('title')).toBe('the compose file cannot be parsed');
    });

    it('double click sends one act', async () => {
      setLive();
      await mount([stack('alpha', { status: 'stopped' })], [], {
        'POST /api/stacks/alpha/actions/up': async () => {
          await new Promise((r) => setTimeout(r, 50));
          return { body: started };
        },
      });
      const btn = screen.getByRole('button', { name: 'Up alpha' });
      const user = userEvent.setup();
      await user.click(btn);
      await user.click(btn);
      await waitFor(() => expect(navigate).toHaveBeenCalled());
      expect(fake.calls.filter((c) => c.path.endsWith('/actions/up'))).toHaveLength(1);
    });

    it('act failure toasts and re-enables', async () => {
      setLive();
      await mount([stack('alpha', { status: 'stopped' })], [], { 'POST /api/stacks/alpha/actions/up': { status: 409, body: { error: 'busy' } } });
      await userEvent.setup().click(screen.getByRole('button', { name: 'Up alpha' }));
      await waitFor(() => expect(get(toasts).some((t) => t.kind === 'warning' && t.message.startsWith('Could not run up'))).toBe(true));
      expect(screen.getByRole('button', { name: 'Up alpha' }).getAttribute('aria-disabled')).toBeNull();
    });
  });

  const hostSnap = (over: Record<string, unknown> = {}) => ({
    hostname: 'nas-01', os: 'linux', source: 'proc', sampledAt: '2026-10-15T10:00:00Z',
    containers: { total: 10, running: 8, exited: 2, bad: 0 },
    cpu: { percent: 18, cores: 8 },
    memory: { used: 4 * 1024 ** 3, total: 16 * 1024 ** 3, swapUsed: 0, swapTotal: 0 },
    disk: null, network: null, diskIO: null, volumes: null, uptime: null, restarts24h: null,
    ...over,
  });
  const summaryFor = (project: string, over: Record<string, unknown> = {}) => ({
    window: '15m', step: 30, from: '', to: '', top: { cpu: [], mem: [] },
    stacks: {
      [project]: {
        cpu: [1, 2, 3, ...Array(27).fill(null)], mem: [5, 6, ...Array(28).fill(null)],
        cpuNow: 12.5, memNow: 2 * 1024 ** 2 * 100, netRxBytesPerSec: 2048, netTxBytesPerSec: 512, ...over,
      },
    },
  });
  const dashboardFake = (host = hostSnap(), summary: unknown = summaryFor('proj-alpha')) => ({
    'GET /api/host': { body: host },
    'GET /api/alerts': { body: [] },
    'GET /api/metrics/summary': { body: summary },
  });

  it('hostname shows after heading', async () => {
    await mount([stack('alpha')], [], dashboardFake());
    expect(await screen.findByText('nas-01')).toBeTruthy();
    const h1 = screen.getByRole('heading', { level: 1 });
    expect(h1.nextElementSibling?.textContent).toBe('nas-01');
  });

  it('tiles render from host snapshot', async () => {
    await mount([stack('alpha')], [], dashboardFake());
    expect(await screen.findByText('8/10')).toBeTruthy();
    expect(screen.getByText('18.0%')).toBeTruthy();
    expect(screen.getByText('8 cores')).toBeTruthy();
    expect(screen.getByText('4.0 GiB / 16.0 GiB')).toBeTruthy();
    expect(screen.queryByText('Uptime')).toBeNull();
  });

  it('CPU and Memory cells show sparkline and value', async () => {
    await mount([stack('alpha')], [], dashboardFake());
    await screen.findByText('12.5%');
    const row = screen.getAllByRole('row')[1];
    expect(within(row).getByText('200.0 MiB')).toBeTruthy();
    const paths = row.querySelectorAll('svg[viewBox="0 0 60 18"] path');
    expect(paths).toHaveLength(2);
    expect(paths[0].getAttribute('stroke')).toBe('var(--accent)');
    expect(paths[1].getAttribute('stroke')).toBe('var(--info)');
    expect(paths[0].getAttribute('d')).not.toBe('M0,17L60,17');
  });

  it('stack without samples shows dash and flat line', async () => {
    await mount([stack('alpha'), stack('beta')], [], dashboardFake());
    await screen.findByText('12.5%');
    const row = screen.getAllByRole('row')[2];
    expect(row.querySelector('td[data-label="CPU"]')?.textContent).toContain('—');
    expect(row.querySelector('td[data-label="Memory"]')?.textContent).toContain('—');
    expect(row.querySelector('td[data-label="CPU"] path')?.getAttribute('d')).toBe('M0,17L60,17');
  });

  it('Net RX/TX cell', async () => {
    await mount([stack('alpha'), stack('beta')], [], dashboardFake());
    await screen.findByText('2.0 KiB/s / 512 B/s');
    const row = screen.getAllByRole('row')[2];
    expect(row.querySelector('td[data-label="Net RX / TX"]')?.textContent?.trim()).toBe('—');
  });

  it('columns keep card data-labels', async () => {
    await mount([stack('alpha')], [], dashboardFake());
    const row = screen.getAllByRole('row')[1];
    const labels = [...row.querySelectorAll('td')].map((td) => td.getAttribute('data-label'));
    expect(labels).toEqual(['Stack', 'Status', 'Services', 'Health', 'Ports', 'CPU', 'Memory', 'Net RX / TX', 'Actions']);
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent?.trim())).toEqual(['Stack', 'Status', 'Services', 'Health', 'Ports', 'CPU', 'Memory', 'Net RX / TX', 'Actions']);
  });

  it('health shows good and bad always and starting only when present', async () => {
    await mount([stack('alpha', { health: { healthy: 2, none: 1, unhealthy: 0, failed: 1, starting: 0 } }), stack('beta', { health: { healthy: 1, none: 0, unhealthy: 0, failed: 0, starting: 0 } })]);
    const rows = screen.getAllByRole('row').slice(1);
    const a = within(rows[0]).getByTitle(/2 healthy/);
    expect(a.querySelector('.good')?.textContent).toBe('3');
    expect(a.querySelector('.bad')?.textContent).toBe('1');
    expect(a.querySelector('.starting')).toBeNull();
    const b = within(rows[1]).getByTitle(/1 healthy/);
    expect(b.querySelector('.bad')?.textContent).toBe('0');
  });

  it('services strip has one cell per service with the running ones tinted', async () => {
    await mount([stack('alpha', { serviceCount: 4, running: 3 })]);
    const row = screen.getAllByRole('row')[1];
    expect(row.querySelectorAll('.cells .cell')).toHaveLength(4);
    expect(row.querySelectorAll('.cells .cell.on')).toHaveLength(3);
    expect(row.querySelector('.cells')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('resets to All when the active Unavailable chip disappears', async () => {
    let items = [stack('a'), stack('b', { status: 'unavailable' })];
    await mount([], [], { 'GET /api/stacks': () => ({ body: items }) });
    await refreshStacks();
    await userEvent.setup().click(await screen.findByRole('button', { name: /^Unavailable/ }));
    expect(screen.queryByRole('link', { name: 'a' })).toBeNull();
    items = [stack('a'), stack('b')];
    await refreshStacks();
    await waitFor(() => expect(screen.getByRole('button', { name: /^All/ }).getAttribute('aria-pressed')).toBe('true'));
    expect(screen.getByRole('link', { name: 'a' })).toBeTruthy();
  });

  it('dashboard sections render below the table with alerts, top consumers and events', async () => {
    const alerts = [{ kind: 'unhealthy', severity: 'danger', stack: 'alpha', message: 'db is unhealthy', since: '2026-10-15T10:00:00Z' }];
    const summary = summaryFor('proj-alpha');
    (summary as { top: unknown }).top = { cpu: [{ container: 'web-1', service: 'web', project: 'proj-alpha', value: 42 }], mem: [] };
    await mount([stack('alpha')], [], { ...dashboardFake(hostSnap(), summary), 'GET /api/alerts': { body: alerts } });
    expect(await screen.findByRole('heading', { name: 'Alerts 1' })).toBeTruthy();
    expect(screen.getByText('db is unhealthy')).toBeTruthy();
    expect(await screen.findByText('web-1 · alpha')).toBeTruthy();
    expect(screen.getByText('Waiting for events…')).toBeTruthy();
    setEvents([{ action: 'die', container: 'web-1', project: 'alpha', service: 'web', image: 'i', time: '2026-10-15T10:00:00Z', exitCode: 1 }]);
    expect(await screen.findByText('web-1 exit 1')).toBeTruthy();
    expect(screen.getByText('live')).toBeTruthy();
    setLiveState({ ...liveState.value, connection: 'reconnecting' });
    expect(await screen.findByText('reconnecting')).toBeTruthy();
  });

  it('empty texts only show once alerts and summary have loaded', async () => {
    await mount([stack('alpha')], [], {
      'GET /api/host': { status: 500, body: { error: 'x' } },
      'GET /api/alerts': { status: 500, body: { error: 'x' } },
      'GET /api/metrics/summary': { status: 500, body: { error: 'x' } },
    });
    expect(screen.getByRole('heading', { name: 'Alerts' })).toBeTruthy();
    expect(screen.queryByText('No alerts.')).toBeNull();
    expect(screen.queryByText('No samples yet.')).toBeNull();
  });

  it('dashboard sections show even when there are no stacks', async () => {
    await mount([], [], dashboardFake());
    expect(await screen.findByText('No alerts.')).toBeTruthy();
    expect(screen.getByText('No samples yet.')).toBeTruthy();
  });
});
