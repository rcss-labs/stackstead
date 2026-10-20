import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { api } from '$lib/api/client';
import { startPeers } from '$lib/servers/peers';
import type { PeerView, ServerSelf } from '$lib/api/types';
import { installFakeFetch, type FakeFetch, type Routes } from '../../vitest-setup';
import { ConnectedServersCard } from './ConnectedServersCard';

let fake: FakeFetch | undefined;
let stopPeers: (() => void) | undefined;
afterEach(() => {
  stopPeers?.();
  stopPeers = undefined;
  fake?.restore();
});

const SELF: ServerSelf = {
  instanceId: 'inst-1234',
  name: 'Mini-01',
  slug: 'mini-01',
  advertisedUrl: '',
  suggestedUrl: 'https://mini-01.lan:8443',
  version: '0.4.2',
};
const PEER: PeerView = {
  id: 'p1',
  name: 'Mini-02',
  slug: 'mini-02',
  url: 'https://mini-02.lan:8443',
  version: '0.4.1',
  status: 'reachable',
  stackCount: 3,
  lastSeenAt: '2026-10-20T10:00:00Z',
  lastError: '',
  compatible: true,
  plaintext: false,
};
const PROBE = { peering: 1, fingerprintSha256: 'AA:BB:CC', systemTrusted: false, plaintextAllowed: false };

async function mount(routes: Routes, peers: PeerView[] = []) {
  fake = installFakeFetch({
    'GET /api/peers/self': { body: SELF },
    'GET /api/peers': () => ({ body: peers }),
    ...routes,
  });
  stopPeers = startPeers({ list: () => api.peers() });
  const view = render(<ConnectedServersCard />);
  await screen.findByDisplayValue('Mini-01');
  return view;
}

async function probeAddress(user: ReturnType<typeof userEvent.setup>, token = 'join-token') {
  await user.type(screen.getByLabelText('Server address'), 'https://mini-02.lan:8443');
  await user.type(screen.getByLabelText('Connection token'), token);
  await user.click(screen.getByRole('button', { name: 'Check address' }));
}

