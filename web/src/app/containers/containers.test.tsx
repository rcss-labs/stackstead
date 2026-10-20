import { cleanup, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import { findRoute } from '../routes';
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
  };
});

const stubWidth = (mobile: boolean) =>
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: mobile && query === '(max-width: 759px)',
    addEventListener: () => {},
    removeEventListener: () => {},
  }));

let fake: FakeFetch;
function mount(res: FakeResponse | (() => FakeResponse)) {
  fake = installFakeFetch({ 'GET /api/containers': res });
  mountAt('/containers');
  return userEvent.setup();
}
const bodyRows = () => screen.getAllByRole('row').slice(1);
const firstLink = (r: HTMLElement) => within(r).getAllByRole('link')[0].textContent;

beforeEach(() => stubWidth(false));
// useStore releases the media hub on a timer after the last consumer leaves; wait it out so the next width stub is read afresh.
const letStoresRelease = () => new Promise((r) => setTimeout(r, 5));

afterEach(async () => {
  cleanup();
  toasts.reset();
  fake?.restore();
  vi.unstubAllGlobals();
  await letStoresRelease();
});

describe('Containers screen', () => {
  it('shows a spinner then the heading with total and running counts', async () => {
    mount({ body: [row('a'), row('b', { state: 'exited', exitCode: 0 }), row('c')] });
    expect(await screen.findByText('Loading containers')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'Containers' })).toBeTruthy();
    await screen.findByText('3 · 2 running');
  });

  it('sorts problems first, then running, then stopped, by name', async () => {
    mount({ body: [row('stopped', { state: 'exited', exitCode: 0 }), row('run-b'), row('crash', { state: 'exited', exitCode: 2 }), row('run-a')] });
    await screen.findByRole('link', { name: 'crash' });
    expect(bodyRows().map(firstLink)).toEqual(['crash', 'run-a', 'run-b', 'stopped']);
  });

  it('renders name link, image title, state tag and health', async () => {
    mount({ body: [row('api', { image: 'ghcr.io/acme/api:2', health: 'unhealthy', status: 'Up 2 minutes (unhealthy)' })] });
    await screen.findByRole('link', { name: 'api' });
    const r = bodyRows()[0];
    expect(within(r).getByRole('link', { name: 'api' }).getAttribute('href')).toBe('/containers/api');
    // Long names are ellipsised in the grid; the full name stays reachable on hover.
    expect(within(r).getByRole('link', { name: 'api' }).getAttribute('title')).toBe('api');
    expect(within(r).getByText('ghcr.io/acme/api:2').getAttribute('title')).toBe('ghcr.io/acme/api:2');
    expect(within(r).getByText('running')).toBeTruthy();
    expect(within(r).getByText('unhealthy').getAttribute('data-health')).toBe('unhealthy');
    expect(within(r).queryByText('api000000000')).toBeNull();
  });

  it('shows the stack link, the unregistered project tag, or a dash', async () => {
    mount({ body: [row('a', { managed: 'stack', stack: 'shop', project: 'shop' }), row('b', { managed: 'compose', project: 'legacy' }), row('c')] });
    await screen.findByRole('link', { name: 'a' });
    const [a, b, c] = bodyRows();
    expect(within(a).getByRole('link', { name: 'shop' }).getAttribute('href')).toBe('/stacks/shop');
    expect(within(b).getByText('legacy')).toBeTruthy();
    expect(within(b).getByText('unregistered')).toBeTruthy();
    expect(within(c).getByText('—', { selector: '[data-col="stack"] *' })).toBeTruthy();
  });

  it('shows the first two ports and +N with all ports in the title', async () => {
    mount({ body: [row('a', { ports: ['80/tcp', '443/tcp', '8080/tcp', '9000/tcp'] })] });
    await screen.findByRole('link', { name: 'a' });
    const r = bodyRows()[0];
    expect(within(r).getByText('80/tcp')).toBeTruthy();
    expect(within(r).getByText('443/tcp')).toBeTruthy();
    expect(within(r).queryByText('8080/tcp')).toBeNull();
    expect(within(r).getByText('+2').closest('[title]')!.getAttribute('title')).toBe('80/tcp, 443/tcp, 8080/tcp, 9000/tcp');
  });

  it('shows CPU and memory values, a dash when null, and a warning on 3+ restarts', async () => {
    mount({ body: [row('busy', { cpu: 12.34, mem: 2 * 1024 * 1024, restarts24h: 3 }), row('idle', { restarts24h: 2 })] });
    await screen.findByRole('link', { name: 'busy' });
    const [busy, idle] = bodyRows();
    expect(within(busy).getByText('12.3%')).toBeTruthy();
    expect(within(busy).getByText('2.0 MiB')).toBeTruthy();
    expect(within(busy).getByText('3', { selector: '[data-col="restarts"] *' }).getAttribute('data-tone')).toBe('warning');
    expect(within(idle).getByText('—', { selector: '[data-col="cpu"] *' })).toBeTruthy();
    expect(within(idle).getByText('—', { selector: '[data-col="mem"] *' })).toBeTruthy();
    expect(within(idle).getByText('2', { selector: '[data-col="restarts"] *' }).getAttribute('data-tone')).toBeNull();
  });

  it('filters by chip with counts and aria-pressed', async () => {
    const user = mount({
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
    expect(bodyRows().map(firstLink)).toEqual(['c']);
  });

  it('searches by name, image, stack and ports, and says when nothing matches', async () => {
    const user = mount({ body: [row('web', { image: 'caddy:2', ports: ['8443/tcp'] }), row('db', { managed: 'compose', project: 'legacy' })] });
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
    mount({ body: [] });
    expect(await screen.findByText('No containers on this host.')).toBeTruthy();
  });

  it('shows an error banner with Retry when nothing loaded, and Retry refetches', async () => {
    let fail = true;
    const user = mount(() => (fail ? { status: 503, body: { error: 'Docker is unreachable' } } : { body: [row('a')] }));
    expect(await screen.findByText('Docker is unreachable')).toBeTruthy();
    fail = false;
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('link', { name: 'a' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });

  it('keeps the last list and shows no banner when a later refresh fails', async () => {
    let fail = false;
    mount(() => (fail ? { status: 503, body: { error: 'Docker is unreachable' } } : { body: [row('a')] }));
    await screen.findByRole('link', { name: 'a' });
    fail = true;
    document.dispatchEvent(new Event('visibilitychange'));
    await waitFor(() => expect(fake.calls.filter((c) => c.path === '/api/containers')).toHaveLength(2));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.getByRole('link', { name: 'a' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });

  it('each row is a focusable row with a labelled peek toggle', async () => {
    mount({ body: [row('a'), row('b')] });
    await screen.findByRole('link', { name: 'a' });
    const rows = bodyRows();
    expect(rows[0].getAttribute('tabindex')).toBe('0');
    expect(rows[1].getAttribute('tabindex')).toBe('-1');
    expect(screen.getByRole('button', { name: 'Peek logs for a' }).getAttribute('aria-expanded')).toBe('false');
  });

  it('shows the key hints on desktop and hides them on mobile', async () => {
    mount({ body: [row('a')] });
    await screen.findByRole('link', { name: 'a' });
    expect(screen.getByText(/peek logs/)).toBeTruthy();
    cleanup();
    fake.restore();
    await letStoresRelease();
    stubWidth(true);
    mount({ body: [row('a')] });
    await screen.findByRole('link', { name: 'a' });
    expect(screen.queryByText(/peek logs/)).toBeNull();
  });

  it('the mobile row shows the status under the name', async () => {
    stubWidth(true);
    mount({ body: [row('api', { health: 'unhealthy', status: 'Up 2 minutes (unhealthy)' })] });
    await screen.findByRole('link', { name: 'api' });
    const r = bodyRows()[0];
    expect(within(r).getByText('Up 2 minutes (unhealthy)')).toBeTruthy();
    expect(r.querySelector('[data-col="image"]')!.hasAttribute('data-desktop')).toBe(true);
  });

  it('encodes the container name in the row link', async () => {
    mount({ body: [row('my db')] });
    expect((await screen.findByRole('link', { name: 'my db' })).getAttribute('href')).toBe('/containers/my%20db');
  });

  it('gives the header and every row the same ten cells', async () => {
    mount({ body: [row('a')] });
    await screen.findByRole('link', { name: 'a' });
    expect(screen.getAllByRole('columnheader')).toHaveLength(10);
    expect(within(bodyRows()[0]).getAllByRole('cell')).toHaveLength(10);
  });

  it('exposes CPU and memory to assistive tech on the mobile row', async () => {
    stubWidth(true);
    mount({ body: [row('busy', { cpu: 12.34, mem: 2 * 1024 * 1024 })] });
    await screen.findByRole('link', { name: 'busy' });
    expect(within(bodyRows()[0]).getByRole('cell', { name: 'CPU 12.3%, memory 2.0 MiB' })).toBeTruthy();
  });

  it('shows no chips or search while loading, after a first-load error, or when empty', async () => {
    let fail = true;
    const user = mount(() => (fail ? { status: 503, body: { error: 'Docker is unreachable' } } : { body: [] }));
    expect(await screen.findByText('Loading containers')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^All/ })).toBeNull();
    await screen.findByText('Docker is unreachable');
    expect(screen.queryByRole('button', { name: /^All/ })).toBeNull();
    expect(screen.queryByRole('searchbox')).toBeNull();
    fail = false;
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByText('No containers on this host.');
    expect(screen.queryByRole('button', { name: /^All/ })).toBeNull();
    expect(screen.queryByRole('searchbox')).toBeNull();
  });

  it('the route table titles /containers as Containers', () => {
    expect(findRoute('/containers')?.title).toBe('Containers');
  });
});
