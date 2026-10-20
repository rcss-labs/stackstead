import { cleanup, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ContainerRow } from '$lib/api/types';
import * as liveModule from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../../vitest-setup';
import { mountAt, row } from './containersTestKit';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const store = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    changes: { subscribe: changes.subscribe },
    refreshStatus: vi.fn(),
    __changes: changes,
  };
});
const emitChange = (c: { project: string; seq: number; resync?: true }) => (liveModule as unknown as { __changes: { set: (v: unknown) => void } }).__changes.set(c);

const stubWidth = (mobile: boolean) =>
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: mobile && query === '(max-width: 759px)',
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
const letStoresRelease = () => new Promise((r) => setTimeout(r, 5));

const logPath = (r: ContainerRow) => `/api/containers/${r.id}/logs`;
function logEvent(container: string, n: number, text: string) {
  const data = JSON.stringify({ service: 'svc', container, stream: 'stdout', time: '2026-10-14T10:00:00Z', text });
  return `id: ${container}:${n}:1\nevent: log\ndata: ${data}\n\n`;
}
const errorEvent = (container: string, error: string) => `event: error\ndata: ${JSON.stringify({ service: 'svc', container, error })}\n\n`;

let fetchFake: FakeFetch;
let stream: FakeStream;
/** The list answers `list`; every container in `streams` has an open, empty log stream. */
function mount(list: FakeResponse | (() => FakeResponse | Promise<FakeResponse>), streams: ContainerRow[], extra: Record<string, FakeResponse | (() => FakeResponse | Promise<FakeResponse>)> = {}) {
  fetchFake = installFakeFetch({ 'GET /api/containers': list, ...extra });
  stream = installFakeStream(Object.fromEntries(streams.map((r) => [logPath(r), []])));
  const view = mountAt('/containers');
  return { user: userEvent.setup(), ...view };
}
const bodyRows = () => screen.getAllByRole('row').filter((r) => r.hasAttribute('data-container'));
const rowOf = (name: string) => bodyRows().find((r) => r.getAttribute('data-container') === name)!;
const peekRows = () => screen.queryAllByTestId('peek-row');
const section = (name: string) => screen.getByRole('region', { name: `Logs of ${name}` });
const abortedFlags = () => stream.requests.map((r) => r.signal!.aborted);

beforeEach(() => stubWidth(false));
afterEach(async () => {
  cleanup();
  toasts.reset();
  stream?.restore();
  fetchFake?.restore();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  await letStoresRelease();
});

