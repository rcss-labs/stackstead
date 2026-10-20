import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse, type RouteHandler } from '../../vitest-setup';
import { findRoute } from '../routes';
import { NetworksScreen } from './NetworksScreen';
import { cards, mountAt, routes, row } from './networksTestKit';

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
function mount(response: FakeResponse | RouteHandler) {
  fake = installFakeFetch(routes(response));
  return { ...mountAt('/networks'), user: userEvent.setup() };
}
afterEach(() => {
  cleanup();
  fake?.restore();
  toasts.reset();
});
const names = () => cards().map((card) => within(card).getByRole('heading', { level: 2 }).textContent);

it('lists sorted networks, the summary, fields and owner tags', async () => {
  mount({ body: [row('z'), row('d', { project: 'legacy' }), row('c', { stack: 'my stack' }), row('bridge', { builtin: true, driver: 'macvlan', subnets: ['10.0.0.0/24', '10.1.0.0/24'], gateways: ['10.0.0.1'], parent: 'eth0' })] });
  await screen.findByText('4 networks');
  expect(names()).toEqual(['bridge', 'c', 'd', 'z']);
  const lead = screen.getByText('4 networks').closest('p')!;
  for (const part of ['1 owned by stacks', '1 LAN network', '1 built-in']) expect(lead.textContent).toContain(part);
  const first = within(cards()[0]);
  for (const text of ['macvlan', '10.0.0.0/24', '+1', '10.0.0.1', 'eth0', 'built-in', 'No containers attached']) expect(first.getByText(text)).toBeTruthy();
  expect(first.getByText('+1').getAttribute('title')).toBe('10.0.0.0/24, 10.1.0.0/24');
  expect(screen.getByRole('link', { name: 'stack my stack' }).getAttribute('href')).toBe('/stacks/my%20stack');
  expect(screen.getByText('compose legacy')).toBeTruthy();
  expect(screen.getByText('unmanaged')).toBeTruthy();
  expect(within(cards()[3]).getAllByText('—')).toHaveLength(3);
  expect(cards()[3].textContent).toContain('No containers attached');
});

it('lists attached containers with encoded links and IP fallback', async () => {
  mount({ body: [row('lan', { containers: [{ name: 'my db', ipv4: '10.0.0.2' }, { name: '<web>', ipv4: '' }] })] });
  await screen.findByText('1 network');
  const card = within(cards()[0]);
  expect(card.getByRole('link', { name: 'my db' }).getAttribute('href')).toBe('/containers/my%20db');
  expect(card.getByRole('link', { name: '<web>' }).getAttribute('href')).toBe('/containers/%3Cweb%3E');
  expect(card.getByText('10.0.0.2')).toBeTruthy();
  expect(within(card.getByRole('link', { name: '<web>' }).parentElement!).getByText('—')).toBeTruthy();
  expect(card.queryByRole('button', { name: 'Show containers on lan' })).toBeNull();
});

it('built-in networks fold their containers behind today\'s toggle', async () => {
  const { user } = mount({ body: [row('bridge', { builtin: true, containers: [{ name: 'my db', ipv4: '10.0.0.2' }] })] });
  const toggle = await screen.findByRole('button', { name: 'Show containers on bridge' });
  expect(toggle.getAttribute('title')).toBe('Show containers on bridge');
  expect(toggle.getAttribute('aria-expanded')).toBe('false');
  expect(cards()[0].querySelector('dl')).toBeNull();
  expect(screen.queryByRole('link', { name: 'my db' })).toBeNull();
  await user.click(toggle);
  expect(toggle.getAttribute('aria-expanded')).toBe('true');
  const list = document.getElementById(toggle.getAttribute('aria-controls')!)!;
  expect(within(list).getByRole('link', { name: 'my db' })).toBeTruthy();
  expect(list.textContent).toContain('10.0.0.2');
  await user.click(toggle);
  expect(screen.queryByRole('link', { name: 'my db' })).toBeNull();
});

it('soft-disables removal with discoverable reasons', async () => {
  const { user } = mount({ body: [row('a', { builtin: true }), row('b', { stack: 'shop' }), row('c', { containers: [{ name: 'web', ipv4: '' }] })] });
  await screen.findByText('3 networks');
  for (const [i, reason] of ['Built-in networks cannot be removed', 'Owned by stack shop', 'In use by 1 container'].entries()) {
    const button = within(cards()[i]).getByRole('button', { name: 'Remove' });
    expect(button.getAttribute('aria-disabled')).toBe('true');
    expect(button.getAttribute('title')).toBe(reason);
    expect(document.getElementById(button.getAttribute('aria-describedby')!)?.textContent).toBe(reason);
    await user.click(button);
  }
  expect(screen.queryByRole('alertdialog')).toBeNull();
  expect(fake.calls.some((c) => c.method === 'DELETE')).toBe(false);
});

it.each([204, 409])('confirms and deletes once, then toasts and refreshes (%s)', async (status) => {
  const { user } = mount({ body: [row('free')] });
  await screen.findByText('1 network');
  fake.restore();
  let release!: (res: FakeResponse) => void;
  fake = installFakeFetch({
    'DELETE /api/networks/free': () => new Promise((resolve) => { release = resolve; }),
    'GET /api/networks': { body: status === 204 ? [] : [row('free', { containers: [{ name: 'web', ipv4: '' }] })] },
  });
  await user.click(screen.getByRole('button', { name: 'Remove' }));
  const dialog = screen.getByRole('alertdialog', { name: 'Remove network `free`?' });
  expect(dialog.textContent).toContain('Docker deletes the network. Containers can only attach again once it is recreated.');
  const confirm = within(dialog).getByRole('button', { name: 'Remove' });
  // Both clicks hit the same mounted button inside one act, before React can re-render the dialog away.
  act(() => {
    confirm.click();
    confirm.click();
  });
  expect(fake.calls.filter((c) => c.method === 'DELETE')).toHaveLength(1);
  release(status === 204 ? { status: 204 } : { status: 409, body: { error: 'has active endpoints <web>' } });
  await waitFor(() =>
    expect(get(toasts)).toEqual([expect.objectContaining({ kind: status === 204 ? 'success' : 'danger', message: status === 204 ? 'Network `free` removed' : 'has active endpoints <web>' })]),
  );
  await waitFor(() => expect(fake.calls.some((c) => c.method === 'GET')).toBe(true));
  if (status === 409) await waitFor(() => expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('title')).toBe('In use by 1 container'));
  else await screen.findByText('No networks');
});

it('opens creation and owns its notification and refresh', async () => {
  const { user } = mount({ body: [] });
  await screen.findByText('No networks');
  await user.click(screen.getByRole('button', { name: 'Create network' }));
  const dialog = screen.getByRole('dialog', { name: 'Create network' });
  await user.type(within(dialog).getByLabelText('Name'), 'new');
  await user.click(within(dialog).getByRole('button', { name: 'Create network' }));
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: 'success', message: 'Network `new` created' })]));
  await waitFor(() => expect(fake.calls.filter((c) => c.path === '/api/networks' && c.method === 'GET')).toHaveLength(2));
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
  await screen.findByText('No networks');
  await new Promise((resolve) => setTimeout(resolve, 20));
  expect(fake.calls.filter((c) => c.path === '/api/networks' && c.method === 'GET')).toHaveLength(1);
});

it('the route table titles /networks as Networks', () => {
  expect(findRoute('/networks')?.title).toBe('Networks');
});
