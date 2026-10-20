import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as session from '$lib/api/session';
import { get } from '$lib/store';
import { initAppearance, setDensity, setTheme } from '$lib/stores/appearance';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type Routes } from '../../vitest-setup';
import { mountAt } from '../containers/containersTestKit';
import { findRoute } from '../routes';
import { SettingsScreen } from './SettingsScreen';

let fake: FakeFetch;
const TOKEN = 'tok_ABCDEF0123456789';
const STATUS = '/api/settings/metrics-token';

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

// The update card loads on every Settings render; give it a quiet default answer.
const UPDATE_STATUS = {
  current: '0.2.0',
  installer: 'script',
  command: '/usr/local/bin/stackstead upgrade',
  state: 'up-to-date',
  latest: '0.2.0',
  notesURL: '',
  checkedAt: '2026-10-18T09:00:00Z',
  error: '',
};
const installSettingsFetch = (routes: Routes) =>
  installFakeFetch({ 'GET /api/settings/update': { body: UPDATE_STATUS }, ...routes });
const tokenStatus = (configured: boolean) => ({ [`GET ${STATUS}`]: { body: { configured } } });
const mountSettings = () => mountAt('/settings');

describe('Settings update card', () => {
  it('renders the update card under System status', async () => {
    fake = installSettingsFetch(tokenStatus(false));
    mountSettings();
    expect(await screen.findByRole('region', { name: 'Software update' })).toBeTruthy();
    expect(await screen.findByText('up to date')).toBeTruthy();
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent);
    expect(headings.indexOf('Software update')).toBe(headings.indexOf('System status') + 1);
  });
});

