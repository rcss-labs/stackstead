import { act, cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { writable } from '$lib/store';
import type { Change, DockerEvent, HostSnapshot, StackSummary, TopEntry } from '$lib/api/types';
import { startHost } from '$lib/stores/host';
import * as liveModule from '$lib/stores/live';
import { startRunningOps } from '$lib/stores/runningOps';
import { operationsRefresh, startStacks } from '$lib/stores/stacks';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import Overview from '../routes/Overview';
import { ActivityFeed } from './ActivityFeed';
import { TopConsumers } from './TopConsumers';

const liveDefaults = vi.hoisted(() => ({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' }));
const setEvents = (v: unknown[]) => (liveModule as unknown as { __setEvents: (v: unknown) => void }).__setEvents(v);
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
    __setEvents: eventStore.set,
    __set: store.set,
  };
});

const ev = (action: string, container: string, over: Partial<DockerEvent> = {}): DockerEvent => ({
  action, container, project: 'p', service: 's', image: 'img', time: '2026-10-15T10:00:00Z', ...over,
});

const renderIn = (ui: React.ReactElement) => render(<MemoryRouter>{ui}</MemoryRouter>);

describe('ActivityFeed', () => {
  afterEach(cleanup);

  it('prepends newest and renders fifty rows', () => {
    const events = Array.from({ length: 50 }, (_, i) => ev('start', `c-${50 - i}`));
    renderIn(<ActivityFeed events={events} connection="open" />);
    const items = screen.getAllByRole('listitem');
    expect(items).toHaveLength(50);
    expect(items[0].textContent).toContain('c-50');
  });

  it('status shows live when open and reconnecting otherwise', () => {
    const view = renderIn(<ActivityFeed events={[]} connection="open" />);
    expect(screen.getByRole('heading', { level: 2, name: /Activity/ })).toBeTruthy();
    expect(screen.getByText('live')).toBeTruthy();
    expect(screen.queryByText('reconnecting')).toBeNull();
    view.rerender(
      <MemoryRouter>
        <ActivityFeed events={[]} connection="reconnecting" />
      </MemoryRouter>,
    );
    expect(screen.getByText('reconnecting')).toBeTruthy();
    expect(screen.queryByText('live')).toBeNull();
  });

  it('is not a live region and tones the action', () => {
    const { container } = renderIn(<ActivityFeed events={[ev('start', 'a')]} connection="open" />);
    expect(container.querySelector('[aria-live], [role="log"], [role="status"], [role="alert"]')).toBeNull();
    expect(screen.getByText('start').getAttribute('data-tone')).toBe('success');
  });

  it('empty text', () => {
    renderIn(<ActivityFeed events={[]} connection="open" />);
    expect(screen.getByText('Waiting for events…')).toBeTruthy();
  });

  it('time is zero-padded 24h local, midnight is 00', () => {
    const d = new Date(2026, 9, 15, 0, 5, 9);
    renderIn(<ActivityFeed events={[ev('start', 'a', { time: d.toISOString() })]} connection="open" />);
    expect(screen.getByText('00:05:09')).toBeTruthy();
  });

  it('action has a title with the full text', () => {
    renderIn(<ActivityFeed events={[ev('health_status', 'a', { health: 'unhealthy' })]} connection="open" />);
    expect(screen.getByText('unhealthy').getAttribute('title')).toBe('unhealthy');
    expect(screen.getByText('unhealthy').getAttribute('data-tone')).toBe('danger');
  });

  it('subject links to the container page, also without a compose project', () => {
    renderIn(<ActivityFeed events={[ev('start', 'web-1'), ev('start', 'lone', { project: '' })]} connection="open" />);
    expect(screen.getByRole('link', { name: 'web-1' }).getAttribute('href')).toBe('/containers/web-1');
    expect(screen.getByRole('link', { name: 'lone' }).getAttribute('href')).toBe('/containers/lone');
  });

  it('does not link an event that has no container', () => {
    renderIn(<ActivityFeed events={[ev('start', '')]} connection="open" />);
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('die shows exit code with danger tone', () => {
    renderIn(<ActivityFeed events={[ev('die', 'web-1', { exitCode: 137 })]} connection="open" />);
    expect(screen.getByText('web-1 exit 137')).toBeTruthy();
    expect(screen.getByText('die').getAttribute('data-tone')).toBe('danger');
  });
});

describe('TopConsumers', () => {
  afterEach(cleanup);
  const entry = (container: string, project: string, value: number): TopEntry => ({ container, service: 'svc', project, value });
  const top = {
    cpu: [entry('web-1', 'web', 50), entry('db-1', 'web', 25)],
    mem: [entry('cache-1', 'kv', 2 * 1024 ** 3), entry('web-1', 'web', 1024 ** 3)],
  };

  it('toggle switches metric and heading, bars scale to the top value', async () => {
    const { container } = renderIn(<TopConsumers top={top} stackNames={{}} />);
    expect(screen.getByRole('heading', { level: 2 }).textContent).toBe('Top CPU');
    expect(screen.getByRole('button', { name: 'CPU' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByText('50.0%')).toBeTruthy();
    const fills = (metric: string) => [...container.querySelectorAll<HTMLElement>(`[data-bar="${metric}"] > span`)].map((f) => f.style.width);
    expect(fills('cpu')).toEqual(['100%', '50%']);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Memory' }));
    expect(screen.getByRole('heading', { level: 2 }).textContent).toBe('Top memory');
    expect(screen.getByRole('button', { name: 'Memory' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByText('2.0 GiB')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'cache-1 · kv' })).toBeTruthy();
    expect(fills('mem')).toEqual(['100%', '50%']);
  });

  it('label uses the stack name, falling back to project, and omits an empty project', () => {
    renderIn(
      <TopConsumers top={{ cpu: [entry('web-1', 'proj-a', 3), entry('x-1', 'other', 2), entry('lone', '', 1)], mem: [] }} stackNames={{ 'proj-a': 'alpha' }} />,
    );
    expect(screen.getByText('web-1 · alpha')).toBeTruthy();
    expect(screen.getByText('x-1 · other')).toBeTruthy();
    expect(screen.getByText('lone')).toBeTruthy();
  });

  it('a project named like an object prototype key shows its own name', () => {
    renderIn(<TopConsumers top={{ cpu: [entry('x-1', 'constructor', 2)], mem: [] }} stackNames={{}} />);
    expect(screen.getByText('x-1 · constructor')).toBeTruthy();
  });

  it('rows link to the container page', () => {
    renderIn(<TopConsumers top={top} stackNames={{}} />);
    expect(screen.getByRole('link', { name: 'web-1 · web' }).getAttribute('href')).toBe('/containers/web-1');
  });

  it('not loaded shows no empty text', () => {
    renderIn(<TopConsumers top={undefined} stackNames={{}} />);
    expect(screen.queryByText('No samples yet.')).toBeNull();
  });

  it('empty shows No samples yet.', () => {
    renderIn(<TopConsumers top={{ cpu: [], mem: [] }} stackNames={{}} />);
    expect(screen.getByText('No samples yet.')).toBeTruthy();
  });
});

describe('Overview side column', () => {
  let fake: FakeFetch;
  let stops: (() => void)[] = [];

  const stack = (name: string): StackSummary => ({
    name,
    path: `/srv/stacks/${name}/compose.yaml`,
    project: `proj-${name}`,
    origin: 'managed',
    status: 'running',
    serviceCount: 1,
    running: 1,
    health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
    ports: [],
  });
  const host = (): HostSnapshot => ({
    hostname: 'box', os: 'linux', source: 'proc', sampledAt: new Date().toISOString(),
    containers: { total: 1, running: 1, exited: 0, bad: 0 }, cpu: { percent: 18, cores: 8 },
    memory: { used: 1, total: 2, swapUsed: 0, swapTotal: 0 }, disk: { path: '/', free: 1, total: 2 },
    network: { rxBytesPerSec: 0, txBytesPerSec: 0, interface: 'eth0' }, diskIO: null, volumes: null,
    uptime: { seconds: 1, load1: 1, load5: 1, load15: 1 }, restarts24h: null,
  });
  const noStorage = { count: 0, size: null, reclaimable: 0, sizesAt: null, refreshing: false, error: '' };

  function install(items: StackSummary[], extra: Record<string, FakeResponse> = {}) {
    fake = installFakeFetch({
      'GET /api/stacks': { body: items },
      'GET /api/operations?state=running': { body: [] },
      'GET /api/host': { body: host() },
      'GET /api/alerts': { body: [] },
      'GET /api/metrics/summary': { body: { window: '15m', step: 15, from: '', to: '', stacks: {}, top: { cpu: [], mem: [] } } },
      'GET /api/storage': { body: { images: noStorage, containers: noStorage, volumes: noStorage, buildCache: noStorage, topContainers: [], declarationsUnknown: [] } },
      ...extra,
    });
    const changes = writable<(Change & { resync?: true }) | null>(null);
    const list = () =>
      fetch('/api/stacks').then(async (r) => {
        if (!r.ok) throw new Error('x');
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

  afterEach(async () => {
    cleanup();
    stops.forEach((stop) => stop());
    stops = [];
    setEvents([]);
    setLive(liveDefaults);
    fake?.restore();
    await new Promise((r) => setTimeout(r, 5));
  });

  it('side column renders alerts, top consumers and events together', async () => {
    install([stack('alpha')], {
      'GET /api/alerts': { body: [{ kind: 'unhealthy', severity: 'danger', stack: 'alpha', message: 'db is unhealthy', since: '2026-10-15T10:00:00Z' }] },
      'GET /api/metrics/summary': {
        body: { window: '15m', step: 15, from: '', to: '', stacks: {}, top: { cpu: [{ container: 'web-1', service: 'web', project: 'proj-alpha', value: 42 }], mem: [] } },
      },
    });
    mount();
    expect(await screen.findByText('db is unhealthy')).toBeTruthy();
    expect(await screen.findByText('web-1 · alpha')).toBeTruthy();
    expect(screen.getByText('Waiting for events…')).toBeTruthy();
    act(() => setEvents([ev('die', 'web-1', { exitCode: 1 })]));
    expect(await screen.findByText('web-1 exit 1')).toBeTruthy();
    expect(screen.getByText('live')).toBeTruthy();
    act(() => setLive({ ...liveDefaults, connection: 'reconnecting' }));
    expect(await screen.findByText('reconnecting')).toBeTruthy();
  });

  it('empty texts only show once the summary has loaded', async () => {
    install([stack('alpha')], { 'GET /api/metrics/summary': { status: 500, body: { error: 'x' } } });
    mount();
    await screen.findByRole('heading', { level: 1 });
    expect(screen.getByRole('heading', { level: 2, name: 'Top CPU' })).toBeTruthy();
    expect(screen.queryByText('No samples yet.')).toBeNull();
  });

  it('side column shows even when there are no stacks', async () => {
    install([]);
    mount();
    expect(await screen.findByText('No samples yet.')).toBeTruthy();
    const feed = screen.getByRole('heading', { level: 2, name: /Activity/ }).closest('section') as HTMLElement;
    expect(within(feed).getByText('Waiting for events…')).toBeTruthy();
  });
});
