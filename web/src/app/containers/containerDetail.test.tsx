import { act, cleanup, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ContainerDetail as Detail, ContainerRow } from '$lib/api/types';
import * as liveModule from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../../vitest-setup';
import { detail as baseDetail, mountAt, row as baseRow } from './containersTestKit';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const live = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return { ...(await orig<typeof import('$lib/stores/live')>()), live: { subscribe: live.subscribe }, changes: { subscribe: changes.subscribe }, refreshStatus: vi.fn(), __changes: changes };
});
const pushChange = (c: { project: string; seq: number; resync?: true }) =>
  act(() => (liveModule as unknown as { __changes: { set: (v: unknown) => void } }).__changes.set(c));

// The kit pads the row id with zeros to 12 characters; the details carry the 64-character id.
const ID = 'redis0000000';
const FULL_ID = ID + '0'.repeat(52);
const NEW = '999888777666';

const row = (over: Partial<ContainerRow> = {}): ContainerRow => baseRow('redis', { image: 'redis:7', ...over });
const detail = (over: Partial<Detail> = {}): Detail => baseDetail({ id: FULL_ID, ...over });

let fake: FakeFetch;
let stream: FakeStream;
let listRes: FakeResponse | (() => FakeResponse | Promise<FakeResponse>);
let detailRes: FakeResponse | (() => FakeResponse | Promise<FakeResponse>);
const calls = (path: string) => fake.calls.filter((c) => c.method === 'GET' && c.path === path).length;
const streamed = (p: string) => stream.requests.some((r) => r.path.startsWith(p));
const logSignal = (id: string) => stream.requests.find((r) => r.path.startsWith(`/api/containers/${id}/logs`))!.signal!;
const location = () => screen.getByTestId('location').textContent;
const heading = (name: string) => screen.findByRole('heading', { level: 1, name });

const stubWidth = (mobile: boolean) =>
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: mobile && query === '(max-width: 759px)',
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
const letStoresRelease = () => new Promise((r) => setTimeout(r, 5));

beforeEach(() => {
  stubWidth(false);
  toasts.reset();
  listRes = { body: [row()] };
  detailRes = { body: detail() };
  fake = installFakeFetch({
    'GET /api/containers': () => (typeof listRes === 'function' ? listRes() : listRes),
    [`GET /api/containers/${ID}`]: () => (typeof detailRes === 'function' ? detailRes() : detailRes),
    [`POST /api/containers/${ID}/stop`]: { body: row({ state: 'exited' }) },
  });
  stream = installFakeStream({ [`/api/containers/${ID}/logs`]: [] });
});
afterEach(async () => {
  cleanup();
  stream.restore();
  fake.restore();
  vi.unstubAllGlobals();
  await letStoresRelease();
});