describe('Settings metrics token', () => {
  it('not configured: explains /metrics 404 and offers Create only', async () => {
    fake = installSettingsFetch(tokenStatus(false));
    mountSettings();
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeTruthy();
    expect(screen.getByText(/404/)).toBeTruthy();
    expect(screen.getByText(/scrapes/).textContent).toContain('Prometheus scrapes /metrics with this bearer token.');
    expect(screen.queryByRole('button', { name: 'Rotate token' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Disable' })).toBeNull();
  });

  it('configured: offers Rotate and Disable', async () => {
    fake = installSettingsFetch(tokenStatus(true));
    mountSettings();
    expect(await screen.findByRole('button', { name: 'Rotate token' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Disable' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Create token' })).toBeNull();
  });

  it('failed status fetch shows an error with Retry, not a guess', async () => {
    let n = 0;
    fake = installSettingsFetch({
      [`GET ${STATUS}`]: () => (++n === 1 ? { status: 500, body: { error: 'boom' } } : { body: { configured: false } }),
    });
    const user = userEvent.setup();
    mountSettings();
    expect(await screen.findByText(/boom/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Create token' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeTruthy();
  });

  it('create shows the token once, with a snippet, and hides it for good when saved', async () => {
    fake = installSettingsFetch({
      ...tokenStatus(false),
      [`POST ${STATUS}`]: { status: 200, body: { token: TOKEN } },
    });
    const user = userEvent.setup();
    mountSettings();
    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    const input = (await screen.findByLabelText('Metrics token')) as HTMLInputElement;
    expect(input.value).toBe(TOKEN);
    expect(input.readOnly).toBe(true);
    // Property only: the secret is never serialised into a value attribute.
    expect(document.body.querySelector(`[value*="${TOKEN}"]`)).toBeNull();
    const snippet = screen.getByTestId('scrape-snippet').textContent ?? '';
    expect(snippet).toContain('scrape_configs');
    expect(snippet).toContain(`credentials: ${TOKEN}`);
    expect(snippet).toContain(window.location.host);
    expect(snippet).toContain(`scheme: ${window.location.protocol.slice(0, -1)}`);
    expect(document.activeElement).toBe(input);
    expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'I have saved it' }));
    expect(document.body.innerHTML).not.toContain(TOKEN);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Rotate token' })));
    expect(JSON.stringify({ ...localStorage })).not.toContain(TOKEN);
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(TOKEN);
  });

  it('leaving the page clears the token from the DOM', async () => {
    fake = installSettingsFetch({
      ...tokenStatus(false),
      [`POST ${STATUS}`]: { status: 200, body: { token: TOKEN } },
    });
    const user = userEvent.setup();
    const { unmount } = mountSettings();
    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    await screen.findByLabelText('Metrics token');
    unmount();
    expect(document.body.innerHTML).not.toContain(TOKEN);
  });

  it('rotate requires confirmation and warns the old token stops working', async () => {
    fake = installSettingsFetch({
      ...tokenStatus(true),
      [`POST ${STATUS}`]: { status: 200, body: { token: TOKEN } },
    });
    const user = userEvent.setup();
    mountSettings();
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
    fake = installSettingsFetch({
      ...tokenStatus(true),
      [`DELETE ${STATUS}`]: { status: 204 },
    });
    const user = userEvent.setup();
    mountSettings();
    await user.click(await screen.findByRole('button', { name: 'Disable' }));
    expect(fake.calls.filter((c) => c.method === 'DELETE')).toHaveLength(0);
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Disable token' }));
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === 'DELETE')).toHaveLength(1);
  });

  it('a failed create shows an error and no token', async () => {
    fake = installSettingsFetch({
      ...tokenStatus(false),
      [`POST ${STATUS}`]: { status: 500, body: { error: 'disk full' } },
    });
    const user = userEvent.setup();
    mountSettings();
    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    expect(await screen.findByText(/disk full/)).toBeTruthy();
    expect(screen.queryByLabelText('Metrics token')).toBeNull();
  });

  it('a double click on Create token sends one request', async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    fake = installSettingsFetch({
      ...tokenStatus(false),
      [`POST ${STATUS}`]: async () => {
        await gate;
        return { status: 200, body: { token: TOKEN } };
      },
    });
    mountSettings();
    const create = await screen.findByRole('button', { name: 'Create token' });
    // Both clicks land before React re-renders the loading state, so only a ref guard can stop the second.
    act(() => {
      fireEvent.click(create);
      fireEvent.click(create);
    });
    release();
    expect(await screen.findByLabelText('Metrics token')).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === 'POST')).toHaveLength(1);
  });

  it('rotate and disable confirmations also send once', async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    fake = installSettingsFetch({
      ...tokenStatus(true),
      [`DELETE ${STATUS}`]: async () => {
        await gate;
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    mountSettings();
    await user.click(await screen.findByRole('button', { name: 'Disable' }));
    const confirm = within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Disable token' });
    act(() => {
      fireEvent.click(confirm);
      fireEvent.click(confirm);
    });
    release();
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === 'DELETE')).toHaveLength(1);
  });

  it('loads the token status and update status once under StrictMode', async () => {
    fake = installSettingsFetch(tokenStatus(false));
    render(
      <StrictMode>
        <SettingsScreen />
      </StrictMode>,
    );
    await screen.findByRole('button', { name: 'Create token' });
    await screen.findByText('up to date');
    expect(fake.calls.filter((c) => c.path === STATUS)).toHaveLength(1);
    expect(fake.calls.filter((c) => c.path === '/api/settings/update')).toHaveLength(1);
  });
});

describe('Settings status and account', () => {
  it('renders status values from the live store and refreshes on demand', async () => {
    fake = installSettingsFetch(tokenStatus(false));
    const liveMod = await import('$lib/stores/live');
    const fetchStatus = vi.fn().mockResolvedValue({ docker: 'ok', compose: 'v2.29.1', events: 'connected', metricsDegraded: false });
    const open = vi.fn((o: { onStatus: (s: string) => void }) => {
      o.onStatus('closed');
      return { close() {}, status: 'closed', lastEventId: '' };
    });
    liveMod.startLive({ fetchStatus, open: open as never });
    try {
      const user = userEvent.setup();
      mountSettings();
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
    fake = installSettingsFetch(tokenStatus(false));
    vi.spyOn(session, 'logout').mockResolvedValue({ ok: false });
    const user = userEvent.setup();
    mountSettings();
    await user.click(await screen.findByRole('button', { name: 'Log out' }));
    expect(session.logout).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(get(toasts).some((t) => /logout may not have completed/i.test(t.message))).toBe(true));
  });
});