describe('Container peek', () => {
  it('clicking a row opens a peek that streams the last 8 lines of that container', async () => {
    const api = row('api');
    const { user } = mount({ body: [api, row('other')] }, [api]);
    await screen.findByRole('link', { name: 'api' });
    await user.click(rowOf('api'));
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    const path = stream.requests[0].path;
    expect(path).toContain('/api/containers/api000000000/logs');
    expect(path).toContain('tail=8');
    expect(path).toContain('follow=1');
    expect(path).not.toContain('service=');
    stream.push(logPath(api), Array.from({ length: 10 }, (_, i) => logEvent(api.id, i + 1, `line ${i + 1}`)).join(''));
    await waitFor(() => expect(peekRows()).toHaveLength(8));
    expect(peekRows()[0].textContent).toContain('line 3');
    expect(rowOf('api').getAttribute('aria-expanded')).toBe('true');
    expect(screen.getByRole('button', { name: 'Peek logs for api' }).getAttribute('aria-expanded')).toBe('true');
    expect(within(section('api')).getByRole('status').textContent).toContain('streaming');
  });

  it('only one peek is open at a time and opening another releases the first stream', async () => {
    const a = row('a');
    const b = row('b');
    const { user } = mount({ body: [a, b] }, [a, b]);
    await screen.findByRole('link', { name: 'a' });
    await user.click(rowOf('a'));
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    await user.click(rowOf('b'));
    await waitFor(() => expect(stream.requests).toHaveLength(2));
    expect(stream.requests[0].signal!.aborted).toBe(true);
    expect(stream.requests[1].signal!.aborted).toBe(false);
    expect(screen.getAllByRole('region', { name: /^Logs of/ })).toHaveLength(1);
    expect(stream.requests[1].path).toContain(`/api/containers/${b.id}/logs`);
  });

  it('clicking the open row again or its toggle closes the peek and aborts the stream', async () => {
    const a = row('a');
    const { user } = mount({ body: [a] }, [a]);
    await screen.findByRole('link', { name: 'a' });
    await user.click(rowOf('a'));
    await screen.findByRole('region', { name: 'Logs of a' });
    await user.click(rowOf('a'));
    expect(screen.queryByRole('region', { name: 'Logs of a' })).toBeNull();
    expect(abortedFlags()).toEqual([true]);
    await user.click(screen.getByRole('button', { name: 'Peek logs for a' }));
    await screen.findByRole('region', { name: 'Logs of a' });
    await user.click(screen.getByRole('button', { name: 'Peek logs for a' }));
    expect(screen.queryByRole('region', { name: 'Logs of a' })).toBeNull();
    expect(abortedFlags()).toEqual([true, true]);
  });

  it("the peek offers today's actions and Open full logs", async () => {
    const free = row('my db');
    const shop = row('shop-web', { managed: 'stack', stack: 'shop' });
    const { user } = mount({ body: [free, shop] }, [free, shop]);
    await screen.findByRole('link', { name: 'my db' });
    await user.click(rowOf('my db'));
    const own = await screen.findByRole('region', { name: 'Logs of my db' });
    expect(within(own).getByRole('button', { name: 'Stop my db' }).textContent).toBe('Stop');
    expect(within(own).getByRole('button', { name: 'Restart my db' }).textContent).toBe('Restart');
    expect(within(own).getByRole('link', { name: 'Open full logs' }).getAttribute('href')).toBe('/containers/my%20db?tab=logs');
    await user.click(rowOf('shop-web'));
    const managed = await screen.findByRole('region', { name: 'Logs of shop-web' });
    expect(within(managed).getByRole('link', { name: 'Manage in stack →' }).getAttribute('href')).toBe('/stacks/shop');
    expect(within(managed).queryByRole('button')).toBeNull();
  });

  it('the peek header shows the Docker status on desktop and not on mobile', async () => {
    const api = row('api', { health: 'unhealthy', status: 'Up 2 minutes (unhealthy)' });
    const { user } = mount({ body: [api] }, [api]);
    await screen.findByRole('link', { name: 'api' });
    await user.click(rowOf('api'));
    expect(within(await screen.findByRole('region', { name: 'Logs of api' })).getByText('Up 2 minutes (unhealthy)')).toBeTruthy();
    cleanup();
    stream.restore();
    fetchFake.restore();
    await letStoresRelease();
    stubWidth(true);
    const view = mount({ body: [api] }, [api]);
    await screen.findByRole('link', { name: 'api' });
    await view.user.click(rowOf('api'));
    expect(within(await screen.findByRole('region', { name: 'Logs of api' })).queryByText('Up 2 minutes (unhealthy)')).toBeNull();
  });

  it('ERROR and WARN lines carry their tones and new lines are bounded to eight', async () => {
    const api = row('api');
    const { user } = mount({ body: [api] }, [api]);
    await screen.findByRole('link', { name: 'api' });
    await user.click(rowOf('api'));
    await screen.findByRole('region', { name: 'Logs of api' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    stream.push(logPath(api), logEvent(api.id, 1, 'fine') + logEvent(api.id, 2, 'WARN slow') + logEvent(api.id, 3, 'ERROR boom'));
    await waitFor(() => expect(peekRows()).toHaveLength(3));
    expect(peekRows().map((r) => r.getAttribute('data-tone'))).toEqual([null, 'warning', 'danger']);
    stream.push(logPath(api), Array.from({ length: 20 }, (_, i) => logEvent(api.id, i + 10, `more ${i}`)).join(''));
    await waitFor(() => expect(peekRows().at(-1)!.textContent).toContain('more 19'));
    expect(peekRows()).toHaveLength(8);
  });

  it('a stream error is announced in the peek', async () => {
    const api = row('api');
    const { user } = mount({ body: [api] }, [api]);
    await screen.findByRole('link', { name: 'api' });
    await user.click(rowOf('api'));
    await screen.findByRole('region', { name: 'Logs of api' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    stream.push(logPath(api), errorEvent(api.id, 'no such container'));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toBe(`svc (${api.id}): no such container`);
  });

  it('the status reads reconnecting after the stream drops', async () => {
    const api = row('api');
    const { user } = mount({ body: [api] }, [api]);
    await screen.findByRole('link', { name: 'api' });
    await user.click(rowOf('api'));
    const region = await screen.findByRole('region', { name: 'Logs of api' });
    await waitFor(() => expect(within(region).getByRole('status').textContent).toContain('streaming'));
    stream.close(logPath(api));
    await waitFor(() => expect(within(region).getByRole('status').textContent).toBe('reconnecting'));
  });

  it('the peek closes when a chip hides its container', async () => {
    const a = row('a');
    const { user } = mount({ body: [a, row('crash', { state: 'exited', exitCode: 2 })] }, [a]);
    await screen.findByRole('link', { name: 'a' });
    await user.click(rowOf('a'));
    await screen.findByRole('region', { name: 'Logs of a' });
    await user.click(screen.getByRole('button', { name: /^Problems/ }));
    expect(screen.queryByRole('region', { name: /^Logs of/ })).toBeNull();
    expect(abortedFlags()).toEqual([true]);
  });

  it('a recreated container re-opens the peek on its new id', async () => {
    const old = row('redis');
    const fresh = row('redis', { id: 'NEW000000000' });
    let calls = 0;
    const { user } = mount(() => ({ body: [++calls === 1 ? old : fresh] }), [old, fresh]);
    await screen.findByRole('link', { name: 'redis' });
    await user.click(rowOf('redis'));
    await screen.findByRole('region', { name: 'Logs of redis' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    emitChange({ project: '', seq: 9, resync: true });
    await waitFor(() => expect(stream.requests).toHaveLength(2));
    expect(stream.requests[1].path).toContain('/api/containers/NEW000000000/logs');
    expect(stream.requests[0].signal!.aborted).toBe(true);
    expect(screen.getByRole('region', { name: 'Logs of redis' })).toBeTruthy();
  });

  it('leaving the page aborts the peek stream', async () => {
    const a = row('a');
    const { user, goTo } = mount({ body: [a] }, [a]);
    await screen.findByRole('link', { name: 'a' });
    await user.click(rowOf('a'));
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    goTo('/');
    await screen.findByText('home');
    expect(abortedFlags()).toEqual([true]);
  });

  it('Stop from the peek applies the returned row at once and refetches', async () => {
    const free = row('free');
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    let listCalls = 0;
    const { user } = mount(
      async () => {
        if (++listCalls > 1) await gate; // the refetch stays in flight
        return { body: [free] };
      },
      [free],
      { [`POST /api/containers/${free.id}/stop`]: { body: row('free', { state: 'exited', exitCode: 0 }) } },
    );
    await screen.findByRole('link', { name: 'free' });
    await user.click(rowOf('free'));
    const region = await screen.findByRole('region', { name: 'Logs of free' });
    await user.click(within(region).getByRole('button', { name: 'Stop free' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop' }));
    await within(screen.getByRole('region', { name: 'Logs of free' })).findByRole('button', { name: 'Start free' });
    await waitFor(() => expect(listCalls).toBeGreaterThan(1));
    release();
  });

  it('the status says the stream is waiting while the container is not running', async () => {
    const idle = row('idle', { state: 'exited', exitCode: 0 });
    const { user } = mount({ body: [idle] }, [idle]);
    await screen.findByRole('link', { name: 'idle' });
    await user.click(rowOf('idle'));
    const status = within(await screen.findByRole('region', { name: 'Logs of idle' })).getByRole('status');
    await waitFor(() => expect(status.textContent).toBe('waiting - container exited'));
  });

  it('a 204 from the peek refetches and the row disappears', async () => {
    const free = row('free');
    const other = row('other');
    let listCalls = 0;
    const { user } = mount(() => ({ body: ++listCalls === 1 ? [free, other] : [other] }), [free], {
      [`POST /api/containers/${free.id}/stop`]: { status: 204 },
    });
    await screen.findByRole('link', { name: 'free' });
    await user.click(rowOf('free'));
    const region = await screen.findByRole('region', { name: 'Logs of free' });
    await user.click(within(region).getByRole('button', { name: 'Stop free' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop' }));
    await waitFor(() => expect(screen.queryByRole('link', { name: 'free' })).toBeNull());
    expect(screen.queryByRole('region', { name: /^Logs of/ })).toBeNull();
    expect(screen.getByRole('link', { name: 'other' })).toBeTruthy();
  });
});
