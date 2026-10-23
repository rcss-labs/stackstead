import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { writable } from '$lib/store';
import type { Alert, Change, HostSnapshot, Operation, StackSummary, StorageKindTotals, StorageOverview } from '$lib/api/types';
import { issueCards } from '$lib/dashboard/issues';
import { startHost } from '$lib/stores/host';
import * as liveModule from '$lib/stores/live';
import { startRunningOps } from '$lib/stores/runningOps';
import { operationsRefresh, startStacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import Overview from '../routes/Overview';
import { IssueCards } from './IssueCards';

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

const alert = (over: Partial<Alert> = {}): Alert => ({
  kind: 'unhealthy',
  severity: 'danger',
  stack: 'web',
  service: 'db',
  message: 'db health check failing',
  since: new Date(Date.now() - 5 * 60_000).toISOString(),
  ...over,
});

const totals = (reclaimable: number): StorageKindTotals => ({ count: 1, size: null, reclaimable, sizesAt: null, refreshing: false, error: '' });
const storage = (gib: number): StorageOverview => ({
  images: totals(gib * 1024 ** 3),
  containers: totals(0),
  volumes: totals(0),
  buildCache: totals(0),
  topContainers: [],
  declarationsUnknown: [],
});

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
  libraryUpdates: 0,
  ...over,
});

function renderCards(alerts: Alert[] | undefined, over: { reason?: string; onAct?: (s: string, a: 'up' | 'restart') => void; storage?: StorageOverview } = {}) {
  return render(
    <MemoryRouter>
      <IssueCards
        cards={issueCards({ alerts, storage: over.storage })}
        stackStatus={() => 'running'}
        reasonFor={() => over.reason}
        acting={new Set()}
        onAct={over.onAct ?? vi.fn()}
      />
    </MemoryRouter>,
  );
}

afterEach(() => cleanup());

describe('issue cards', () => {
  it('renders nothing until alerts load, and nothing when there are no issues', () => {
    const { container, rerender } = renderCards(undefined);
    expect(container.innerHTML).toBe('');
    rerender(
      <MemoryRouter>
        <IssueCards cards={issueCards({ alerts: [], storage: undefined })} stackStatus={() => undefined} reasonFor={() => undefined} acting={new Set()} onAct={vi.fn()} />
      </MemoryRouter>,
    );
    expect(container.innerHTML).toBe('');
    expect(screen.queryByRole('heading', { name: 'Issues' })).toBeNull();
  });

  it('a card shows the LED, kind, stack link, age, title, message and both actions', () => {
    renderCards([alert()]);
    const section = screen.getByRole('region', { name: 'Issues' });
    const card = within(section).getByRole('article', { name: 'db is unhealthy' });
    expect(within(section).getByRole('heading', { level: 2, name: 'Issues' })).toBeTruthy();
    expect(within(card).getByRole('heading', { level: 3, name: 'db is unhealthy' })).toBeTruthy();
    expect(within(card).getByText('unhealthy', { selector: '[data-tone]' })).toBeTruthy();
    expect(within(card).getByRole('link', { name: 'web' }).getAttribute('href')).toBe('/stacks/web');
    expect(within(card).getByText('5m ago')).toBeTruthy();
    expect(within(card).getByText('db health check failing')).toBeTruthy();
    expect(within(card).getByText('danger').className).toContain('visually-hidden');
    expect(within(card).getByRole('button', { name: 'Restart stack' })).toBeTruthy();
    expect(within(card).getByRole('link', { name: 'Open logs' }).getAttribute('href')).toBe('/stacks/web?tab=logs&service=db');
  });

  it('an unhealthy card pulses and an exited card does not', () => {
    renderCards([alert(), alert({ kind: 'exited', service: 'app', message: 'app exited (1)' })]);
    const pulse = (name: string) => within(screen.getByRole('article', { name })).getByText('danger').parentElement!.querySelector('[data-tone]')!.getAttribute('data-pulse');
    expect(pulse('db is unhealthy')).toBe('true');
    expect(pulse('app exited')).toBeNull();
  });

  it('a container-only card links to the container and has no act button', () => {
    renderCards([alert({ kind: 'exited', stack: undefined, service: undefined, container: 'lone one', message: 'lone one exited (137)' })]);
    const card = screen.getByRole('article', { name: 'lone one exited' });
    expect(within(card).getByRole('link', { name: 'lone one' }).getAttribute('href')).toBe('/containers/lone%20one');
    expect(within(card).getByRole('link', { name: 'Open container' }).getAttribute('href')).toBe('/containers/lone%20one');
    expect(within(card).queryByRole('button')).toBeNull();
  });

  it('a host-scope card whose message equals its title shows the text once', () => {
    renderCards([alert({ kind: 'exited', stack: undefined, service: undefined, container: 'lone', message: 'lone exited' })]);
    expect(screen.getAllByText('lone exited')).toHaveLength(1);
  });

  it('the storage card links to /storage with Review cleanup', () => {
    renderCards([], { storage: storage(5) });
    const card = screen.getByRole('article', { name: '5.0 GiB can be freed' });
    expect(within(card).getByRole('link', { name: 'storage' }).getAttribute('href')).toBe('/storage');
    expect(within(card).getByRole('link', { name: 'Review cleanup' }).getAttribute('href')).toBe('/storage');
  });

  it('a card action is soft-disabled with the lifecycle reason and never posts', async () => {
    const onAct = vi.fn();
    renderCards([alert()], { reason: 'docker compose plugin not found', onAct });
    const button = screen.getByRole('button', { name: 'Restart stack' });
    expect(button.getAttribute('aria-disabled')).toBe('true');
    expect(button.getAttribute('title')).toBe('docker compose plugin not found');
    await userEvent.setup().click(button);
    expect(onAct).not.toHaveBeenCalled();
  });
});