describe('Settings v2 cards', () => {
  it('docker reads unknown, not unavailable, before any status arrives', async () => {
    fake = installSettingsFetch(tokenStatus(false));
    mountSettings();
    expect((await screen.findByTestId('status-docker')).textContent).toContain('unknown');
    expect(screen.getByTestId('status-docker').textContent).not.toContain('unavailable');
  });

  it('appearance buttons reflect and set the theme and density', async () => {
    fake = installSettingsFetch(tokenStatus(false));
    const dispose = initAppearance({ root: document.createElement('html'), storage: () => null });
    try {
      act(() => {
        setTheme('dark');
        setDensity('compact');
      });
      const user = userEvent.setup();
      mountSettings();
      await screen.findByRole('button', { name: 'Dark' });
      const pressed = (name: string) => screen.getByRole('button', { name }).getAttribute('aria-pressed');
      expect(pressed('Dark')).toBe('true');
      expect(pressed('Light')).toBe('false');
      expect(pressed('Compact')).toBe('true');
      await user.click(screen.getByRole('button', { name: 'Light' }));
      expect(pressed('Light')).toBe('true');
      expect(pressed('Dark')).toBe('false');
      await user.click(screen.getByRole('button', { name: 'System' }));
      expect(pressed('System')).toBe('true');
      await user.click(screen.getByRole('button', { name: 'Comfortable' }));
      expect(pressed('Comfortable')).toBe('true');
      expect(pressed('Compact')).toBe('false');
    } finally {
      dispose();
    }
  });

  it('system status shows docker version and metrics interval', async () => {
    fake = installSettingsFetch(tokenStatus(false));
    const liveMod = await import('$lib/stores/live');
    const fetchStatus = vi.fn().mockResolvedValue({
      docker: 'ok',
      dockerVersion: '27.1.1',
      compose: 'v2.29.1',
      events: 'connected',
      metricsDegraded: false,
      metricsIntervalSeconds: 15,
    });
    const open = vi.fn((o: { onStatus: (s: string) => void }) => {
      o.onStatus('closed');
      return { close() {}, status: 'closed', lastEventId: '' };
    });
    liveMod.startLive({ fetchStatus, open: open as never });
    try {
      const user = userEvent.setup();
      mountSettings();
      await user.click(await screen.findByRole('button', { name: 'Refresh' }));
      await waitFor(() => expect(screen.getByTestId('status-docker').textContent).toContain('27.1.1 · ok'));
      expect(screen.getByTestId('status-metrics').textContent).toContain('ok · 15 s');
    } finally {
      liveMod.stopLive();
    }
  });
});

describe('Settings layout', () => {
  it('the section nav links to every card', async () => {
    fake = installSettingsFetch(tokenStatus(false));
    mountSettings();
    const nav = await screen.findByRole('navigation', { name: 'Settings sections' });
    const links = within(nav).getAllByRole('link');
    const ids = ['settings-system', 'settings-updates', 'settings-prometheus', 'settings-appearance', 'settings-account'];
    expect(links.map((a) => a.getAttribute('href'))).toEqual(ids.map((id) => `#${id}`));
    expect(links.map((a) => a.textContent)).toEqual(['System', 'Updates', 'Prometheus', 'Appearance', 'Account']);
    for (const id of ids) expect(document.getElementById(id)).not.toBeNull();
    expect(screen.getAllByRole('heading', { level: 1 }).map((h) => h.textContent)).toEqual(['Settings']);
  });

  it('the route table titles /settings as Settings', () => {
    expect(findRoute('/settings')?.title).toBe('Settings');
  });
});
