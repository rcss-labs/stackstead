import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { CreateNetworkDialog } from './CreateNetworkDialog';
let fake: FakeFetch;
afterEach(() => {
  cleanup();
  fake?.restore();
});
const interfaces = [
  { name: 'eth0', ipv4: '10.0.0.2', subnet: '10.0.0.0/24', gateway: '10.0.0.1', default: true },
  { name: 'enp3s0', ipv4: '192.168.0.2', subnet: '192.168.0.0/24', gateway: '192.168.0.1', default: false },
];
function setup(response: { status?: number; body: unknown } = { body: { name: 'lan' } }, ifaces = interfaces) {
  fake = installFakeFetch({ 'GET /api/host/interfaces': { body: ifaces }, 'POST /api/networks': response });
  const onCreated = vi.fn(), onClose = vi.fn();
  return { ...render(<CreateNetworkDialog open onCreated={onCreated} onClose={onClose} />), onCreated, onClose };
}
const input = (label: string) => screen.getByLabelText(label) as HTMLInputElement;
async function lan() {
  await userEvent.click(screen.getByRole('radio', { name: 'Macvlan' }));
  await screen.findByRole('option', { name: /eth0/ });
}
it('reveals driver controls, autofills the initial default and loads interfaces once', async () => {
  setup(); expect(screen.queryByLabelText('Host interface')).toBeNull(); expect(screen.queryByLabelText('IPvlan mode')).toBeNull();
  await lan(); expect(input('Subnet').value).toBe('10.0.0.0/24'); expect(input('Gateway').value).toBe('10.0.0.1');
  expect(screen.getByRole('note').textContent).toBe("The Docker host itself cannot reach containers on this network. Pick addresses outside your router's DHCP pool, or set an IP range outside it.");
  expect(screen.getByText('Required for macvlan and ipvlan')).toBeTruthy();
  await userEvent.click(screen.getByRole('radio', { name: 'IPvlan' })); expect(screen.getByLabelText('IPvlan mode')).toBeTruthy();
  expect(fake.calls.filter(c => c.path === '/api/host/interfaces')).toHaveLength(1);
});
it('updates autofill, preserves manual fields and offers Other', async () => {
  setup(); await lan(); await userEvent.selectOptions(input('Host interface'), 'enp3s0');
  expect(input('Subnet').value).toBe('192.168.0.0/24'); expect(input('Gateway').value).toBe('192.168.0.1');
  await userEvent.clear(input('Subnet')); await userEvent.type(input('Subnet'), '172.20.0.0/16');
  await userEvent.selectOptions(input('Host interface'), 'eth0'); expect(input('Subnet').value).toBe('172.20.0.0/16');
  await userEvent.selectOptions(input('Host interface'), '__other__'); expect(screen.getByLabelText('Parent interface')).toBeTruthy();
});
it('uses plain parent input for an empty interface list', async () => {
  setup(undefined, []); await userEvent.click(screen.getByRole('radio', { name: 'Macvlan' }));
  expect(await screen.findByLabelText('Parent interface')).toBeTruthy(); expect(screen.queryByLabelText('Host interface')).toBeNull();
});
it('posts exact macvlan body and calls created before close', async () => {
  const { onCreated, onClose } = setup(); await lan(); await userEvent.type(input('Name'), 'lan');
  await userEvent.selectOptions(input('Host interface'), 'enp3s0'); await userEvent.click(screen.getByRole('button', { name: 'Create network' }));
  await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
  expect(fake.calls.find(c => c.method === 'POST')?.body).toEqual({ name: 'lan', driver: 'macvlan', subnet: '192.168.0.0/24', gateway: '192.168.0.1', parent: 'enp3s0' });
  expect(onCreated).toHaveBeenCalledWith({ name: 'lan' }); expect(onCreated.mock.invocationCallOrder[0]).toBeLessThan(onClose.mock.invocationCallOrder[0]);
});
it('omits parent and mode when switched to bridge', async () => {
  setup(); await lan(); await userEvent.click(screen.getByRole('radio', { name: 'IPvlan' })); await userEvent.selectOptions(input('IPvlan mode'), 'l3');
  await userEvent.click(screen.getByRole('radio', { name: 'Bridge' })); await userEvent.type(input('Name'), 'lan'); await userEvent.click(screen.getByRole('button', { name: 'Create network' }));
  expect(fake.calls.find(c => c.method === 'POST')?.body).toEqual({ name: 'lan', driver: 'bridge', subnet: '10.0.0.0/24', gateway: '10.0.0.1' });
});
it('maps known field errors inline', async () => {
  setup({ status: 400, body: { error: 'invalid', fields: [{ field: 'subnet', message: 'Bad subnet' }, { field: 'parent', message: 'Bad parent' }] } });
  await lan(); await userEvent.type(input('Name'), 'lan'); await userEvent.click(screen.getByRole('button', { name: 'Create network' }));
  expect(await screen.findByText('Bad subnet')).toBeTruthy(); expect(input('Subnet').getAttribute('aria-invalid')).toBe('true'); expect(input('Host interface').getAttribute('aria-invalid')).toBe('true'); expect(screen.queryByRole('alert')).toBeNull();
});
it.each([409, 503, 400])('shows non-field errors in banner (%s)', async status => {
  setup({ status, body: { error: 'Cannot create', fields: status === 400 ? [{ field: 'unknown', message: 'Unknown' }] : [] } });
  await userEvent.type(input('Name'), 'lan'); await userEvent.click(screen.getByRole('button', { name: 'Create network' })); expect((await screen.findByRole('alert')).textContent).toContain('Cannot create');
});
it('Esc closes without posting', async () => { const { onClose } = setup(); await userEvent.keyboard('{Escape}'); expect(onClose).toHaveBeenCalledOnce(); expect(fake.calls.some(c => c.method === 'POST')).toBe(false); });
it('is busy and prevents duplicate submits', async () => {
  setup(); fake.restore(); let resolve!: (v: { body: unknown }) => void;
  fake = installFakeFetch({ 'GET /api/host/interfaces': { body: [] }, 'POST /api/networks': () => new Promise(r => { resolve = r; }) });
  await userEvent.type(input('Name'), 'lan'); const button = screen.getByRole('button', { name: 'Create network' }); await userEvent.click(button); await userEvent.click(button);
  expect(button.getAttribute('aria-busy')).toBe('true'); expect(fake.calls.filter(c => c.method === 'POST')).toHaveLength(1);
  resolve({ body: { name: 'lan' } }); await waitFor(() => expect(button.getAttribute('aria-busy')).toBeNull());
});
it('falls back on interface fetch failure and resets on each opening', async () => {
  fake = installFakeFetch({ 'GET /api/host/interfaces': { status: 503, body: { error: 'No interfaces' } } });
  const onCreated = vi.fn(), onClose = vi.fn();
  const view = render(<CreateNetworkDialog open onCreated={onCreated} onClose={onClose} />);
  const reopen = async () => {
    view.rerender(<CreateNetworkDialog open={false} onCreated={onCreated} onClose={onClose} />);
    view.rerender(<CreateNetworkDialog open onCreated={onCreated} onClose={onClose} />);
  };
  await userEvent.type(input('Name'), 'old'); await userEvent.click(screen.getByRole('radio', { name: 'Macvlan' }));
  expect(await screen.findByLabelText('Parent interface')).toBeTruthy(); expect(screen.queryByLabelText('Host interface')).toBeNull();
  await reopen();
  expect(input('Name').value).toBe(''); expect((screen.getByRole('radio', { name: 'Bridge' }) as HTMLInputElement).checked).toBe(true);
  expect(fake.calls.filter(c => c.path === '/api/host/interfaces')).toHaveLength(2);
});
it('preserves a manually edited gateway when selecting another interface', async () => {
  setup(); await lan(); await userEvent.clear(input('Gateway')); await userEvent.type(input('Gateway'), '10.0.0.254');
  await userEvent.selectOptions(input('Host interface'), 'enp3s0'); expect(input('Gateway').value).toBe('10.0.0.254');
});
it('sends ipvlan mode and explicit advanced options', async () => {
  setup(); await lan(); await userEvent.click(screen.getByRole('radio', { name: 'IPvlan' }));
  await userEvent.selectOptions(input('IPvlan mode'), 'l3'); await userEvent.type(input('Name'), 'lan');
  await userEvent.type(input('IP range (optional)'), '10.0.0.128/25');
  await userEvent.click(screen.getByText('Advanced')); await userEvent.click(screen.getByLabelText('Internal'));
  await userEvent.click(screen.getByRole('button', { name: 'Create network' }));
  expect(fake.calls.find(c => c.method === 'POST')?.body).toEqual({ name: 'lan', driver: 'ipvlan', subnet: '10.0.0.0/24', gateway: '10.0.0.1', parent: 'eth0', ipvlanMode: 'l3', ipRange: '10.0.0.128/25', internal: true });
});
it.each([201, 400])('ignores canceled request completion and preserves a newer busy submission (%s)', async status => {
  let finishOld!: (response: { status: number; body: unknown }) => void;
  let finishNew!: (response: { body: unknown }) => void;
  let posts = 0;
  fake = installFakeFetch({
    'GET /api/host/interfaces': { body: [] },
    'POST /api/networks': () => ++posts === 1 ? new Promise(resolve => { finishOld = resolve; }) : new Promise(resolve => { finishNew = resolve; }),
  });
  const onCreated = vi.fn(), onClose = vi.fn();
  const view = render(<CreateNetworkDialog open onCreated={onCreated} onClose={onClose} />);
  const reopen = async () => {
    view.rerender(<CreateNetworkDialog open={false} onCreated={onCreated} onClose={onClose} />);
    view.rerender(<CreateNetworkDialog open onCreated={onCreated} onClose={onClose} />);
  };
  await userEvent.type(input('Name'), 'old'); await userEvent.click(screen.getByRole('button', { name: 'Create network' }));
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  await reopen();
  await userEvent.type(input('Name'), 'new'); const button = screen.getByRole('button', { name: 'Create network' });
  await userEvent.click(button);
  finishOld({ status, body: status === 201 ? { name: 'old' } : { error: 'Old error', fields: [{ field: 'name', message: 'Old name error' }] } });
  await userEvent.keyboard('{Tab}');
  expect(onCreated).not.toHaveBeenCalled(); expect(onClose).toHaveBeenCalledTimes(1);
  expect(screen.queryByText('Old name error')).toBeNull(); expect(screen.queryByRole('alert')).toBeNull();
  expect(input('Name').value).toBe('new'); expect(button.getAttribute('aria-busy')).toBe('true');
  await userEvent.click(button); expect(posts).toBe(2);
  finishNew({ body: { name: 'new' } });
  await waitFor(() => expect(onCreated).toHaveBeenCalledWith({ name: 'new' }));
  expect(onClose).toHaveBeenCalledTimes(2);
});
