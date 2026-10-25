import { act, render, cleanup, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse, type RouteHandler, type Routes as FakeRoutes } from '../../vitest-setup';
import { findRoute } from '../routes';
import { NetworksScreen } from './NetworksScreen';
import { member, mountAt, routes, row } from './networksTestKit';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const live = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: live.subscribe },
    changes: { subscribe: changes.subscribe },
    refreshStatus: vi.fn(),
    __live: live,
    __changes: changes,
  };
});

let fake: FakeFetch;
function mount(response: FakeResponse | RouteHandler, path = '/networks', extra: FakeRoutes = {}) {
  fake = installFakeFetch(routes(response, extra));
  return { ...mountAt(path), user: userEvent.setup() };
}
afterEach(() => {
  cleanup();
  fake?.restore();
  toasts.reset();
});

const options = () => screen.queryAllByRole('option');
const optionNames = () => options().map((o) => o.querySelector('span span')?.textContent);
const location = () => screen.getByTestId('location').textContent;
const calls = (method: string, path: string) => fake.calls.filter((c) => c.method === method && c.path === path);

const shop = row('shop_default', {
  fullId: 'abcdef0123456789abcdef',
  subnets: ['172.20.0.0/16'],
  gateways: ['172.20.0.1'],
  attachable: true,
  created: '2026-03-14T09:12:00Z',
  containers: [
    member('web', { stack: 'shop', ipv4: '172.20.0.2', mac: '02:42:ac:14:00:02' }),
    member('db', { stack: 'shop', ipv4: '172.20.0.3', state: 'exited', exitCode: 1 }),
    member('cache', { stack: 'media', ipv4: '172.20.0.4' }),
  ],
});
const fleet = [
  row('bridge', { builtin: true, subnets: ['172.17.0.0/16'], gateways: ['172.17.0.1'], containers: [member('probe')] }),
  row('host', { builtin: true, driver: 'host' }),
  row('vault', { internal: true, subnets: ['10.9.0.0/24'] }),
  shop,
  row('spare'),
];

it('lists networks with health, tags, counts and the summary, and asks for a selection', async () => {
  mount({ body: fleet });
  await screen.findByText('5 networks · 2 in use');
  expect(optionNames()).toEqual(['bridge', 'host', 'shop_default', 'spare', 'vault']);
  const bridge = within(options()[0]);
  expect(bridge.getByText('system')).toBeTruthy();
  expect(bridge.getByText('bridge · 172.17.0.0/16')).toBeTruthy();
  expect(bridge.getByLabelText('1 containers').textContent).toBe('1');
  expect(within(options()[4]).getByText('internal')).toBeTruthy();
  expect(within(options()[3]).getByText('bridge · no subnet')).toBeTruthy();
  const leds = options().map((o) => o.querySelector('[data-tone]')?.getAttribute('data-tone'));
  expect(leds).toEqual(['success', 'faint', 'danger', 'faint', 'faint']);
  expect(screen.getByRole('heading', { name: 'Select a network' })).toBeTruthy();
});

it('filters by chip and query, keeping the selection and the filter across selections', async () => {
  const { user } = mount({ body: fleet });
  await screen.findByText('5 networks · 2 in use');
  const chip = (name: string) => screen.getByRole('button', { name: new RegExp(`^${name}`) });
  expect(chip('All').textContent).toBe('All5');
  expect(chip('In use').textContent).toBe('In use2');
  expect(chip('Internal').textContent).toBe('Internal1');
  expect(chip('Unused').textContent).toBe('Unused2');

  await user.click(chip('In use'));
  expect(optionNames()).toEqual(['bridge', 'shop_default']);
  await user.click(options()[1]);
  expect(location()).toBe('/networks/shop_default');
  expect(chip('In use').getAttribute('aria-pressed')).toBe('true');
  expect(options()[1].getAttribute('aria-selected')).toBe('true');

  await user.click(chip('Unused'));
  expect(optionNames()).toEqual(['spare', 'vault']);
  expect(screen.getByRole('heading', { level: 2, name: 'shop_default' })).toBeTruthy();

  await user.click(chip('All'));
  await user.type(screen.getByRole('searchbox', { name: 'Filter networks' }), '10.9');
  expect(optionNames()).toEqual(['vault']);
  await user.type(screen.getByRole('searchbox'), 'x');
  expect(screen.getByText('No networks match this filter.')).toBeTruthy();
});

