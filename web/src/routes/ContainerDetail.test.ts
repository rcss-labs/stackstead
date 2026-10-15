import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ContainerDetail as Detail, ContainerRow } from '$lib/api/types';
import { navigate, route } from '$lib/router';
import * as liveModule from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../vitest-setup';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('svelte/store');
  const live = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return { ...(await orig<typeof import('$lib/stores/live')>()), live: { subscribe: live.subscribe }, changes: { subscribe: changes.subscribe }, refreshStatus: vi.fn(), __changes: changes };
});
const pushChange = (c: { project: string; seq: number; resync?: true }) =>
  (liveModule as unknown as { __changes: { set: (v: unknown) => void } }).__changes.set(c);

import ContainerDetail from './ContainerDetail.svelte';

const ID = 'abc123def456';
const FULL_ID = ID + '0'.repeat(52);

const row = (over: Partial<ContainerRow> = {}): ContainerRow => ({
  id: ID,
  name: 'redis',
  image: 'redis:7',
  state: 'running',
  status: 'Up 1 hour',
  health: 'none',
  exitCode: null,
  createdAt: '2026-01-01T00:00:00Z',
  project: '',
  service: '',
  stack: '',
  managed: 'none',
  ports: [],
  networks: {},
  cpu: null,
  mem: null,
  memLimit: null,
  restarts24h: 0,
  ...over,
});
const detail = (over: Partial<Detail> = {}): Detail => ({
  id: FULL_ID,
  name: 'redis',
  image: 'redis:7',
  state: 'running',
  status: 'Up 1 hour',
  health: 'none',
  startedAt: '2026-10-15T08:00:00Z',
  finishedAt: null,
  exitCode: null,
  restartPolicy: 'unless-stopped',
  restarts24h: 2,
  project: '',
  service: '',
  stack: '',
  managed: 'none',
  ports: ['6379/tcp'],
  networks: { bridge: '172.17.0.2' },
  mounts: [{ type: 'volume', source: 'redis-data', destination: '/data', readOnly: false }],
  labels: { 'com.example.owner': 'me', 'com.docker.compose.project': 'shop' },
  ...over,
});

let fake: FakeFetch;
let stream: FakeStream;
let listRes: FakeResponse | (() => FakeResponse);
let detailRes: FakeResponse | (() => FakeResponse);
const calls = (path: string) => fake.calls.filter((c) => c.method === 'GET' && c.path === path).length;
const streamed = (p: string) => stream.requests.some((r) => r.path.startsWith(p));

beforeEach(() => {
  toasts.reset();
  navigate('/containers/redis');
  listRes = { body: [row()] };
  detailRes = { body: detail() };
  fake = installFakeFetch({
    'GET /api/containers': () => (typeof listRes === 'function' ? listRes() : listRes),
    [`GET /api/containers/${ID}`]: () => (typeof detailRes === 'function' ? detailRes() : detailRes),
    [`POST /api/containers/${ID}/stop`]: { body: row({ state: 'exited' }) },
  });
  stream = installFakeStream({ [`/api/containers/${ID}/logs`]: [] });
});
afterEach(() => {
  stream.restore();
  fake.restore();
});