describe('issue cards on the overview', () => {
  let fake: FakeFetch;
  let stops: (() => void)[] = [];
  let alertsBody: Alert[] = [];

  function install(items: StackSummary[], opts: { ops?: Operation[]; extra?: Parameters<typeof installFakeFetch>[0] } = {}) {
    const host: HostSnapshot = {
      hostname: 'box',
      os: 'linux',
      source: 'proc',
      sampledAt: new Date().toISOString(),
      containers: { total: 1, running: 1, exited: 0, bad: 0 },
      cpu: { percent: 1, cores: 1 },
      memory: { used: 1, total: 2, swapUsed: 0, swapTotal: 0 },
      disk: { path: '/', free: 1, total: 2 },
      network: { rxBytesPerSec: 0, txBytesPerSec: 0, interface: 'eth0' },
      diskIO: null,
      volumes: null,
      uptime: { seconds: 1, load1: 0, load5: 0, load15: 0 },
      restarts24h: null,
    };
    fake = installFakeFetch({
      'GET /api/stacks': { body: items },
      'GET /api/operations?state=running': { body: opts.ops ?? [] },
      'GET /api/host': { body: host },
      'GET /api/alerts': () => ({ body: alertsBody }),
      'GET /api/metrics/summary': { body: { window: '15m', step: 15, from: '', to: '', stacks: {}, top: { cpu: [], mem: [] } } },
      'GET /api/storage': { body: storage(0) },
      ...opts.extra,
    });
    const changes = writable<(Change & { resync?: true }) | null>(null);
    stops.push(startStacks({ list: () => fetch('/api/stacks').then((r) => r.json()), changes }));
    stops.push(startHost({ host: () => fetch('/api/host').then((r) => r.json()) }));
    stops.push(startRunningOps({ list: () => fetch('/api/operations?state=running').then((r) => r.json()), changes, refresh: operationsRefresh }));
  }

  function Where() {
    const l = useLocation();
    return <output data-testid="where">{l.pathname + l.search}</output>;
  }

  async function mount() {
    render(
      <MemoryRouter>
        <Overview />
        <Where />
      </MemoryRouter>,
    );
    await screen.findByRole('table', { name: 'Stacks' });
  }

  afterEach(async () => {
    stops.forEach((stop) => stop());
    stops = [];
    toasts.reset();
    setLive(liveDefaults);
    fake?.restore();
    await new Promise((r) => setTimeout(r, 5));
  });

  it('Up stack on an exited card posts up and opens the operation', async () => {
    alertsBody = [alert({ kind: 'exited', service: 'app', message: 'app exited (1)' })];
    install([stack('web', { status: 'partial' })], {
      extra: { 'POST /api/stacks/web/actions/up': { body: { id: 'op9', stack: 'web', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' } } },
    });
    await mount();
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Up stack' }));
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/stacks/web?tab=operations&op=op9'));
    expect(fake.calls.filter((c) => c.path === '/api/stacks/web/actions/up')).toHaveLength(1);
  });

  it('while its action is in flight the card button is aria-busy', async () => {
    alertsBody = [alert({ kind: 'exited', service: 'app', message: 'app exited (1)' })];
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    install([stack('web', { status: 'partial' })], {
      extra: {
        'POST /api/stacks/web/actions/up': async () => {
          await gate;
          return { body: { id: 'op9', stack: 'web', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' } };
        },
      },
    });
    await mount();
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Up stack' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Up stack' }).getAttribute('aria-busy')).toBe('true'));
    release();
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/stacks/web?tab=operations&op=op9'));
  });

  it('a busy stack disables its card and its row together', async () => {
    alertsBody = [alert()];
    const running: Operation = { id: 'op1', stack: 'web', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' };
    install([stack('web')], { ops: [running] });
    await mount();
    const card = await screen.findByRole('button', { name: 'Restart stack' });
    const row = screen.getByRole('button', { name: 'Restart web' });
    await waitFor(() => expect(card.getAttribute('aria-disabled')).toBe('true'));
    expect(row.getAttribute('aria-disabled')).toBe('true');
    expect(card.getAttribute('title')).toBe(row.getAttribute('title'));
    expect(card.getAttribute('title')).toBeTruthy();
  });
});