it('shows the selected network configuration, topology and containers', async () => {
  mount({ body: fleet }, '/networks/shop_default');
  const heading = await screen.findByRole('heading', { level: 2, name: 'shop_default' });
  const header = heading.closest('header')!;
  expect(within(header).getByText('abcdef012345')).toBeTruthy();
  expect(within(header).getByRole('button', { name: 'Copy network ID' })).toBeTruthy();

  const facts = within(document.querySelector<HTMLElement>('dl[aria-label="Configuration"]')!);
  const fact = (key: string) => facts.getByText(key).closest('div')!.querySelector('dd')!.textContent;
  expect([fact('Driver'), fact('Scope'), fact('Subnet'), fact('Gateway'), fact('Internal'), fact('Attachable'), fact('IPv6'), fact('Created')]).toEqual([
    'bridge', 'local', '172.20.0.0/16', '172.20.0.1', 'no', 'yes', 'disabled', '2026-03-14',
  ]);

  expect(screen.getByText('host · eth0 · NAT')).toBeTruthy();
  expect(screen.getByText('172.20.0.0/16 · gw 172.20.0.1')).toBeTruthy();
  const groups = screen.getAllByRole('group').filter((g) => g.closest('[class*="diagram"]'));
  expect(groups.map((g) => [g.getAttribute('aria-label'), within(g).getAllByRole('button').map((b) => b.textContent)])).toEqual([
    ['media', ['cache172.20.0.4']],
    ['shop', ['db172.20.0.3', 'web172.20.0.2']],
  ]);

  const table = within(screen.getByRole('table', { name: 'Connected containers' }));
  const rows = table.getAllByRole('row').slice(1);
  expect(rows.map((r) => within(r).getAllByRole('cell').slice(0, 5).map((c) => c.textContent))).toEqual([
    ['web', 'shop', '172.20.0.2', '02:42:ac:14:00:02', 'running'],
    ['db', 'shop', '172.20.0.3', '—', 'failed'],
    ['cache', 'media', '172.20.0.4', '—', 'running'],
  ]);
});

it('highlights a container in both the topology and the table, and clears it on a new selection', async () => {
  const { user } = mount({ body: fleet }, '/networks/shop_default');
  await screen.findByRole('heading', { level: 2, name: 'shop_default' });
  const node = () => screen.getAllByRole('button', { name: /^web/ }).find((b) => b.textContent === 'web172.20.0.2')!;
  const tableRow = () => within(screen.getByRole('table')).getAllByRole('row')[1];

  await user.click(node());
  expect(node().getAttribute('aria-pressed')).toBe('true');
  expect(tableRow().getAttribute('aria-selected')).toBe('true');
  await user.click(within(tableRow()).getAllByRole('cell')[2]);
  expect(node().getAttribute('aria-pressed')).toBe('false');
  expect(tableRow().getAttribute('aria-selected')).toBe('false');

  await user.click(within(tableRow()).getByRole('button', { name: 'web' }));
  expect(node().getAttribute('aria-pressed')).toBe('true');
  await user.click(options().find((o) => o.textContent?.includes('bridge'))!);
  await user.click(options().find((o) => o.textContent?.includes('shop_default'))!);
  expect(node().getAttribute('aria-pressed')).toBe('false');
});

it('labels the route and empty states of system, internal and unused networks', async () => {
  const { goTo } = mount({ body: fleet }, '/networks/host');
  await screen.findByRole('heading', { level: 2, name: 'host' });
  expect(screen.getByText('host network stack')).toBeTruthy();
  expect(screen.getByText('No containers attached')).toBeTruthy();
  expect(screen.getByText('Containers started with this network mode appear here.')).toBeTruthy();
  const connect = screen.getByRole('button', { name: 'Connect container' });
  expect(connect.getAttribute('title')).toBe("Containers can't join the host network at runtime");
  expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('title')).toBe("System networks can't be removed");

  goTo('/networks/vault');
  await screen.findByRole('heading', { level: 2, name: 'vault' });
  expect(screen.getByText('no external route · internal').getAttribute('data-warning')).toBe('true');
  expect(screen.getByText('This network is unused and can be removed.')).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('aria-disabled')).toBeNull();

  goTo('/networks/shop_default');
  await screen.findByRole('heading', { level: 2, name: 'shop_default' });
  expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('title')).toBe('Detach 3 containers first');

  goTo('/networks/gone');
  expect(await screen.findByRole('heading', { name: 'Network not found' })).toBeTruthy();
});

it('confirms and removes a network once, then returns to the list', async () => {
  let remaining = fleet;
  const { user } = mount(() => ({ body: remaining }), '/networks/spare', {
    'DELETE /api/networks/spare': () => {
      remaining = fleet.filter((n) => n.name !== 'spare');
      return { status: 204 };
    },
  });
  await user.click(await screen.findByRole('button', { name: 'Remove' }));
  const dialog = screen.getByRole('alertdialog', { name: 'Remove network `spare`?' });
  const confirm = within(dialog).getByRole('button', { name: 'Remove' });
  act(() => {
    confirm.click();
    confirm.click();
  });
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: 'success', message: 'Network `spare` removed' })]));
  expect(calls('DELETE', '/api/networks/spare')).toHaveLength(1);
  await waitFor(() => expect(location()).toBe('/networks'));
  await waitFor(() => expect(optionNames()).not.toContain('spare'));
});

it('reports a refused removal and keeps the network selected', async () => {
  const { user } = mount({ body: fleet }, '/networks/spare', { 'DELETE /api/networks/spare': { status: 409, body: { error: 'has active endpoints' } } });
  await user.click(await screen.findByRole('button', { name: 'Remove' }));
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Remove' }));
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: 'danger', message: 'has active endpoints' })]));
  expect(location()).toBe('/networks/spare');
});

