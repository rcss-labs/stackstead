import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from '$lib/store';
import { afterEach, expect, it, vi } from 'vitest';
import type { Network } from '$lib/api/types';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../vitest-setup';
import NetworkList from './NetworkList.svelte';
vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const store = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  return { ...(await orig<typeof import('$lib/stores/live')>()), live: { subscribe: store.subscribe }, refreshStatus: vi.fn() };
});
const row = (name: string, over: Partial<Network> = {}): Network => ({ id: name, name, driver: 'bridge', scope: 'local', subnets: [], gateways: [], ipRange: '', parent: '', ipvlanMode: '', internal: false, builtin: false, project: '', stack: '', containers: [], ...over });
let fake: FakeFetch;
function mount(response: FakeResponse | (() => FakeResponse)) {
  fake = installFakeFetch({ 'GET /api/networks': response, 'GET /api/host/interfaces': { body: [] }, 'POST /api/networks': { status: 201, body: row('new') } });
  return { ...render(NetworkList), user: userEvent.setup() };
}
afterEach(() => { fake?.restore(); toasts.reset(); });
const rows = () => screen.getAllByRole('row').slice(1);
it('lists sorted networks, fields, owner chips and responsive cell labels', async () => {
  mount({ body: [row('z'), row('d', { project: 'legacy' }), row('c', { stack: 'my stack' }), row('bridge', { builtin: true, driver: 'macvlan', subnets: ['10.0.0.0/24', '10.1.0.0/24'], gateways: ['10.0.0.1'], parent: 'eth0' })] });
  await screen.findByText('4 networks');
  expect(rows().map(r => r.querySelector('[data-label="Name"]')?.textContent?.trim())).toEqual(['bridge', 'c', 'd', 'z']);
  for (const text of ['macvlan', '10.0.0.0/24', '+1', '10.0.0.1', 'eth0', '0', 'built-in']) expect(within(rows()[0]).getByText(text)).toBeTruthy();
  expect(screen.getByRole('link', { name: 'stack my stack' }).getAttribute('href')).toBe('/stacks/my%20stack');
  expect(screen.getByText('compose legacy')).toBeTruthy(); expect(screen.getByText('unmanaged')).toBeTruthy();
  expect(within(rows()[3]).getAllByText('—')).toHaveLength(3);
  for (const cell of screen.getAllByRole('cell')) expect(cell.getAttribute('data-label')).toBeTruthy();
});
it('expands containers with encoded links, IP fallback and ARIA relationship', async () => {
  const { user } = mount({ body: [row('lan', { containers: [{ name: 'my db', ipv4: '10.0.0.2' }, { name: '<web>', ipv4: '' }] })] });
  const toggle = await screen.findByRole('button', { name: 'Show containers on lan' });
  expect(toggle.getAttribute('title')).toBe('Show containers on lan'); expect(toggle.getAttribute('aria-expanded')).toBe('false');
  await user.click(toggle); expect(toggle.getAttribute('aria-expanded')).toBe('true');
  const detail = document.getElementById(toggle.getAttribute('aria-controls')!)!;
  expect(within(detail).getByRole('link', { name: 'my db' }).getAttribute('href')).toBe('/containers/my%20db');
  expect(within(detail).getByRole('link', { name: '<web>' }).getAttribute('href')).toBe('/containers/%3Cweb%3E');
  expect(detail.textContent).toContain('→ 10.0.0.2'); expect(detail.textContent).toContain('→ —');
  await user.click(toggle); expect(screen.queryByRole('link', { name: 'my db' })).toBeNull();
});
it('soft-disables removal with discoverable reasons', async () => {
  const { user } = mount({ body: [row('a', { builtin: true }), row('b', { stack: 'shop' }), row('c', { containers: [{ name: 'web', ipv4: '' }] })] });
  await screen.findByText('3 networks');
  for (const [i, reason] of ['Built-in networks cannot be removed', 'Owned by stack shop', 'In use by 1 container'].entries()) {
    const button = within(rows()[i]).getByRole('button', { name: 'Remove' });
    expect(button.getAttribute('aria-disabled')).toBe('true'); expect(button.getAttribute('title')).toBe(reason);
    expect(document.getElementById(button.getAttribute('aria-describedby')!)?.textContent).toBe(reason); await user.click(button);
  }
  expect(screen.queryByRole('alertdialog')).toBeNull(); expect(fake.calls.some(c => c.method === 'DELETE')).toBe(false);
});
it.each([204, 409])('confirms and deletes once, then toasts and refreshes (%s)', async status => {
  const { user } = mount({ body: [row('free')] }); await screen.findByText('1 networks'); fake.restore();
  let release!: (res: FakeResponse) => void;
  fake = installFakeFetch({ 'DELETE /api/networks/free': () => new Promise(resolve => { release = resolve; }), 'GET /api/networks': { body: status === 204 ? [] : [row('free', { containers: [{ name: 'web', ipv4: '' }] })] } });
  await user.click(screen.getByRole('button', { name: 'Remove' }));
  const dialog = screen.getByRole('alertdialog', { name: 'Remove network `free`?' });
  expect(dialog.textContent).toContain('Docker deletes the network. Containers can only attach again once it is recreated.');
  const confirm = within(dialog).getByRole('button', { name: 'Remove' }); await fireEvent.click(confirm); await fireEvent.click(confirm);
  expect(fake.calls.filter(c => c.method === 'DELETE')).toHaveLength(1);
  release(status === 204 ? { status: 204 } : { status: 409, body: { error: 'has active endpoints <web>' } });
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: status === 204 ? 'success' : 'danger', message: status === 204 ? 'Network `free` removed' : 'has active endpoints <web>' })]));
  await waitFor(() => expect(fake.calls.some(c => c.method === 'GET')).toBe(true));
  if (status === 409) await waitFor(() => expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('title')).toBe('In use by 1 container')); else await screen.findByText('No networks');
});
it('opens creation and owns its notification and refresh', async () => {
  const { user } = mount({ body: [] }); await screen.findByText('No networks'); await user.click(screen.getByRole('button', { name: 'Create network' }));
  const dialog = screen.getByRole('dialog', { name: 'Create network' }); await user.type(within(dialog).getByLabelText('Name'), 'new');
  await user.click(within(dialog).getByRole('button', { name: 'Create network' }));
  await waitFor(() => expect(get(toasts)).toEqual([expect.objectContaining({ kind: 'success', message: 'Network `new` created' })]));
  await waitFor(() => expect(fake.calls.filter(c => c.path === '/api/networks' && c.method === 'GET')).toHaveLength(2)); expect(screen.queryByRole('dialog')).toBeNull();
});
it('shows loading, error and Retry, then recovers', async () => {
  let fail = true;
  const { user } = mount(() => fail ? { status: 503, body: { error: 'Docker is unreachable' } } : { body: [row('recovered')] });
  expect(screen.getByRole('status')).toBeTruthy(); expect(await screen.findByText('Docker is unreachable')).toBeTruthy(); fail = false;
  await user.click(screen.getByRole('button', { name: 'Retry' })); await screen.findByText('recovered'); expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
});
