import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { get, writable } from '$lib/store';
import type { Alert, Change, HostSnapshot, Operation, StackSummary } from '$lib/api/types';
import { startHost } from '$lib/stores/host';
import * as liveModule from '$lib/stores/live';
import { startRunningOps } from '$lib/stores/runningOps';
import { operationsRefresh, refreshRunningOps, refreshStacks, startStacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import Overview from '../routes/Overview';

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
const started = { id: 'op9', stack: 'alpha', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' };

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

const liveDefaults = vi.hoisted(() => ({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' }));
const setLive = (v: Record<string, unknown>) => (liveModule as unknown as { __set: (v: unknown) => void }).__set(v);

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const store = writable(liveDefaults);
  const eventStore = writable<unknown[]>([]);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    events: { subscribe: eventStore.subscribe },
    refreshStatus: vi.fn(),
    __set: store.set,
  };
});

let fake: FakeFetch;
let stops: (() => void)[] = [];
let stacksRes: FakeResponse;
let opsNow: Operation[] = [];

function install(items: StackSummary[], opts: { ops?: Operation[]; extra?: Parameters<typeof installFakeFetch>[0]; summary?: unknown; alerts?: Alert[] } = {}) {
  stacksRes = { body: items };
  opsNow = opts.ops ?? [];
  fake = installFakeFetch({
    'GET /api/stacks': () => stacksRes,
    'GET /api/operations?state=running': () => ({ body: opsNow }),
    'GET /api/host': { body: hostSnapshot() },
    'GET /api/alerts': { body: opts.alerts ?? [] },
    'GET /api/metrics/summary': { body: opts.summary ?? { window: '15m', step: 15, from: '', to: '', stacks: {}, top: { cpu: [], mem: [] } } },
    'GET /api/storage': { body: { reclaimable: 0 } },
    ...opts.extra,
  });
  const changes = writable<(Change & { resync?: true }) | null>(null);
  stops.push(startStacks({ list: () => fetch('/api/stacks').then((r) => r.json()), changes }));
  stops.push(startHost({ host: () => fetch('/api/host').then((r) => r.json()) }));
  stops.push(startRunningOps({ list: () => fetch('/api/operations?state=running').then((r) => r.json()), changes, refresh: operationsRefresh }));
}

/** Every distinct location the router visited, to count history entries. */
let visited: string[] = [];

function Where() {
  const l = useLocation();
  if (visited[visited.length - 1] !== l.key) visited.push(l.key);
  return <output data-testid="where">{l.pathname + l.search}</output>;
}

async function mount() {
  visited = [];
  const view = render(
    <MemoryRouter>
      <Overview />
      <Where />
    </MemoryRouter>,
  );
  await screen.findByRole('table', { name: 'Stacks' });
  return view;
}

const where = () => screen.getByTestId('where').textContent;
const rowOf = (name: string) => {
  const row = document.querySelector<HTMLElement>(`[data-stack="${CSS.escape(name)}"]`);
  if (!row) throw new Error(`no row for ${name}`);
  return row;
};
const rowNames = () => [...document.querySelectorAll('[data-stack]')].map((r) => r.getAttribute('data-stack'));
const groupHeader = (id: string) => document.querySelector<HTMLElement>(`[data-group="${id}"]`);

afterEach(async () => {
  cleanup();
  vi.useRealTimers();
  stops.forEach((stop) => stop());
  stops = [];
  toasts.reset();
  vi.mocked(liveModule.refreshStatus).mockClear();
  setLive(liveDefaults);
  fake?.restore();
  await new Promise((r) => setTimeout(r, 5));
});

describe('stacks list', () => {
  it('renders rows grouped with counts, sorted by name, with the linked name, imported tag, status text and n/m', async () => {
    install([stack('beta', { origin: 'external' }), stack('alpha')]);
    await mount();
    const header = groupHeader('running')!;
    expect(header.textContent).toMatch(/Running\s*2/);
    expect(rowNames()).toEqual(['alpha', 'beta']);
    const alpha = within(rowOf('alpha'));
    expect(alpha.getByRole('link', { name: 'alpha' }).getAttribute('href')).toBe('/stacks/alpha');
    expect(alpha.getByRole('link', { name: 'alpha' }).getAttribute('title')).toBe('project proj-alpha');
    expect(alpha.queryByText('imported')).toBeNull();
    expect(within(rowOf('beta')).getByText('imported')).toBeTruthy();
    expect(alpha.getByText('running')).toBeTruthy();
    expect(alpha.getByText('2/3')).toBeTruthy();
  });

  it('needs attention group lists partial and unhealthy stacks first and the Unknown group appears only when present', async () => {
    install([
      stack('calm'),
      stack('half', { status: 'partial' }),
      stack('sick', { health: { healthy: 0, unhealthy: 1, starting: 0, none: 0, failed: 0 } }),
      stack('off', { status: 'stopped', running: 0 }),
    ]);
    await mount();
    expect(rowNames()).toEqual(['half', 'sick', 'calm', 'off']);
    expect(groupHeader('attention')!.textContent).toMatch(/Needs attention\s*2/);
    expect(groupHeader('stopped')!.textContent).toMatch(/Stopped\s*1/);
    expect(groupHeader('unknown')).toBeNull();
    cleanup();
    stops.forEach((stop) => stop());
    stops = [];
    fake.restore();
    install([stack('calm'), stack('mystery', { status: 'unknown' })]);
    await mount();
    expect(groupHeader('unknown')!.textContent).toMatch(/Unknown\s*1/);
  });

  it('a stack leaving the attention group moves between groups after a refresh', async () => {
    install([stack('alpha', { status: 'partial' }), stack('beta')]);
    await mount();
    expect(groupHeader('attention')).not.toBeNull();
    stacksRes = { body: [stack('alpha'), stack('beta')] };
    void refreshStacks();
    await waitFor(() => expect(groupHeader('attention')).toBeNull());
    expect(groupHeader('running')!.textContent).toMatch(/Running\s*2/);
  });

  it('search filters by name, project or path, case-insensitively', async () => {
    install([stack('alpha', { path: '/data/one/compose.yaml' }), stack('beta', { project: 'shared-db' })]);
    await mount();
    const user = userEvent.setup();
    const box = screen.getByRole('searchbox', { name: 'Search stacks' });
    await user.type(box, 'shared');
    expect(rowNames()).toEqual(['beta']);
    await user.clear(box);
    await user.type(box, 'ALP');
    expect(rowNames()).toEqual(['alpha']);
    await user.clear(box);
    await user.type(box, '/data/one');
    expect(rowNames()).toEqual(['alpha']);
  });

  it('shows no-match text only for a non-empty query', async () => {
    install([stack('alpha')]);
    await mount();
    expect(screen.queryByText(/No stacks match/)).toBeNull();
    await userEvent.setup().type(screen.getByRole('searchbox', { name: 'Search stacks' }), 'zzz');
    expect(screen.getByText('No stacks match the current filter.')).toBeTruthy();
  });

  it('names the table, keeps six column headers and labels the groups', async () => {
    install([stack('alpha')]);
    await mount();
    const table = screen.getByRole('table', { name: 'Stacks' });
    const headers = within(table).getAllByRole('columnheader').map((h) => h.textContent);
    expect(headers).toEqual(['Status', 'Stack', 'Services', 'Ports', 'CPU', 'Memory', 'Actions']);
    expect(within(rowOf('alpha')).getAllByRole('cell')).toHaveLength(headers.length);
    expect(groupHeader('running')!.querySelector('[role="cell"]')!.getAttribute('aria-colspan')).toBe(String(headers.length));
    expect(within(table).getAllByRole('rowgroup').length).toBeGreaterThanOrEqual(2);
    expect(groupHeader('running')!.getAttribute('role')).toBe('row');
  });

  it('encodes names with spaces and # in links', async () => {
    install([stack('my stack#1')]);
    await mount();
    expect(screen.getByRole('link', { name: 'my stack#1' }).getAttribute('href')).toBe('/stacks/my%20stack%231');
  });

  it('sub-line names failing services from the alerts in warning, else the path', async () => {
    const alerts: Alert[] = [{ kind: 'exited', severity: 'danger', stack: 'alpha', service: 'web', message: 'web exited', since: '2026-10-14T10:00:00Z' }];
    install([stack('alpha', { status: 'partial' }), stack('beta')], { alerts });
    await mount();
    const down = await within(rowOf('alpha')).findByText('web down');
    expect(down.getAttribute('data-tone')).toBe('warning');
    const path = within(rowOf('beta')).getByText('/srv/stacks/beta/compose.yaml');
    expect(path.getAttribute('title')).toBe('/srv/stacks/beta/compose.yaml');
  });

  it('service cells show one cell per service with the health title', async () => {
    install([stack('alpha', { serviceCount: 4, health: { healthy: 2, unhealthy: 1, starting: 1, none: 0, failed: 0 } })]);
    await mount();
    const cells = within(rowOf('alpha')).getAllByTitle(/2 healthy, 1 unhealthy, 1 starting/)[0];
    expect(cells.children).toHaveLength(4);
  });

  it('ports render as :port links to the host in a new tab, +N keeps the full title', async () => {
    install([stack('alpha', { ports: ['8123', '9000-9005'] }), stack('beta', { ports: ['1', '2', '3', '4', '5'] }), stack('gamma')]);
    await mount();
    const link = within(rowOf('alpha')).getByRole('link', { name: ':8123' });
    expect(link.getAttribute('href')).toBe(`http://${window.location.hostname}:8123`);
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toBe('noreferrer');
    expect(within(rowOf('alpha')).getByRole('link', { name: ':9000-9005' }).getAttribute('href')).toBe(`http://${window.location.hostname}:9000`);
    const beta = within(rowOf('beta'));
    expect(beta.getByText('+2').closest('[title]')?.getAttribute('title')).toBe('1, 2, 3, 4, 5');
    expect(beta.queryByText(':4')).toBeNull();
    expect(within(rowOf('gamma')).getAllByText('—').length).toBeGreaterThan(0);
  });

  it('CPU and Memory cells show the sparkline and value from the summary, dash and flat line without samples', async () => {
    const summary = {
      window: '15m', step: 30, from: '', to: '', top: { cpu: [], mem: [] },
      stacks: { 'proj-alpha': { cpu: [1, 2, 3, ...Array(27).fill(null)], mem: [5, 6, ...Array(28).fill(null)], cpuNow: 12.5, memNow: 200 * 1024 ** 2, netRxBytesPerSec: 0, netTxBytesPerSec: 0 } },
    };
    install([stack('alpha'), stack('beta')], { summary });
    await mount();
    await screen.findByText('12.5%');
    const alpha = rowOf('alpha');
    expect(within(alpha).getByText('200.0 MiB')).toBeTruthy();
    const paths = alpha.querySelectorAll('svg[viewBox="0 0 60 18"] path');
    expect(paths).toHaveLength(2);
    expect(paths[0].getAttribute('stroke')).toBe('var(--accent)');
    expect(paths[1].getAttribute('stroke')).toBe('var(--info)');
    expect(paths[0].getAttribute('d')).not.toBe('M0,17L60,17');
    const beta = rowOf('beta');
    expect(within(beta).getAllByText('—').length).toBeGreaterThanOrEqual(2);
    expect(beta.querySelector('svg path')?.getAttribute('d')).toBe('M0,17L60,17');
  });

  it("clicking a row's empty area opens the stack; clicking the name, a port or an action does not double-navigate", async () => {
    install([stack('alpha', { ports: ['8123'] }), stack('beta', { status: 'stopped', running: 0 })], { extra: { 'POST /api/stacks/beta/actions/up': { body: { ...started, stack: 'beta' } } } });
    await mount();
    const user = userEvent.setup();
    await user.click(rowOf('beta'));
    expect(where()).toBe('/stacks/beta');
    cleanup();
    await mount();
    expect(where()).toBe('/');
    await user.click(within(rowOf('alpha')).getByRole('link', { name: ':8123' }));
    expect(where()).toBe('/');
    const entries = visited.length;
    await user.click(within(rowOf('alpha')).getByRole('link', { name: 'alpha' }));
    expect(where()).toBe('/stacks/alpha');
    expect(visited).toHaveLength(entries + 1); // exactly one push
    cleanup();
    await mount();
    await user.click(within(rowOf('alpha')).getByRole('link', { name: 'Logs' }));
    expect(where()).toBe('/stacks/alpha?tab=logs');
    cleanup();
    await mount();
    await user.click(within(rowOf('beta')).getByRole('button', { name: 'Up beta' }));
    await waitFor(() => expect(where()).toBe('/stacks/beta?tab=operations&op=op9'));
  });

  it('shows a running-operation spinner only for stacks with a running operation', async () => {
    install([stack('alpha'), stack('beta')], { ops: [op('beta')] });
    await mount();
    await waitFor(() => expect(within(rowOf('beta')).getByText('Operation running on beta')).toBeTruthy());
    expect(within(rowOf('alpha')).queryByText(/Operation running/)).toBeNull();
  });

  it('rechecks running operations with backoff until none run, and on demand', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    install([stack('alpha')], { ops: [op('alpha')] });
    await mount();
    await waitFor(() => expect(screen.getByText('Operation running on alpha')).toBeTruthy());
    const opsCalls = () => fake.calls.filter((c) => c.path.startsWith('/api/operations')).length;
    const before = opsCalls();
    opsNow = [];
    await vi.advanceTimersByTimeAsync(2100);
    await waitFor(() => expect(screen.queryByText(/Operation running/)).toBeNull());
    const after = opsCalls();
    expect(after).toBeGreaterThan(before);
    await vi.advanceTimersByTimeAsync(30000);
    expect(opsCalls()).toBe(after); // stopped polling once nothing runs
    opsNow = [op('alpha')];
    refreshRunningOps();
    await waitFor(() => expect(screen.getByText('Operation running on alpha')).toBeTruthy());
  });

  describe('inline actions', () => {
    it('inline Up calls api.act and opens the operation', async () => {
      install([stack('alpha', { status: 'stopped', running: 0 })], { extra: { 'POST /api/stacks/alpha/actions/up': { body: started } } });
      await mount();
      await userEvent.setup().click(screen.getByRole('button', { name: 'Up alpha' }));
      await waitFor(() => expect(where()).toBe('/stacks/alpha?tab=operations&op=op9'));
    });

    it('the primary action is Restart for running and partial stacks and Up for stopped, unavailable and unknown', async () => {
      install([
        stack('a-run'),
        stack('b-part', { status: 'partial' }),
        stack('c-off', { status: 'stopped', running: 0 }),
        stack('d-bad', { status: 'unavailable' }),
        stack('e-unk', { status: 'unknown' }),
      ]);
      await mount();
      const labels = rowNames().map((n) => within(rowOf(n!)).getByRole('button', { name: /^(Up|Restart) / }).getAttribute('aria-label'));
      expect(new Set(labels)).toEqual(new Set(['Restart a-run', 'Restart b-part', 'Up c-off', 'Up d-bad', 'Up e-unk']));
    });

    it('inline action is disabled while an operation runs on that stack', async () => {
      install([stack('alpha'), stack('beta')], { ops: [op('alpha')] });
      await mount();
      await waitFor(() => expect(screen.getByRole('button', { name: 'Restart alpha' }).getAttribute('aria-disabled')).toBe('true'));
      expect(screen.getByRole('button', { name: 'Restart alpha' }).getAttribute('title')).toBe('an operation is already running');
      expect(screen.getByRole('button', { name: 'Restart beta' }).getAttribute('aria-disabled')).toBeNull();
    });

    it('inline actions disabled when compose is unavailable, and a click sends no request', async () => {
      setLive({ ...liveDefaults, compose: 'unavailable' });
      install([stack('alpha')]);
      await mount();
      const btn = screen.getByRole('button', { name: 'Restart alpha' });
      expect(btn.getAttribute('title')).toBe('docker compose plugin not found');
      await userEvent.setup().click(btn);
      expect(fake.calls.some((c) => c.method === 'POST')).toBe(false);
    });

    it('unavailable stacks get the parse reason', async () => {
      install([stack('alpha', { status: 'unavailable' })]);
      await mount();
      expect(screen.getByRole('button', { name: 'Up alpha' }).getAttribute('title')).toBe('the compose file cannot be parsed');
    });

    it('double click sends one act', async () => {
      install([stack('alpha', { status: 'stopped', running: 0 })], {
        extra: {
          'POST /api/stacks/alpha/actions/up': async () => {
            await new Promise((r) => setTimeout(r, 50));
            return { body: started };
          },
        },
      });
      await mount();
      const btn = screen.getByRole('button', { name: 'Up alpha' });
      const user = userEvent.setup();
      await user.click(btn);
      await user.click(btn);
      await waitFor(() => expect(where()).toBe('/stacks/alpha?tab=operations&op=op9'));
      expect(fake.calls.filter((c) => c.path.endsWith('/actions/up'))).toHaveLength(1);
    });

    it('two synchronous clicks in one tick send one act', async () => {
      install([stack('alpha', { status: 'stopped', running: 0 })], { extra: { 'POST /api/stacks/alpha/actions/up': { body: started } } });
      await mount();
      const btn = screen.getByRole('button', { name: 'Up alpha' });
      // One act() batch: no re-render (and so no soft-disabled button) between the clicks.
      act(() => {
        fireEvent.click(btn);
        fireEvent.click(btn);
      });
      await waitFor(() => expect(where()).toBe('/stacks/alpha?tab=operations&op=op9'));
      expect(fake.calls.filter((c) => c.path.endsWith('/actions/up'))).toHaveLength(1);
    });

    it('enabled primary action carries its label as title', async () => {
      install([stack('alpha')]);
      await mount();
      expect(screen.getByRole('button', { name: 'Restart alpha' }).getAttribute('title')).toBe('Restart alpha');
    });

    it('act failure toasts a warning on 409 and re-enables', async () => {
      install([stack('alpha', { status: 'stopped', running: 0 })], { extra: { 'POST /api/stacks/alpha/actions/up': { status: 409, body: { error: 'busy' } } } });
      await mount();
      await userEvent.setup().click(screen.getByRole('button', { name: 'Up alpha' }));
      await waitFor(() => expect(get(toasts).some((t) => t.kind === 'warning' && t.message.startsWith('Could not run up'))).toBe(true));
      await waitFor(() => expect(screen.getByRole('button', { name: 'Up alpha' }).getAttribute('aria-disabled')).toBeNull());
      expect(where()).toBe('/');
    });

    it('a 503 failure refreshes the live status', async () => {
      install([stack('alpha', { status: 'stopped', running: 0 })], { extra: { 'POST /api/stacks/alpha/actions/up': { status: 503, body: { error: 'docker down' } } } });
      await mount();
      await userEvent.setup().click(screen.getByRole('button', { name: 'Up alpha' }));
      await waitFor(() => expect(liveModule.refreshStatus).toHaveBeenCalled());
      expect(get(toasts).some((t) => t.kind === 'danger' && t.message.startsWith('Could not run up'))).toBe(true);
    });

    it("Logs links to the stack's logs tab", async () => {
      install([stack('alpha')]);
      await mount();
      expect(within(rowOf('alpha')).getByRole('link', { name: 'Logs' }).getAttribute('href')).toBe('/stacks/alpha?tab=logs');
    });
  });
});