describe('ContainerDetail', () => {
  it('shows a not-found state with a link to the list when the name is unknown', async () => {
    listRes = { body: [row({ name: 'other', id: 'fff000fff000' })] };
    render(ContainerDetail, { name: 'redis' });
    await screen.findByText('Container not found');
    expect(screen.getByRole('link', { name: /containers/i }).getAttribute('href')).toBe('/containers');
  });

  it('shows the header with image, no-stack meta, actions and a copyable full id', async () => {
    render(ContainerDetail, { name: 'redis' });
    expect(await screen.findByRole('heading', { level: 1, name: 'redis' })).toBeTruthy();
    expect(screen.getAllByText('running').length).toBeGreaterThan(0);
    expect(screen.getByText(/redis:7/)).toBeTruthy();
    expect(screen.getByText(/not in a stack/)).toBeTruthy();
    const group = screen.getByRole('group');
    expect(within(group).getByRole('button', { name: 'Stop redis' })).toBeTruthy();
    expect(within(group).getByRole('button', { name: 'Restart redis' })).toBeTruthy();
    await waitFor(() => expect(screen.getByText(FULL_ID)).toBeTruthy());
    expect(screen.getByRole('button', { name: 'Copy id' })).toBeTruthy();
  });

  it('copies the full id', async () => {
    const user = userEvent.setup(); // installs its own clipboard stub; spy after it
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    render(ContainerDetail, { name: 'redis' });
    await screen.findByText(FULL_ID);
    await user.click(screen.getByRole('button', { name: 'Copy id' }));
    expect(writeText).toHaveBeenCalledWith(FULL_ID);
  });

  it('shows the unregistered project for a compose container outside any registered stack', async () => {
    listRes = { body: [row({ managed: 'compose', project: 'shop' })] };
    render(ContainerDetail, { name: 'redis' });
    await screen.findByRole('heading', { level: 1, name: 'redis' });
    expect(screen.getByText(/project/).textContent).toContain('shop');
    expect(screen.getByText(/unregistered/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Stop redis' })).toBeTruthy();
  });

  it('offers Open stack and no action buttons for a container in a registered stack', async () => {
    listRes = { body: [row({ managed: 'stack', stack: 'my shop', project: 'shop' })] };
    render(ContainerDetail, { name: 'redis' });
    await screen.findByRole('heading', { level: 1, name: 'redis' });
    expect(screen.getByRole('link', { name: 'Open stack' }).getAttribute('href')).toBe('/stacks/my%20shop');
    expect(screen.getByText("Managed by stack my shop; use the stack's actions.")).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^(Start|Stop|Restart) / })).toBeNull();
  });

  it('renders ports, networks, mounts, restart policy and exit info from the details', async () => {
    render(ContainerDetail, { name: 'redis' });
    await screen.findByText('unless-stopped');
    expect(screen.getByText('6379/tcp')).toBeTruthy();
    expect(screen.getByText(/bridge → 172\.17\.0\.2/)).toBeTruthy();
    expect(screen.getByText(/volume redis-data → \/data/)).toBeTruthy();
    expect(screen.getByText('2')).toBeTruthy(); // restarts 24h
  });

  it('lists compose labels first inside a collapsed details element', async () => {
    const { container } = render(ContainerDetail, { name: 'redis' });
    await screen.findByText('unless-stopped');
    const el = container.querySelector('details')!;
    expect(el.open).toBe(false);
    const keys = Array.from(el.querySelectorAll('dt')).map((d) => d.textContent);
    expect(keys).toEqual(['com.docker.compose.project', 'com.example.owner']);
  });

  it('never renders env or command even when the server sends them', async () => {
    detailRes = { body: { ...detail(), env: ['SECRET=hunter2'], cmd: ['redis-server', '--requirepass', 'x'], entrypoint: ['sh'] } };
    const { container } = render(ContainerDetail, { name: 'redis' });
    await screen.findByText('unless-stopped');
    expect(container.textContent).not.toMatch(/hunter2|requirepass|SECRET|entrypoint/i);
  });

  it('shows the resources box from the list row with dashes when unknown', async () => {
    listRes = { body: [row({ cpu: 12.5, mem: 52428800, memLimit: 104857600 })] };
    render(ContainerDetail, { name: 'redis' });
    const box = await screen.findByRole('region', { name: /resources/i });
    expect(box.textContent).toContain('12.5%');
    expect(box.textContent).toContain('50.0 MiB');
    expect(box.textContent).toContain('100.0 MiB');
  });

  it('shows dashes when there is no sampler reading', async () => {
    render(ContainerDetail, { name: 'redis' });
    const box = await screen.findByRole('region', { name: /resources/i });
    expect(box.textContent).toMatch(/CPU\s*—/);
    expect(box.textContent).toMatch(/Memory\s*—/);
  });

  it('switches tabs through ?tab= and mounts logs only while active', async () => {
    render(ContainerDetail, { name: 'redis' });
    await screen.findByText('unless-stopped');
    expect(streamed(`/api/containers/${ID}/logs`)).toBe(false);
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Logs' }));
    await waitFor(() => expect(get(route).query.get('tab')).toBe('logs'));
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    expect(screen.queryByLabelText('Service')).toBeNull();
    const signal = stream.requests.find((r) => r.path.startsWith(`/api/containers/${ID}/logs`))!.signal!;
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Details' }));
    await waitFor(() => expect(signal.aborted).toBe(true));
  });

  it('opens the logs viewer for ?tab=logs', async () => {
    navigate('/containers/redis?tab=logs');
    render(ContainerDetail, { name: 'redis' });
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    expect(screen.queryByText('unless-stopped')).toBeNull();
  });

  it('refetches details on a change for its project, any change without a project, and resync', async () => {
    listRes = { body: [row({ project: 'shop', managed: 'compose' })] };
    render(ContainerDetail, { name: 'redis' });
    await screen.findByText('unless-stopped');
    expect(calls(`/api/containers/${ID}`)).toBe(1);
    pushChange({ project: 'other', seq: 1 });
    await new Promise((r) => setTimeout(r, 500));
    expect(calls(`/api/containers/${ID}`)).toBe(1);
    pushChange({ project: 'shop', seq: 2 });
    await waitFor(() => expect(calls(`/api/containers/${ID}`)).toBe(2));
    pushChange({ project: 'x', seq: 3, resync: true });
    await waitFor(() => expect(calls(`/api/containers/${ID}`)).toBe(3));
  });

  it('refetches on any change when the container has no project', async () => {
    render(ContainerDetail, { name: 'redis' });
    await screen.findByText('unless-stopped');
    pushChange({ project: 'whatever', seq: 5 });
    await waitFor(() => expect(calls(`/api/containers/${ID}`)).toBe(2));
  });

  it('refetches details when the Details tab becomes active again', async () => {
    navigate('/containers/redis?tab=logs');
    render(ContainerDetail, { name: 'redis' });
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    expect(calls(`/api/containers/${ID}`)).toBe(0);
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Details' }));
    await waitFor(() => expect(calls(`/api/containers/${ID}`)).toBe(1));
  });

  it('re-resolves the id after the container is recreated', async () => {
    const NEW = '999888777666';
    fake.restore();
    fake = installFakeFetch({
      'GET /api/containers': () => listRes as FakeResponse,
      [`GET /api/containers/${ID}`]: { body: detail() },
      [`GET /api/containers/${NEW}`]: { body: detail({ id: NEW + '0'.repeat(52), restartPolicy: 'always' }) },
    });
    render(ContainerDetail, { name: 'redis' });
    await screen.findByText('unless-stopped');
    listRes = { body: [row({ id: NEW })] };
    pushChange({ project: '', seq: 9, resync: true });
    await screen.findByText('always');
    expect(screen.getByRole('heading', { level: 1, name: 'redis' })).toBeTruthy();
    expect(screen.queryByText('This container no longer exists')).toBeNull();
  });

  it('remounts the logs stream on the new id after recreation', async () => {
    const NEW = '999888777666';
    stream.restore();
    fake.restore();
    fake = installFakeFetch({
      'GET /api/containers': () => listRes as FakeResponse,
      [`GET /api/containers/${ID}`]: { body: detail() },
      [`GET /api/containers/${NEW}`]: { body: detail() },
    });
    stream = installFakeStream({ [`/api/containers/${ID}/logs`]: [], [`/api/containers/${NEW}/logs`]: [] });
    navigate('/containers/redis?tab=logs');
    render(ContainerDetail, { name: 'redis' });
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    listRes = { body: [row({ id: NEW })] };
    pushChange({ project: '', seq: 9, resync: true });
    await waitFor(() => expect(streamed(`/api/containers/${NEW}/logs`)).toBe(true));
  });

  it('shows no longer exists only when the name is gone', async () => {
    render(ContainerDetail, { name: 'redis' });
    await screen.findByRole('heading', { level: 1, name: 'redis' });
    listRes = { body: [row({ name: 'other', id: 'fff000fff000' })] };
    pushChange({ project: '', seq: 1, resync: true });
    await screen.findByText('This container no longer exists');
    expect(screen.queryByText('Container not found')).toBeNull();
    expect(screen.getByRole('link', { name: /back/i }).getAttribute('href')).toBe('/containers');
  });

  it('keeps the page and warns when a later list refresh fails', async () => {
    render(ContainerDetail, { name: 'redis' });
    await screen.findByRole('heading', { level: 1, name: 'redis' });
    listRes = { status: 500, body: { error: 'list exploded' } };
    pushChange({ project: '', seq: 1, resync: true });
    await screen.findByText(/Showing the last known state; list exploded/);
    expect(screen.getByRole('heading', { level: 1, name: 'redis' })).toBeTruthy();
  });

  it('keeps the page while details fail to load and reports the error', async () => {
    detailRes = { status: 500, body: { error: 'boom' } };
    render(ContainerDetail, { name: 'redis' });
    await screen.findByRole('heading', { level: 1, name: 'redis' });
    await screen.findByText(/boom/);
  });
});
