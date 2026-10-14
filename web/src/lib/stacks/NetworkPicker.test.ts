import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import NetworkPicker from './NetworkPicker.svelte';

let fake: FakeFetch;
afterEach(() => fake?.restore());

const nets = [
  { name: 'frontend', driver: 'bridge', subnets: ['172.20.0.0/16'] },
  { name: 'backend', driver: 'bridge', subnets: [] },
];

describe('NetworkPicker', () => {
  it('offers the API networks with subnet hints and loads them once', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onchange = vi.fn();
    render(NetworkPicker, { value: [{ name: 'frontend', ipv4Address: '' }], onchange });
    const select = (await screen.findByRole('combobox', { name: /network/i })) as HTMLSelectElement;
    await waitFor(() => expect(screen.getByRole('option', { name: /backend/ })).toBeTruthy());
    expect(screen.getByRole('option', { name: /frontend.*172\.20\.0\.0\/16/ })).toBeTruthy();
    expect(select.value).toBe('frontend');
    expect(fake.calls.filter((c) => c.path === '/api/networks')).toHaveLength(1);
  });

  it('static IP input appears only once a network is chosen and uses the first subnet as placeholder', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onchange = vi.fn();
    render(NetworkPicker, { value: [{ name: '', ipv4Address: '' }], onchange });
    await screen.findByRole('option', { name: /backend/ });
    expect(screen.queryByLabelText(/static ipv4/i)).toBeNull();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: /network/i }), 'frontend');
    expect(onchange).toHaveBeenCalledWith([{ name: 'frontend', ipv4Address: '' }]);
  });

  it('shows the IP input with the subnet placeholder for a chosen network and reports typing', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onchange = vi.fn();
    render(NetworkPicker, { value: [{ name: 'frontend', ipv4Address: '' }], onchange });
    const ip = (await screen.findByLabelText(/static ipv4/i)) as HTMLInputElement;
    await waitFor(() => expect(ip.placeholder).toBe('e.g. in 172.20.0.0/16'));
    await userEvent.type(ip, '1');
    expect(onchange).toHaveBeenLastCalledWith([{ name: 'frontend', ipv4Address: '1' }]);
  });

  it('add and remove rows', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onchange = vi.fn();
    render(NetworkPicker, { value: [{ name: 'frontend', ipv4Address: '' }], onchange });
    await screen.findByRole('option', { name: /backend/ });
    await userEvent.click(screen.getByRole('button', { name: /add network/i }));
    expect(onchange).toHaveBeenLastCalledWith([
      { name: 'frontend', ipv4Address: '' },
      { name: '', ipv4Address: '' },
    ]);
    await userEvent.click(screen.getByRole('button', { name: 'Remove item 1' }));
    expect(onchange).toHaveBeenLastCalledWith([]);
  });

  it('renders per-row errors', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    render(NetworkPicker, { value: [{ name: 'frontend', ipv4Address: 'bad' }], onchange: vi.fn(), errors: { 0: 'not an IPv4 address' } });
    expect(await screen.findByText('not an IPv4 address')).toBeTruthy();
  });

  it('degrades to a free-text name with a note when the engine is down', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { status: 503, body: { error: 'docker unavailable' } } });
    const onchange = vi.fn();
    render(NetworkPicker, { value: [{ name: 'frontend', ipv4Address: '' }], onchange });
    expect(await screen.findByText(/couldn.t list networks/i)).toBeTruthy();
    expect(screen.queryByRole('combobox', { name: /network/i })).toBeNull();
    const text = screen.getByRole('textbox', { name: /network/i }) as HTMLInputElement;
    expect(text.value).toBe('frontend');
    await userEvent.type(text, 'x');
    expect(onchange).toHaveBeenLastCalledWith([{ name: 'frontendx', ipv4Address: '' }]);
  });

  it('read-only disables controls and hides add/remove', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    render(NetworkPicker, { value: [{ name: 'frontend', ipv4Address: '172.20.0.5' }], onchange: vi.fn(), readonly: true });
    const select = (await screen.findByRole('combobox', { name: /network/i })) as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect((screen.getByLabelText(/static ipv4/i) as HTMLInputElement).readOnly).toBe(true);
    expect(screen.queryByRole('button', { name: /add network/i })).toBeNull();
  });

  it('clearing the network name also clears the static IP', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onchange = vi.fn();
    render(NetworkPicker, { value: [{ name: 'frontend', ipv4Address: '172.20.0.5' }], onchange });
    await screen.findByRole('option', { name: /backend/ });
    await userEvent.selectOptions(screen.getByRole('combobox', { name: /network/i }), '');
    expect(onchange).toHaveBeenLastCalledWith([{ name: '', ipv4Address: '' }]);
  });

  it('uses stable ids from idPrefix and flags the control a row error belongs to', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    render(NetworkPicker, {
      value: [{ name: 'frontend', ipv4Address: 'bad' }, { name: '', ipv4Address: '' }],
      onchange: vi.fn(),
      idPrefix: 'svc-networks',
      errors: { 0: 'not an IPv4 address', 1: 'network name is required' },
    });
    await screen.findAllByRole('option', { name: /backend/ });
    const ip = document.getElementById('svc-networks-0-ip')!;
    expect(ip.getAttribute('aria-invalid')).toBe('true');
    expect(document.getElementById('svc-networks-0-name')!.getAttribute('aria-invalid')).toBeNull();
    expect(document.getElementById('svc-networks-1-name')!.getAttribute('aria-invalid')).toBe('true');
  });
});