describe('ConnectedServersCard', () => {
  it('creates a token and shows it once', async () => {
    const user = userEvent.setup();
    const routes: Routes = {
      'POST /api/peers/tokens': { body: { token: 'cs_SECRET_TOKEN', expiresAt: '2026-10-21T10:00:00Z' } },
    };
    const view = await mount(routes);
    await user.click(screen.getByRole('button', { name: 'Create connection token' }));
    expect(await screen.findByDisplayValue('cs_SECRET_TOKEN')).toBeTruthy();
    expect(screen.getByText(/Expires at/)).toBeTruthy();

    view.unmount();
    fake!.restore();
    fake = installFakeFetch({ 'GET /api/peers/self': { body: SELF }, 'GET /api/peers': { body: [] } });
    render(<ConnectedServersCard />);
    await screen.findByDisplayValue('Mini-01');
    expect(screen.queryByDisplayValue('cs_SECRET_TOKEN')).toBeNull();
    expect(fake.calls.filter((c) => c.method !== 'GET')).toEqual([]);
  });

  it('probe shows the fingerprint and pins it on add', async () => {
    const user = userEvent.setup();
    await mount({
      'POST /api/peers/probe': { body: PROBE },
      'POST /api/peers': { body: PEER },
    });
    await probeAddress(user);
    expect(await screen.findByText('AA:BB:CC')).toBeTruthy();
    expect(screen.getByText('mini-02.lan will reach this server at https://mini-01.lan:8443')).toBeTruthy();
    await user.click(screen.getByLabelText('Trust this certificate'));
    await user.click(screen.getByRole('button', { name: 'Add server' }));
    await waitFor(() => expect(fake!.calls.some((c) => c.method === 'POST' && c.path === '/api/peers')).toBe(true));
    const add = fake!.calls.find((c) => c.method === 'POST' && c.path === '/api/peers')!;
    expect(add.body).toMatchObject({
      url: 'https://mini-02.lan:8443',
      token: 'join-token',
      trust: { pinSha256: 'AA:BB:CC', plaintext: false },
      advertisedUrl: 'https://mini-01.lan:8443',
    });
  });

  it('plaintext checkbox appears only when allowed and reaches both trusts', async () => {
    const user = userEvent.setup();
    await mount({ 'POST /api/peers/probe': { body: PROBE }, 'POST /api/peers': { body: PEER } });
    await probeAddress(user);
    await screen.findByText('AA:BB:CC');
    expect(screen.queryByLabelText('I accept sending a root-equivalent credential unencrypted')).toBeNull();
    fake!.restore();

    fake = installFakeFetch({
      'GET /api/peers/self': { body: SELF },
      'GET /api/peers': { body: [] },
      'POST /api/peers/probe': { body: { ...PROBE, fingerprintSha256: '', systemTrusted: false, plaintextAllowed: true } },
      'POST /api/peers': { body: PEER },
    });
    await user.click(screen.getByRole('button', { name: 'Check address' }));
    await user.click(await screen.findByLabelText('I accept sending a root-equivalent credential unencrypted'));
    await user.click(screen.getByRole('button', { name: 'Add server' }));
    await waitFor(() => expect(fake!.calls.some((c) => c.method === 'POST' && c.path === '/api/peers')).toBe(true));
    const add = fake!.calls.find((c) => c.method === 'POST' && c.path === '/api/peers')!;
    expect(add.body).toMatchObject({ trust: { plaintext: true }, selfTrust: { plaintext: true } });
  });

  it('an http address shows no certificate block and needs only the plaintext checkbox', async () => {
    const user = userEvent.setup();
    await mount({
      'POST /api/peers/probe': { body: { peering: 1, fingerprintSha256: '', systemTrusted: false, plaintextAllowed: true } },
      'POST /api/peers': { body: PEER },
    });
    await user.type(screen.getByLabelText('Server address'), 'http://192.168.1.5:8420');
    await user.type(screen.getByLabelText('Connection token'), 'join-token');
    await user.click(screen.getByRole('button', { name: 'Check address' }));
    const plaintext = await screen.findByLabelText('I accept sending a root-equivalent credential unencrypted');
    expect(screen.queryByText(/not signed by a trusted authority/)).toBeNull();
    expect(screen.queryByLabelText('Trust this certificate')).toBeNull();
    const add = screen.getByRole('button', { name: 'Add server' });
    expect(add.hasAttribute('disabled') || add.getAttribute('aria-disabled') === 'true').toBe(true);
    await user.click(plaintext);
    expect(add.hasAttribute('disabled') || add.getAttribute('aria-disabled') === 'true').toBe(false);
    await user.click(add);
    await waitFor(() => expect(fake!.calls.some((c) => c.method === 'POST' && c.path === '/api/peers')).toBe(true));
    expect(fake!.calls.find((c) => c.method === 'POST' && c.path === '/api/peers')!.body).toMatchObject({
      trust: { plaintext: true },
      selfTrust: { plaintext: true },
    });
  });

  it('sends this server\'s CA certificate as selfTrust when given', async () => {
    const user = userEvent.setup();
    await mount({
      'POST /api/peers/probe': { body: { ...PROBE, systemTrusted: true } },
      'POST /api/peers': { body: PEER },
    });
    await probeAddress(user);
    await user.type(await screen.findByLabelText("This server's CA certificate (PEM)"), 'PEM-OF-A');
    await user.click(screen.getByRole('button', { name: 'Add server' }));
    await waitFor(() => expect(fake!.calls.some((c) => c.method === 'POST' && c.path === '/api/peers')).toBe(true));
    expect(fake!.calls.find((c) => c.method === 'POST' && c.path === '/api/peers')!.body).toMatchObject({
      selfTrust: { caPem: 'PEM-OF-A', pinSha256: '', plaintext: false },
    });
  });

  it('add-server error is shown inline on the token field', async () => {
    const user = userEvent.setup();
    await mount({
      'POST /api/peers/probe': { body: { ...PROBE, systemTrusted: true } },
      'POST /api/peers': {
        status: 400,
        body: { error: 'Token rejected', fields: [{ field: 'token', message: 'Token rejected or expired' }] },
      },
    });
    await probeAddress(user);
    await user.click(await screen.findByRole('button', { name: 'Add server' }));
    const error = await screen.findByText('Token rejected or expired');
    expect(screen.getByLabelText('Connection token').getAttribute('aria-describedby')).toContain(error.id);
  });

  it('older server copy is shown', async () => {
    const user = userEvent.setup();
    await mount({
      'POST /api/peers/probe': { body: { ...PROBE, systemTrusted: true } },
      'POST /api/peers': { status: 502, body: { error: 'peer too old', code: 'peer_old' } },
    });
    await probeAddress(user);
    await user.click(await screen.findByRole('button', { name: 'Add server' }));
    expect(await screen.findByText('mini-02.lan runs an older Stackstead without connected servers')).toBeTruthy();
  });

  it('remove asks for confirmation then deletes and refreshes peers', async () => {
    const user = userEvent.setup();
    let list = [PEER];
    await mount(
      {
        'GET /api/peers': () => ({ body: list }),
        'DELETE /api/peers/p1': () => {
          list = [];
          return { status: 204 };
        },
      },
      list,
    );
    await user.click(await screen.findByRole('button', { name: 'Remove Mini-02' }));
    expect(fake!.calls.some((c) => c.method === 'DELETE')).toBe(false);
    const dialog = await screen.findByRole('alertdialog');
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }));
    await waitFor(() => expect(fake!.calls.some((c) => c.method === 'DELETE' && c.path === '/api/peers/p1')).toBe(true));
    await waitFor(() => expect(screen.queryByText('Mini-02')).toBeNull());
  });

  it('a removed peer offers Dismiss', async () => {
    const user = userEvent.setup();
    await mount({ 'DELETE /api/peers/p1': { status: 204 } }, [{ ...PEER, status: 'removed' }]);
    expect(await screen.findByText('Mini-02 removed this connection')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Dismiss' }));
    await waitFor(() => expect(fake!.calls.some((c) => c.method === 'DELETE' && c.path === '/api/peers/p1')).toBe(true));
  });

  it('version mismatch shows a warning on the row', async () => {
    const lastError = 'Mini-02 runs v0.3, this server v0.4. Update to match';
    await mount({}, [{ ...PEER, version: '0.3.9', compatible: false, lastError }]);
    expect(await screen.findByText(lastError)).toBeTruthy();
  });

  it('shows the backend wording once for an incompatible peer and nothing for a compatible one', async () => {
    const lastError = 'Alpha runs v0.4, this server v0.4. Update to match';
    await mount({}, [
      { ...PEER, id: 'a', name: 'Alpha', version: '0.4.1', compatible: false, lastError },
      { ...PEER, id: 'c', name: 'Gamma', version: '0.3.0', compatible: true },
    ]);
    expect(await screen.findAllByText(lastError)).toHaveLength(1);
    expect(screen.queryByText(/Gamma runs/)).toBeNull();
  });

  it('shows errors for hidden fields in the general banner', async () => {
    const user = userEvent.setup();
    await mount({
      'POST /api/peers/probe': { body: { ...PROBE, systemTrusted: true } },
      'POST /api/peers': {
        status: 400,
        body: { error: 'bad', fields: [{ field: 'trust.pinSha256', message: 'Pin mismatch' }, { field: 'token', message: 'Bad token' }] },
      },
    });
    await probeAddress(user);
    await user.click(await screen.findByRole('button', { name: 'Add server' }));
    expect(await screen.findByText('Bad token')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('Pin mismatch');
  });

  it('blocks Add when no advertised address is known', async () => {
    const user = userEvent.setup();
    fake = installFakeFetch({
      'GET /api/peers/self': { body: { ...SELF, suggestedUrl: '' } },
      'GET /api/peers': { body: [] },
      'POST /api/peers/probe': { body: { ...PROBE, systemTrusted: true } },
    });
    stopPeers = startPeers({ list: () => api.peers() });
    render(<ConnectedServersCard />);
    await screen.findByDisplayValue('Mini-01');
    await probeAddress(user);
    const add = await screen.findByRole('button', { name: 'Add server' });
    await user.click(add);
    expect(fake.calls.some((c) => c.method === 'POST' && c.path === '/api/peers')).toBe(false);
    expect(screen.getByText('Set this server\'s advertised address under "This server" first.')).toBeTruthy();
  });

  it('renaming this server sends PUT /api/peers/self', async () => {
    const user = userEvent.setup();
    await mount({ 'PUT /api/peers/self': { body: { ...SELF, name: 'Mini-One' } } });
    const name = screen.getByLabelText('Server name');
    await user.clear(name);
    await user.type(name, 'Mini-One');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(fake!.calls.some((c) => c.method === 'PUT' && c.path === '/api/peers/self')).toBe(true));
    expect(fake!.calls.find((c) => c.method === 'PUT')!.body).toEqual({ name: 'Mini-One', advertisedUrl: '' });
  });
});
