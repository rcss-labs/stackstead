import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ContainerRow } from '$lib/api/types';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../vitest-setup';
import ContainerList from './ContainerList.svelte';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('svelte/store');
  const store = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  return { ...(await orig<typeof import('$lib/stores/live')>()), live: { subscribe: store.subscribe }, refreshStatus: vi.fn() };
});

const row = (name: string, over: Partial<ContainerRow> = {}): ContainerRow => ({
  id: name.padEnd(12, '0').slice(0, 12),
  name,
  image: 'nginx:1',
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

let fake: FakeFetch;
async function mount(res: FakeResponse | (() => FakeResponse)) {
  fake = installFakeFetch({ 'GET /api/containers': res });
  const view = render(ContainerList);
  return { view, user: userEvent.setup() };
}
const bodyRows = () => screen.getAllByRole('row').slice(1);

afterEach(() => {
  toasts.reset();
  fake?.restore();
});

describe('ContainerList', () => {
  it('shows a spinner then the heading with total and running counts', async () => {
    await mount({ body: [row('a'), row('b', { state: 'exited', exitCode: 0 }), row('c')] });
    expect(screen.getByRole('heading', { level: 1, name: 'Containers' })).toBeTruthy();
    await screen.findByText('3 · 2 running');
  });

  it('sorts problems first, then running, then stopped, by name', async () => {
    await mount({
      body: [
        row('stopped', { state: 'exited', exitCode: 0 }),
        row('run-b'),
        row('crash', { state: 'exited', exitCode: 2 }),
        row('run-a'),
      ],
    });
    await screen.findByRole('link', { name: 'crash' });
    expect(bodyRows().map((r) => within(r).getAllByRole('link')[0].textContent)).toEqual(['crash', 'run-a', 'run-b', 'stopped']);
  });

  it('renders name link, short id, image title, state badge, health, status', async () => {
    await mount({ body: [row('api', { image: 'ghcr.io/acme/api:2', health: 'unhealthy', status: 'Up 2 minutes (unhealthy)' })] });
    const r = (await screen.findAllByRole('row'))[1];
    expect(within(r).getByRole('link', { name: 'api' }).getAttribute('href')).toBe('/containers/api');
    expect(within(r).getByText('api000000000')).toBeTruthy();
    expect(within(r).getByText('ghcr.io/acme/api:2').getAttribute('title')).toBe('ghcr.io/acme/api:2');
    expect(within(r).getByText('running')).toBeTruthy();
    expect(within(r).getByText('unhealthy')).toBeTruthy();
    expect(within(r).getByText('Up 2 minutes (unhealthy)')).toBeTruthy();
  });

  it('links Logs to the logs tab with an encoded name', async () => {
    await mount({ body: [row('my db')] });
    const r = (await screen.findAllByRole('row'))[1];
    expect(within(r).getByRole('link', { name: 'my db' }).getAttribute('href')).toBe('/containers/my%20db');
    expect(within(r).getByRole('link', { name: 'Logs' }).getAttribute('href')).toBe('/containers/my%20db?tab=logs');
  });

  it('shows the stack link, the unregistered project tag, or a dash', async () => {
    await mount({
      body: [
        row('a', { managed: 'stack', stack: 'shop', project: 'shop' }),
        row('b', { managed: 'compose', project: 'legacy' }),
        row('c'),
      ],
    });
    await screen.findByRole('link', { name: 'a' });
    const [a, b, c] = bodyRows();
    expect(within(a).getByRole('link', { name: 'shop' }).getAttribute('href')).toBe('/stacks/shop');
    expect(within(b).getByText('legacy')).toBeTruthy();
    expect(within(b).getByText('unregistered')).toBeTruthy();
    expect(within(c).getByText('—', { selector: '[data-label="Stack"] *' })).toBeTruthy();
  });

  it('shows the first two ports and +N with all ports in the title', async () => {
    await mount({ body: [row('a', { ports: ['80/tcp', '443/tcp', '8080/tcp', '9000/tcp'] })] });
    const r = (await screen.findAllByRole('row'))[1];
    expect(within(r).getByText('80/tcp')).toBeTruthy();
    expect(within(r).getByText('443/tcp')).toBeTruthy();
    expect(within(r).queryByText('8080/tcp')).toBeNull();
    expect(within(r).getByText('+2').closest('[title]')!.getAttribute('title')).toBe('80/tcp, 443/tcp, 8080/tcp, 9000/tcp');
  });

  it('shows CPU and memory values, a dash when null, and a warning on 3+ restarts', async () => {
    await mount({
      body: [
        row('busy', { cpu: 12.34, mem: 2 * 1024 * 1024, restarts24h: 3 }),
        row('idle', { restarts24h: 2 }),
      ],
    });
    await screen.findByRole('link', { name: 'busy' });
    const [busy, idle] = bodyRows();
    expect(within(busy).getByText('12.3%')).toBeTruthy();
    expect(within(busy).getByText('2.0 MiB')).toBeTruthy();
    expect(within(busy).getByText('3', { selector: '[data-label="Restarts 24h"] *' }).className).toContain('warn');
    expect(within(idle).getByText('—', { selector: '[data-label="CPU"] *' })).toBeTruthy();
    expect(within(idle).getByText('—', { selector: '[data-label="Memory"] *' })).toBeTruthy();
    expect(within(idle).getByText('2', { selector: '[data-label="Restarts 24h"] *' }).className).not.toContain('warn');
  });

  it('offers actions per row: a stack link for stack containers, Stop and Restart otherwise', async () => {
    await mount({ body: [row('owned', { managed: 'stack', stack: 'shop' }), row('free')] });
    await screen.findByRole('link', { name: 'owned' });
    expect(screen.getAllByRole('link', { name: 'Manage in stack →' })).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'Stop owned' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Stop free' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Restart free' })).toBeTruthy();
  });

  it('applies the returned row at once and also refetches, without waiting for the refetch', async () => {
    const { user } = await mount({ body: [row('free')] });
    await screen.findByRole('link', { name: 'free' });
    fake.restore();
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    fake = installFakeFetch({
      'GET /api/containers': async () => {
        await gate; // the refetch is still in flight (or would serve a stale cache)
        return { body: [row('free')] };
      },
      'POST /api/containers/free00000000/stop': { body: row('free', { state: 'exited', exitCode: 0 }) },
    });
    await user.click(screen.getByRole('button', { name: 'Stop free' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop' }));
    await screen.findByRole('button', { name: 'Start free' });
    await waitFor(() => expect(fake.calls.some((c) => c.method === 'GET' && c.path === '/api/containers')).toBe(true));
    release();
  });

  it('refetches after a 204 (container removed by the action) and the row disappears', async () => {
    const { user } = await mount({ body: [row('free'), row('other')] });
    await screen.findByRole('link', { name: 'free' });
    fake.restore();
    fake = installFakeFetch({
      'GET /api/containers': { body: [row('other')] },
      'POST /api/containers/free00000000/stop': { status: 204 },
    });
    await user.click(screen.getByRole('button', { name: 'Stop free' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop' }));
    await waitFor(() => expect(screen.queryByRole('link', { name: 'free' })).toBeNull());
    expect(screen.getByRole('link', { name: 'other' })).toBeTruthy();
  });

  it('filters by chip with counts and aria-pressed', async () => {
    const { user } = await mount({
      body: [row('a'), row('b', { state: 'exited', exitCode: 0 }), row('c', { state: 'restarting' }), row('d', { managed: 'stack', stack: 's' })],
    });
    await screen.findByRole('link', { name: 'a' });
    expect(screen.getByRole('button', { name: /^All\s*4$/ }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('button', { name: /^Running\s*2$/ })).toBeTruthy();
    expect(screen.getByRole('button', { name: /^Stopped\s*2$/ })).toBeTruthy();
    expect(screen.getByRole('button', { name: /^Problems\s*1$/ })).toBeTruthy();
    expect(screen.getByRole('button', { name: /^Unmanaged\s*3$/ })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: /^Problems/ }));
    expect(screen.getByRole('button', { name: /^Problems/ }).getAttribute('aria-pressed')).toBe('true');
    expect(bodyRows().map((r) => within(r).getAllByRole('link')[0].textContent)).toEqual(['c']);
  });

  it('searches by name, image, stack and ports, and says when nothing matches', async () => {
    const { user } = await mount({
      body: [row('web', { image: 'caddy:2', ports: ['8443/tcp'] }), row('db', { managed: 'compose', project: 'legacy' })],
    });
    await screen.findByRole('link', { name: 'web' });
    const search = screen.getByRole('searchbox', { name: 'Search containers' });
    await user.type(search, '8443');
    expect(bodyRows()).toHaveLength(1);
    await user.clear(search);
    await user.type(search, 'legacy');
    expect(within(bodyRows()[0]).getByRole('link', { name: 'db' })).toBeTruthy();
    await user.clear(search);
    await user.type(search, 'zzz');
    expect(screen.getByText('No containers match the current filter.')).toBeTruthy();
  });

  it('shows the empty state', async () => {
    await mount({ body: [] });
    expect(await screen.findByText('No containers on this host.')).toBeTruthy();
  });

  it('shows an error banner with Retry when nothing loaded, and Retry refetches', async () => {
    let fail = true;
    const { user } = await mount(() => (fail ? { status: 503, body: { error: 'Docker is unreachable' } } : { body: [row('a')] }));
    expect(await screen.findByText('Docker is unreachable')).toBeTruthy();
    fail = false;
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('link', { name: 'a' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });

  it('keeps the last list and shows no banner when a later refresh fails', async () => {
    let fail = false;
    await mount(() => (fail ? { status: 503, body: { error: 'Docker is unreachable' } } : { body: [row('a')] }));
    await screen.findByRole('link', { name: 'a' });
    fail = true;
    document.dispatchEvent(new Event('visibilitychange')); // visible again: the controller refetches
    await waitFor(() => expect(fake.calls.filter((c) => c.path === '/api/containers')).toHaveLength(2));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.getByRole('link', { name: 'a' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });
});