describe('ContainerDetail', () => {
  it('shows a not-found state with a link to the list when the name is unknown', async () => {
    listRes = { body: [baseRow('other')] };
    mountAt('/containers/redis');
    await screen.findByText('Container not found');
    expect(screen.getByRole('link', { name: /containers/i }).getAttribute('href')).toBe('/containers');
  });

  it('shows the header with image, no-stack meta, actions and a copyable full id', async () => {
    mountAt('/containers/redis');
    expect(await heading('redis')).toBeTruthy();
    expect(screen.getAllByText('running').length).toBeGreaterThan(0);
    expect(screen.getByText(/redis:7/)).toBeTruthy();
    expect(screen.getByText(/not in a stack/)).toBeTruthy();
    const group = screen.getByRole('group', { name: 'Container actions' });
    expect(within(group).getByRole('button', { name: 'Stop redis' })).toBeTruthy();
    expect(within(group).getByRole('button', { name: 'Restart redis' })).toBeTruthy();
    await waitFor(() => expect(screen.getByTitle(FULL_ID)).toBeTruthy());
    expect(screen.getByTitle(FULL_ID).textContent).toBe(FULL_ID.slice(0, 12));
    expect(screen.queryByText(FULL_ID)).toBeNull();
    expect(screen.getByRole('button', { name: 'Copy id' })).toBeTruthy();
  });

  it('copies the full id', async () => {
    const user = userEvent.setup(); // installs its own clipboard stub; spy after it
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    mountAt('/containers/redis');
    await screen.findByTitle(FULL_ID);
    await user.click(screen.getByRole('button', { name: 'Copy id' }));
    expect(writeText).toHaveBeenCalledWith(FULL_ID);
  });

  it('shows the unregistered project for a compose container outside any registered stack', async () => {
    listRes = { body: [row({ managed: 'compose', project: 'shop' })] };
    mountAt('/containers/redis');
    await heading('redis');
    const meta = screen.getByText(/unregistered/).parentElement!;
    expect(within(meta).getByText('shop')).toBeTruthy();
    expect(meta.textContent).toMatch(/project\s+shop/);
    expect(screen.getByRole('button', { name: 'Stop redis' })).toBeTruthy();
  });

  it('offers Open stack and no action buttons for a container in a registered stack', async () => {
    listRes = { body: [row({ managed: 'stack', stack: 'my shop', project: 'shop' })] };
    mountAt('/containers/redis');
    await heading('redis');
    expect(screen.getByRole('link', { name: 'Open stack' }).getAttribute('href')).toBe('/stacks/my%20shop');
    expect(screen.getByText("Managed by stack my shop; use the stack's actions.")).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^(Start|Stop|Restart) / })).toBeNull();
  });

  it('renders ports, networks, mounts, restart policy and exit info from the details', async () => {
    mountAt('/containers/redis');
    await screen.findByText('unless-stopped');
    expect(screen.getByText('6379/tcp')).toBeTruthy();
    expect(screen.getByText(/bridge → 172\.17\.0\.2/)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'redis-data' }).closest('div')?.textContent).toContain('volume redis-data → /data');
    expect(screen.getByText('2')).toBeTruthy(); // restarts 24h
  });

  it('lists compose labels first inside a collapsed details element', async () => {
    const { container } = mountAt('/containers/redis');
    await screen.findByText('unless-stopped');
    const el = container.querySelector('details')!;
    expect(el.open).toBe(false);
    const keys = Array.from(el.querySelectorAll('dt')).map((d) => d.textContent);
    expect(keys).toEqual(['com.docker.compose.project', 'com.example.owner']);
  });

  it('never renders env or command even when the server sends them', async () => {
    detailRes = { body: { ...detail(), env: ['SECRET=hunter2'], cmd: ['redis-server', '--requirepass', 'x'], entrypoint: ['sh'] } };
    const { container } = mountAt('/containers/redis');
    await screen.findByText('unless-stopped');
    expect(container.textContent).not.toMatch(/hunter2|requirepass|SECRET|entrypoint/i);
  });

  it('shows used / limit when the details carry a memory limit (not the stats limit)', async () => {
    listRes = { body: [row({ cpu: 12.5, mem: 52428800, memLimit: 999999999999 })] };
    detailRes = { body: detail({ memoryLimit: 104857600 }) };
    mountAt('/containers/redis');
    const box = await screen.findByRole('region', { name: /resources/i });
    expect(box.textContent).toContain('12.5%');
    await waitFor(() => expect(box.textContent).toContain('50.0 MiB / 100.0 MiB'));
  });

  it('shows only the used memory when the container has no limit, even if the stats limit is host RAM', async () => {
    listRes = { body: [row({ cpu: 12.5, mem: 52428800, memLimit: 17179869184 })] };
    detailRes = { body: detail({ memoryLimit: null }) };
    mountAt('/containers/redis');
    const box = await screen.findByRole('region', { name: /resources/i });
    await screen.findByText('unless-stopped');
    expect(box.textContent).toContain('50.0 MiB');
    expect(box.textContent).not.toContain('/');
    expect(box.textContent).not.toContain('GiB');
  });

  it('shows dashes when there is no sampler reading', async () => {
    mountAt('/containers/redis');
    const box = await screen.findByRole('region', { name: /resources/i });
    expect(box.textContent).toMatch(/CPU\s*—/);
    expect(box.textContent).toMatch(/Memory\s*—/);
  });

  it('switches tabs through ?tab= and mounts logs only while active', async () => {
    mountAt('/containers/redis');
    await screen.findByText('unless-stopped');
    expect(streamed(`/api/containers/${ID}/logs`)).toBe(false);
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Logs' }));
    await waitFor(() => expect(location()).toBe('/containers/redis?tab=logs'));
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    expect(screen.queryByLabelText('Service filter')).toBeNull();
    const signal = logSignal(ID);
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Details' }));
    await waitFor(() => expect(signal.aborted).toBe(true));
  });

  it('aborts the logs stream when the page is destroyed', async () => {
    const { unmount } = mountAt('/containers/redis?tab=logs');
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    const signal = logSignal(ID);
    expect(signal.aborted).toBe(false);
    unmount();
    expect(signal.aborted).toBe(true);
  });

  it('opens the logs viewer for ?tab=logs', async () => {
    mountAt('/containers/redis?tab=logs');
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    expect(screen.queryByText('unless-stopped')).toBeNull();
  });

  it('applies the action row to the header at once, without waiting for the list refetch', async () => {
    const user = userEvent.setup();
    mountAt('/containers/redis');
    await heading('redis');
    const group = screen.getByRole('group', { name: 'Container actions' });
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    listRes = async () => {
      await gate;
      return { body: [row()] };
    };
    await user.click(within(group).getByRole('button', { name: 'Stop redis' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop' }));
    await within(group).findByRole('button', { name: 'Start redis' });
    release();
  });

  it('refetches details on a change for its project, any change without a project, and resync', async () => {
    listRes = { body: [row({ project: 'shop', managed: 'compose' })] };
    mountAt('/containers/redis');
    await screen.findByText('unless-stopped');
    expect(calls(`/api/containers/${ID}`)).toBe(1);
    await pushChange({ project: 'other', seq: 1 });
    await new Promise((r) => setTimeout(r, 500));
    expect(calls(`/api/containers/${ID}`)).toBe(1);
    await pushChange({ project: 'shop', seq: 2 });
    await waitFor(() => expect(calls(`/api/containers/${ID}`)).toBe(2));
    await pushChange({ project: 'x', seq: 3, resync: true });
    await waitFor(() => expect(calls(`/api/containers/${ID}`)).toBe(3));
  });

  it('refetches on any change when the container has no project', async () => {
    mountAt('/containers/redis');
    await screen.findByText('unless-stopped');
    await pushChange({ project: 'whatever', seq: 5 });
    await waitFor(() => expect(calls(`/api/containers/${ID}`)).toBe(2));
  });

  it('refetches details when the Details tab becomes active again', async () => {
    mountAt('/containers/redis?tab=logs');
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    expect(calls(`/api/containers/${ID}`)).toBe(0);
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Details' }));
    await waitFor(() => expect(calls(`/api/containers/${ID}`)).toBe(1));
  });

  it('re-resolves the id after the container is recreated', async () => {
    fake.restore();
    fake = installFakeFetch({
      'GET /api/containers': () => listRes as FakeResponse,
      [`GET /api/containers/${ID}`]: { body: detail() },
      [`GET /api/containers/${NEW}`]: { body: detail({ id: NEW + '0'.repeat(52), restartPolicy: 'always' }) },
    });
    mountAt('/containers/redis');
    await screen.findByText('unless-stopped');
    listRes = { body: [row({ id: NEW })] };
    await pushChange({ project: '', seq: 9, resync: true });
    await screen.findByText('always');
    expect(screen.getByRole('heading', { level: 1, name: 'redis' })).toBeTruthy();
    expect(screen.queryByText('This container no longer exists')).toBeNull();
  });

  it('never pairs the recreated container with the previous details, not even for one render', async () => {
    fake.restore();
    fake = installFakeFetch({
      'GET /api/containers': () => listRes as FakeResponse,
      [`GET /api/containers/${ID}`]: { body: detail({ memoryLimit: 104857600 }) },
      [`GET /api/containers/${NEW}`]: { body: detail({ id: NEW + '0'.repeat(52), memoryLimit: null }) },
    });
    listRes = { body: [row({ mem: 52428800 })] };
    mountAt('/containers/redis');
    const box = await screen.findByRole('region', { name: /resources/i });
    await waitFor(() => expect(box.textContent).toContain('50.0 MiB / 100.0 MiB'));
    // Every text the memory cell ever held: a commit that mixes the new row with the old limit would leave a trace here.
    const memory = within(box).getByText('Memory').nextElementSibling!;
    const seen: (string | null)[] = [];
    const observer = new MutationObserver((records) => records.forEach((r) => seen.push(r.oldValue)));
    observer.observe(memory, { subtree: true, characterData: true, characterDataOldValue: true });
    listRes = { body: [row({ id: NEW, mem: 62914560 })] };
    await pushChange({ project: '', seq: 9, resync: true });
    await waitFor(() => expect(memory.textContent).toBe('60.0 MiB'));
    observer.disconnect();
    expect(seen).not.toContain('60.0 MiB / 100.0 MiB');
  });

  it('remounts the logs stream on the new id after recreation', async () => {
    stream.restore();
    fake.restore();
    fake = installFakeFetch({
      'GET /api/containers': () => listRes as FakeResponse,
      [`GET /api/containers/${ID}`]: { body: detail() },
      [`GET /api/containers/${NEW}`]: { body: detail() },
    });
    stream = installFakeStream({ [`/api/containers/${ID}/logs`]: [], [`/api/containers/${NEW}/logs`]: [] });
    mountAt('/containers/redis?tab=logs');
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    listRes = { body: [row({ id: NEW })] };
    await pushChange({ project: '', seq: 9, resync: true });
    await waitFor(() => expect(streamed(`/api/containers/${NEW}/logs`)).toBe(true));
  });

  it('shows no longer exists only when the name is gone', async () => {
    mountAt('/containers/redis');
    await heading('redis');
    listRes = { body: [baseRow('other')] };
    await pushChange({ project: '', seq: 1, resync: true });
    await screen.findByText('This container no longer exists');
    expect(screen.queryByText('Container not found')).toBeNull();
    expect(screen.getByRole('link', { name: /back/i }).getAttribute('href')).toBe('/containers');
  });

  it('keeps the page and warns when a later list refresh fails', async () => {
    mountAt('/containers/redis');
    await heading('redis');
    listRes = { status: 500, body: { error: 'list exploded' } };
    await pushChange({ project: '', seq: 1, resync: true });
    await screen.findByText(/Showing the last known state; list exploded/);
    expect(screen.getByRole('heading', { level: 1, name: 'redis' })).toBeTruthy();
  });

  it('keeps the page while details fail to load and reports the error', async () => {
    detailRes = { status: 500, body: { error: 'boom' } };
    mountAt('/containers/redis');
    await heading('redis');
    await screen.findByText(/boom/);
  });

  it('links image by immutable id and volume by mount name, leaving binds as text', async () => {
    listRes = { body: [row({ imageId: 'sha256:' + 'f'.repeat(64) })] };
    detailRes = {
      body: detail({
        mounts: [
          { type: 'volume', name: 'actual-volume', source: '/var/lib/docker/volumes/other/_data', destination: '/data', readOnly: false },
          { type: 'bind', name: '', source: '/host/path', destination: '/host', readOnly: true },
        ],
      }),
    };
    mountAt('/containers/redis');
    expect((await screen.findByRole('link', { name: 'redis:7' })).getAttribute('href')).toBe('/storage/images/' + 'f'.repeat(12));
    expect((await screen.findByRole('link', { name: 'actual-volume' })).getAttribute('href')).toBe('/storage/volumes/actual-volume');
    expect(screen.queryByRole('link', { name: '/host/path' })).toBeNull();
  });

  it('a stale details response never overwrites a newer one', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    let requests = 0;
    detailRes = async () => {
      requests += 1;
      if (requests === 1) {
        await gate;
        return { body: detail({ restartPolicy: 'always' }) };
      }
      return { body: detail({ restartPolicy: 'unless-stopped' }) };
    };
    mountAt('/containers/redis');
    await heading('redis');
    await waitFor(() => expect(requests).toBe(1));
    await pushChange({ project: '', seq: 1, resync: true });
    await screen.findByText('unless-stopped');
    release();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getByText('unless-stopped')).toBeTruthy();
    expect(screen.queryByText('always')).toBeNull();
  });

  it('sets the document title', async () => {
    mountAt('/containers/redis');
    await heading('redis');
    expect(document.title).toBe('redis · Stackstead');
  });

  it('navigating from one container to another re-resolves the row, the title and the stream', async () => {
    const OTHER = baseRow('other');
    listRes = { body: [row(), OTHER] };
    stream.restore();
    fake.restore();
    fake = installFakeFetch({
      'GET /api/containers': () => listRes as FakeResponse,
      [`GET /api/containers/${ID}`]: () => detailRes as FakeResponse,
      [`GET /api/containers/${OTHER.id}`]: { body: detail({ id: OTHER.id + '0'.repeat(52), name: 'other' }) },
    });
    stream = installFakeStream({ [`/api/containers/${ID}/logs`]: [] });
    const view = mountAt('/containers/redis?tab=logs');
    await waitFor(() => expect(streamed(`/api/containers/${ID}/logs`)).toBe(true));
    const signal = logSignal(ID);
    view.goTo('/containers/other');
    await heading('other');
    await waitFor(() => expect(document.title).toBe('other · Stackstead'));
    expect(signal.aborted).toBe(true);
    expect(screen.queryByRole('heading', { level: 1, name: 'redis' })).toBeNull();
  });

  it('the header LED pulses for a problem container', async () => {
    listRes = { body: [row({ health: 'unhealthy' })] };
    const { container } = mountAt('/containers/redis');
    await heading('redis');
    const led = container.querySelector('header [data-pulse]');
    expect(led?.getAttribute('data-pulse')).toBe('true');
    expect(led?.getAttribute('data-tone')).toBe('danger');
  });
});

describe('ContainerDetail terminal entry points', () => {
  it('has a Terminal tab and a Terminal link in the actions group', async () => {
    mountAt('/containers/redis');
    await heading('redis');
    expect(screen.getByRole('tab', { name: 'Terminal' })).toBeTruthy();
    const group = screen.getByRole('group', { name: 'Container actions' });
    expect(within(group).getByRole('link', { name: 'Terminal' }).getAttribute('href')).toBe('/containers/redis?tab=terminal');
  });

  it('shows the not-running state on the Terminal tab of a stopped container', async () => {
    listRes = { body: [row({ state: 'exited', exitCode: 0 })] };
    mountAt('/containers/redis?tab=terminal');
    expect(await screen.findByText("Container isn't running")).toBeTruthy();
  });
});
