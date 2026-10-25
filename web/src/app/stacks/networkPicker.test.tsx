import { useState } from 'react';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import type { Network } from '$lib/api/types';
import { NetworkPicker, type NetworkPickerProps } from './NetworkPicker';

let fake: FakeFetch;
afterEach(() => {
  cleanup();
  fake?.restore();
});

const renderPicker = (props: NetworkPickerProps) => render(<NetworkPicker {...props} />);

const networkDefaults = { fullId: '', scope: 'local', gateways: [], ipRange: '', parent: '', ipvlanMode: '', internal: false, attachable: false, ipv6: false, created: '', builtin: false, project: '', stack: '', containers: [] };
const nets: Network[] = [
  { ...networkDefaults, id: 'frontend-id', name: 'frontend', driver: 'bridge', subnets: ['172.20.0.0/16'] },
  { ...networkDefaults, id: 'backend-id', name: 'backend', driver: 'bridge', subnets: [] },
];

describe('NetworkPicker', () => {
  it('offers the API networks with subnet hints and loads them once', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onChange = vi.fn();
    renderPicker({ value: [{ name: 'frontend', ipv4Address: '' }], onChange });
    const select = (await screen.findByRole('combobox', { name: /network/i })) as HTMLSelectElement;
    await waitFor(() => expect(screen.getByRole('option', { name: /backend/ })).toBeTruthy());
    expect(screen.getByRole('option', { name: /frontend.*172\.20\.0\.0\/16/ })).toBeTruthy();
    expect(select.value).toBe('frontend');
    expect(fake.calls.filter((c) => c.path === '/api/networks')).toHaveLength(1);
  });

  it('static IP input appears only once a network is chosen and uses the first subnet as placeholder', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onChange = vi.fn();
    renderPicker({ value: [{ name: '', ipv4Address: '' }], onChange });
    await screen.findByRole('option', { name: /backend/ });
    expect(screen.queryByLabelText(/static ipv4/i)).toBeNull();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: /network/i }), 'frontend');
    expect(onChange).toHaveBeenCalledWith([{ name: 'frontend', ipv4Address: '' }]);
  });

  it('shows the IP input with the subnet placeholder for a chosen network and reports typing', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onChange = vi.fn();
    renderPicker({ value: [{ name: 'frontend', ipv4Address: '' }], onChange });
    const ip = (await screen.findByLabelText(/static ipv4/i)) as HTMLInputElement;
    await waitFor(() => expect(ip.placeholder).toBe('e.g. in 172.20.0.0/16'));
    await userEvent.type(ip, '1');
    expect(onChange).toHaveBeenLastCalledWith([{ name: 'frontend', ipv4Address: '1' }]);
  });

  it('add and remove rows', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onChange = vi.fn();
    renderPicker({ value: [{ name: 'frontend', ipv4Address: '' }], onChange });
    await screen.findByRole('option', { name: /backend/ });
    await userEvent.click(screen.getByRole('button', { name: /add network/i }));
    expect(onChange).toHaveBeenLastCalledWith([
      { name: 'frontend', ipv4Address: '' },
      { name: '', ipv4Address: '' },
    ]);
    await userEvent.click(screen.getByRole('button', { name: 'Remove item 1' }));
    expect(onChange).toHaveBeenLastCalledWith([]);
  });

  it('renders per-row errors', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    renderPicker({ value: [{ name: 'frontend', ipv4Address: 'bad' }], onChange: vi.fn(), errors: { 0: 'not an IPv4 address' } });
    expect(await screen.findByText('not an IPv4 address')).toBeTruthy();
  });

  it('degrades to a free-text name with a note when the engine is down', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { status: 503, body: { error: 'docker unavailable' } } });
    const onChange = vi.fn();
    renderPicker({ value: [{ name: 'frontend', ipv4Address: '' }], onChange });
    expect(await screen.findByText(/couldn.t list networks/i)).toBeTruthy();
    expect(screen.queryByRole('combobox', { name: /network/i })).toBeNull();
    expect(screen.queryByRole('option', { name: 'Create network…' })).toBeNull();
    const text = screen.getByRole('textbox', { name: /network/i }) as HTMLInputElement;
    expect(text.value).toBe('frontend');
    await userEvent.type(text, 'x');
    expect(onChange).toHaveBeenLastCalledWith([{ name: 'frontendx', ipv4Address: '' }]);
  });

  it('read-only disables controls and hides add/remove', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    renderPicker({ value: [{ name: 'frontend', ipv4Address: '172.20.0.5' }], onChange: vi.fn(), readonly: true });
    const select = (await screen.findByRole('combobox', { name: /network/i })) as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect((screen.getByLabelText(/static ipv4/i) as HTMLInputElement).readOnly).toBe(true);
    expect(screen.queryByRole('button', { name: /add network/i })).toBeNull();
  });

  it('clearing the network name also clears the static IP', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    const onChange = vi.fn();
    renderPicker({ value: [{ name: 'frontend', ipv4Address: '172.20.0.5' }], onChange });
    await screen.findByRole('option', { name: /backend/ });
    await userEvent.selectOptions(screen.getByRole('combobox', { name: /network/i }), '');
    expect(onChange).toHaveBeenLastCalledWith([{ name: '', ipv4Address: '' }]);
  });

  it('uses stable ids from idPrefix and flags the control a row error belongs to', async () => {
    fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
    renderPicker({
      value: [{ name: 'frontend', ipv4Address: 'bad' }, { name: '', ipv4Address: '' }],
      onChange: vi.fn(),
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

it('offers Create network last and restores previous selection on cancel', async () => {
  fake = installFakeFetch({ 'GET /api/networks': { body: nets }, 'GET /api/host/interfaces': { body: [] } });
  const onChange = vi.fn(); renderPicker({ value: [{ name: 'frontend', ipv4Address: '' }], onChange });
  await screen.findByRole('option', { name: /backend/ });
  const select = screen.getByRole('combobox', { name: 'Network' }) as HTMLSelectElement;
  expect(select.options[select.options.length - 1].text).toBe('Create network…');
  await userEvent.selectOptions(select, '__create__'); expect(await screen.findByRole('dialog')).toBeTruthy(); expect(onChange).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' })); expect(select.value).toBe('frontend'); expect(onChange).not.toHaveBeenCalled();
});
it('selects the created network after delayed reload without restoring the old selection', async () => {
  let count = 0, finish!: (value: { body: Network[] }) => void;
  const created = { ...nets[0], name: 'lan', id: 'lan-id' };
  fake = installFakeFetch({
    'GET /api/networks': () => ++count === 1 ? { body: nets } : new Promise(resolve => { finish = resolve; }),
    'GET /api/host/interfaces': { body: [] }, 'POST /api/networks': { status: 201, body: created },
  });
  const onChange = vi.fn(); renderPicker({ value: [{ name: 'frontend', ipv4Address: '' }], onChange });
  await screen.findByRole('option', { name: /backend/ }); const select = screen.getByRole('combobox', { name: 'Network' }) as HTMLSelectElement;
  await userEvent.selectOptions(select, '__create__'); await userEvent.type(screen.getByLabelText('Name'), 'lan');
  await userEvent.click(screen.getByRole('button', { name: 'Create network' })); await waitFor(() => expect(count).toBe(2));
  expect(screen.queryByRole('dialog')).toBeNull(); expect(select.value).not.toBe('frontend');
  finish({ body: [...nets, created] }); await waitFor(() => expect(onChange).toHaveBeenCalledWith([{ name: 'lan', ipv4Address: '' }]));
  expect(await screen.findByRole('option', { name: /lan/ })).toBeTruthy(); expect(count).toBe(2);
});
it('disables creation when readonly', async () => {
  fake = installFakeFetch({ 'GET /api/networks': { body: nets } });
  renderPicker({ value: [{ name: 'frontend', ipv4Address: '' }], onChange: vi.fn(), readonly: true });
  expect((await screen.findByRole('option', { name: 'Create network…' }) as HTMLOptionElement).disabled).toBe(true);
});
it.each([false, true])('ignores a delayed canceled POST without changing rows or closing a newer dialog (reopen: %s)', async reopen => {
  let finish!: (response: { status: number; body: Network }) => void;
  fake = installFakeFetch({
    'GET /api/networks': { body: nets }, 'GET /api/host/interfaces': { body: [] },
    'POST /api/networks': () => new Promise(resolve => { finish = resolve; }),
  });
  const onChange = vi.fn();
  renderPicker({ value: [{ name: 'frontend', ipv4Address: '' }, { name: 'backend', ipv4Address: '' }], onChange });
  await screen.findAllByRole('option', { name: /backend/ });
  const selects = screen.getAllByRole('combobox', { name: 'Network' }) as HTMLSelectElement[];
  await userEvent.selectOptions(selects[0], '__create__'); await userEvent.type(screen.getByLabelText('Name'), 'old');
  await userEvent.click(screen.getByRole('button', { name: 'Create network' }));
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(selects[0].value).toBe('frontend');
  if (reopen) {
    await userEvent.selectOptions(selects[1], '__create__'); await userEvent.type(screen.getByLabelText('Name'), 'new');
  }
  finish({ status: 201, body: { ...nets[0], name: 'old', id: 'old' } });
  await userEvent.keyboard('{Tab}');
  expect(onChange).not.toHaveBeenCalled(); expect(selects[0].value).toBe('frontend');
  expect(fake.calls.filter(c => c.path === '/api/networks' && c.method === 'GET')).toHaveLength(1);
  if (reopen) { expect(screen.getByRole('dialog')).toBeTruthy(); expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('new'); }
  else expect(screen.queryByRole('dialog')).toBeNull();
});

function StatefulPicker({ initial, onValue }: { initial: NetworkPickerProps['value']; onValue: (v: NetworkPickerProps['value']) => void }) {
  const [value, setValue] = useState(initial);
  onValue(value);
  return <NetworkPicker value={value} onChange={setValue} />;
}

describe('NetworkPicker with a stateful parent', () => {
  function setupCreate() {
    let count = 0, finish!: (value: { body: Network[] }) => void;
    const created = { ...nets[0], name: 'lan', id: 'lan-id' };
    fake = installFakeFetch({
      'GET /api/networks': () => (++count === 1 ? { body: nets } : new Promise((resolve) => { finish = resolve; })),
      'GET /api/host/interfaces': { body: [] },
      'POST /api/networks': { status: 201, body: created },
    });
    let latest: NetworkPickerProps['value'] = [];
    render(<StatefulPicker initial={[{ name: 'frontend', ipv4Address: '' }, { name: 'backend', ipv4Address: '' }]} onValue={(v) => (latest = v)} />);
    return { finish: () => finish({ body: [...nets, created] }), value: () => latest, created };
  }
  const selects = () => screen.getAllByRole('combobox', { name: 'Network' }) as HTMLSelectElement[];

  it('returns the select to the real value after cancel, and after create it shows the new network and stays editable', async () => {
    const ctl = setupCreate();
    await screen.findAllByRole('option', { name: /backend/ });
    await userEvent.selectOptions(selects()[0], '__create__');
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(selects()[0].value).toBe('frontend');
    await userEvent.selectOptions(selects()[0], '__create__');
    await userEvent.type(screen.getByLabelText('Name'), 'lan');
    await userEvent.click(screen.getByRole('button', { name: 'Create network' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    ctl.finish();
    await waitFor(() => expect(selects()[0].value).toBe('lan'));
    await userEvent.selectOptions(selects()[0], 'backend');
    expect(selects()[0].value).toBe('backend');
    expect(ctl.value()[0].name).toBe('backend');
  });

  it('does not write the created network into a different row when the row was removed before the reload finished', async () => {
    const ctl = setupCreate();
    await screen.findAllByRole('option', { name: /backend/ });
    await userEvent.selectOptions(selects()[0], '__create__');
    await userEvent.type(screen.getByLabelText('Name'), 'lan');
    await userEvent.click(screen.getByRole('button', { name: 'Create network' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await userEvent.click(screen.getByRole('button', { name: 'Remove item 1' }));
    ctl.finish();
    await screen.findByRole('option', { name: /lan/ });
    expect(ctl.value()).toEqual([{ name: 'backend', ipv4Address: '' }]);
    expect(selects()[0].value).toBe('backend');
  });
});