it('detaches a container after confirmation', async () => {
  const { user } = mount({ body: fleet }, '/networks/shop_default', { 'POST /api/networks/shop_default/disconnect': { status: 204 } });
  await screen.findByRole('heading', { level: 2, name: 'shop_default' });
  const before = calls('GET', '/api/networks').length;
  await user.click(screen.getByRole('button', { name: 'Detach db' }));
  expect(screen.getByRole('table').querySelector('[aria-selected="true"]')).toBeNull();
  await user.click(within(screen.getByRole('alertdialog', { name: 'Detach `db`?' })).getByRole('button', { name: 'Detach' }));
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: 'success', message: 'Detached `db` from `shop_default`' })]));
  expect(calls('POST', '/api/networks/shop_default/disconnect').map((c) => c.body)).toEqual([{ container: 'db' }]);
  await waitFor(() => expect(calls('GET', '/api/networks').length).toBeGreaterThan(before));
});

it('connects a container that is not yet on the network', async () => {
  const containers = ['web', 'worker', 'api'].map((name) => ({ id: `${name}-id`, name }));
  const { user } = mount({ body: fleet }, '/networks/shop_default', {
    'GET /api/containers': { body: containers },
    'POST /api/networks/shop_default/connect': { status: 204 },
  });
  await user.click(await screen.findByRole('button', { name: 'Connect container' }));
  const dialog = within(screen.getByRole('dialog', { name: 'Connect a container to `shop_default`' }));
  const select = await dialog.findByLabelText('Container');
  expect([...select.querySelectorAll('option')].map((o) => o.textContent)).toEqual(['api', 'worker']);
  await user.selectOptions(select, 'worker');
  await user.click(dialog.getByRole('button', { name: 'Connect' }));
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: 'success', message: 'Connected `worker` to `shop_default`' })]));
  expect(calls('POST', '/api/networks/shop_default/connect').map((c) => c.body)).toEqual([{ container: 'worker' }]);
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('prunes unused networks after confirmation and leaves a pruned selection', async () => {
  const { user } = mount({ body: fleet }, '/networks/spare', { 'POST /api/networks/prune': { body: { removed: ['spare', 'vault'] } } });
  await screen.findByRole('heading', { level: 2, name: 'spare' });
  await user.click(screen.getByRole('button', { name: 'Prune unused' }));
  await user.click(within(screen.getByRole('alertdialog', { name: 'Prune unused networks?' })).getByRole('button', { name: 'Prune' }));
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: 'success', message: 'Removed 2 unused networks' })]));
  await waitFor(() => expect(location()).toBe('/networks'));
});

it('inspects the selected network as JSON', async () => {
  const { user } = mount({ body: fleet }, '/networks/vault');
  await user.click(await screen.findByRole('button', { name: 'Inspect' }));
  const dialog = screen.getByRole('dialog', { name: 'Inspect `vault`' });
  expect(JSON.parse(dialog.querySelector('pre')!.textContent!)).toMatchObject({ name: 'vault', internal: true });
});

it('opens creation and owns its notification and refresh', async () => {
  const { user } = mount({ body: [] });
  await screen.findByText('0 networks · 0 in use');
  await user.click(screen.getByRole('button', { name: 'Create network' }));
  const dialog = screen.getByRole('dialog', { name: 'Create network' });
  await user.type(within(dialog).getByLabelText('Name'), 'new');
  await user.click(within(dialog).getByRole('button', { name: 'Create network' }));
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: 'success', message: 'Network `new` created' })]));
  await waitFor(() => expect(calls('GET', '/api/networks')).toHaveLength(2));
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('shows loading, error and Retry, then recovers', async () => {
  let fail = true;
  let open!: () => void;
  const gate = new Promise<void>((resolve) => { open = resolve; });
  const { user } = mount(async () => {
    if (!fail) return { body: [row('recovered')] };
    await gate; // hold the first response so the loading state stays observable
    return { status: 503, body: { error: 'Docker is unreachable' } };
  });
  // Scoped to the screen: the router probe <output> also has the status role. The route is lazy, so wait for it.
  const region = await screen.findByRole('region', { name: 'Networks' });
  expect(within(region).getByRole('status')).toBeTruthy();
  open();
  expect(await screen.findByText('Docker is unreachable')).toBeTruthy();
  fail = false;
  await user.click(screen.getByRole('button', { name: 'Retry' }));
  await screen.findByText('recovered');
  expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
});

it('fetches the list once under StrictMode', async () => {
  fake = installFakeFetch(routes({ body: [] }));
  render(
    <StrictMode>
      <MemoryRouter initialEntries={['/networks']}>
        <Routes>
          <Route path="/networks" element={<NetworksScreen />} />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
  await screen.findByText('0 networks · 0 in use');
  await new Promise((resolve) => setTimeout(resolve, 20));
  expect(calls('GET', '/api/networks')).toHaveLength(1);
});

it('the route table titles both network paths as Networks', () => {
  expect(findRoute('/networks')?.title).toBe('Networks');
  expect(findRoute('/networks/shop')?.title).toBe('Networks');
});
