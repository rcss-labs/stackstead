import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as session from '../lib/api/session';
import { toasts } from '../lib/stores/toasts';
import { installFakeFetch, type FakeFetch } from '../vitest-setup';
import Settings from './Settings.svelte';

let fake: FakeFetch;
const TOKEN = 'tok_ABCDEF0123456789';

beforeEach(() => {
  vi.spyOn(session, 'logout').mockResolvedValue({ ok: true });
});
afterEach(() => {
  fake?.restore();
  toasts.reset();
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
});

const STATUS = '/api/settings/metrics-token';

describe('Settings metrics token', () => {
  it('not configured: explains /metrics 404 and offers Create only', async () => {
    fake = installFakeFetch({ [`GET ${STATUS}`]: { body: { configured: false } } });
    render(Settings);
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeTruthy();
    expect(screen.getByText(/404/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Rotate token' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Disable' })).toBeNull();
  });

  it('configured: offers Rotate and Disable', async () => {
    fake = installFakeFetch({ [`GET ${STATUS}`]: { body: { configured: true } } });
    render(Settings);
    expect(await screen.findByRole('button', { name: 'Rotate token' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Disable' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Create token' })).toBeNull();
  });

  it('failed status fetch shows an error with Retry, not a guess', async () => {
    let n = 0;
    fake = installFakeFetch({
      [`GET ${STATUS}`]: () => (++n === 1 ? { status: 500, body: { error: 'boom' } } : { body: { configured: false } }),
    });
    const user = userEvent.setup();
    render(Settings);
    expect(await screen.findByText(/boom/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Create token' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeTruthy();
  });

  it('create shows the token once, with a snippet, and hides it for good when saved', async () => {
    fake = installFakeFetch({
      [`GET ${STATUS}`]: { body: { configured: false } },
      [`POST ${STATUS}`]: { status: 200, body: { token: TOKEN } },
    });
    const user = userEvent.setup();
    const { container } = render(Settings);
    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    const input = (await screen.findByLabelText('Metrics token')) as HTMLInputElement;
    expect(input.value).toBe(TOKEN);
    expect(input.readOnly).toBe(true);
    const snippet = screen.getByTestId('scrape-snippet').textContent ?? '';
    expect(snippet).toContain('scrape_configs');
    expect(snippet).toContain(`credentials: ${TOKEN}`);
    expect(snippet).toContain(window.location.host);
    expect(snippet).toContain(`scheme: ${window.location.protocol.slice(0, -1)}`);
    expect(document.activeElement).toBe(input);
    expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'I have saved it' }));
    expect(container.ownerDocument.body.innerHTML).not.toContain(TOKEN);
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Rotate token' }));
    expect(JSON.stringify({ ...localStorage })).not.toContain(TOKEN);
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(TOKEN);
  });

  it('leaving the page clears the token from the DOM', async () => {
    fake = installFakeFetch({
      [`GET ${STATUS}`]: { body: { configured: false } },
      [`POST ${STATUS}`]: { status: 200, body: { token: TOKEN } },
    });
    const user = userEvent.setup();
    const { unmount } = render(Settings);
    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    await screen.findByLabelText('Metrics token');
    unmount();
    expect(document.body.innerHTML).not.toContain(TOKEN);
  });

  it('rotate requires confirmation and warns the old token stops working', async () => {
    fake = installFakeFetch({
      [`GET ${STATUS}`]: { body: { configured: true } },
      [`POST ${STATUS}`]: { status: 200, body: { token: TOKEN } },
    });
    const user = userEvent.setup();
    render(Settings);
    await user.click(await screen.findByRole('button', { name: 'Rotate token' }));
    const dlg = screen.getByRole('alertdialog');
    expect(dlg.textContent).toMatch(/old token stops working immediately/i);
    expect(fake.calls.filter((c) => c.method === 'POST')).toHaveLength(0);
    await user.click(within(dlg).getByRole('button', { name: 'Cancel' }));
    expect(fake.calls.filter((c) => c.method === 'POST')).toHaveLength(0);

    await user.click(screen.getByRole('button', { name: 'Rotate token' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Rotate' }));
    expect(((await screen.findByLabelText('Metrics token')) as HTMLInputElement).value).toBe(TOKEN);
  });

  it('disable requires confirmation then flips to not configured', async () => {
    fake = installFakeFetch({
      [`GET ${STATUS}`]: { body: { configured: true } },
      [`DELETE ${STATUS}`]: { status: 204 },
    });
    const user = userEvent.setup();
    render(Settings);
    await user.click(await screen.findByRole('button', { name: 'Disable' }));
    expect(fake.calls.filter((c) => c.method === 'DELETE')).toHaveLength(0);
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Disable token' }));
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === 'DELETE')).toHaveLength(1);
  });

  it('a failed create shows an error and no token', async () => {
    fake = installFakeFetch({
      [`GET ${STATUS}`]: { body: { configured: false } },
      [`POST ${STATUS}`]: { status: 500, body: { error: 'disk full' } },
    });
    const user = userEvent.setup();
    render(Settings);
    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    expect(await screen.findByText(/disk full/)).toBeTruthy();
    expect(screen.queryByLabelText('Metrics token')).toBeNull();
  });
});

describe('Settings status and account', () => {
  it('renders status values from the live store and refreshes on demand', async () => {
    fake = installFakeFetch({ [`GET ${STATUS}`]: { body: { configured: false } } });
    const liveMod = await import('../lib/stores/live');
    const fetchStatus = vi.fn().mockResolvedValue({ docker: 'ok', compose: 'v2.29.1', events: 'connected', metricsDegraded: false });
    const open = vi.fn((o: { onStatus: (s: string) => void }) => {
      o.onStatus('closed');
      return { close() {}, status: 'closed', lastEventId: '' };
    });
    liveMod.startLive({ fetchStatus, open: open as never });
    try {
      const user = userEvent.setup();
      render(Settings);
      await user.click(await screen.findByRole('button', { name: 'Refresh' }));
      await waitFor(() => expect(screen.getByTestId('status-compose').textContent).toContain('v2.29.1'));
      expect(fetchStatus).toHaveBeenCalled();
      expect(screen.getByTestId('status-docker').textContent).toMatch(/ok/i);
      expect(screen.getByTestId('status-metrics')).toBeTruthy();
    } finally {
      liveMod.stopLive();
    }
  });

  it('logout calls the session action and warns when it may not have completed', async () => {
    fake = installFakeFetch({ [`GET ${STATUS}`]: { body: { configured: false } } });
    vi.spyOn(session, 'logout').mockResolvedValue({ ok: false });
    const user = userEvent.setup();
    render(Settings);
    await user.click(await screen.findByRole('button', { name: 'Log out' }));
    expect(session.logout).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(get(toasts).some((t) => /logout may not have completed/i.test(t.message))).toBe(true));
  });
});
