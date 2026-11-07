import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { initAppearance } from '$lib/stores/appearance';
import { installFakeFetch, type FakeFetch, type Routes } from '../../vitest-setup';
import { mountAt } from '../containers/containersTestKit';

let fake: FakeFetch;
const DECOY_SECRET = 'ghp_DECOYSECRET0123456789';

const baseRoutes: Routes = {
  'GET /api/settings/update': {
    body: { current: '0.2.0', installer: 'script', command: 'sudo stackstead upgrade', state: 'up-to-date', latest: '0.2.0', notesURL: '', error: '' },
  },
  // The extra token field stands in for anything the server might add to this payload later.
  'GET /api/peers/self': {
    body: { instanceId: 'i', name: 'Here', slug: 'here', advertisedUrl: '', suggestedUrl: '', version: '0.2.0', token: DECOY_SECRET },
  },
  'GET /api/settings/metrics-token': { body: { configured: false } },
  'GET /api/settings/github': { body: { configured: true, login: 'octocat' } },
  'GET /api/settings/registries': {
    body: [
      { id: 'r1', name: 'Docker Hub', kind: 'dockerhub', host: 'docker.io', username: 'pavel', region: '', mode: '', profile: '', accountId: '', configured: true, lastTestAt: '', lastTestOk: false, lastTestMessage: '' },
    ],
  },
};

beforeEach(() => {
  fake = installFakeFetch(baseRoutes);
});
afterEach(() => {
  fake.restore();
  localStorage.clear();
  vi.restoreAllMocks();
});

const pathname = () => screen.getByTestId('location').textContent;

describe('Settings sections', () => {
  it.each([
    ['system', 'System status'],
    ['updates', 'Software update'],
    ['prometheus', 'Prometheus token'],
    ['servers', 'Connected servers'],
    ['registries', 'Registries'],
    ['github', 'GitHub'],
    ['images', 'Images'],
    ['backups', 'Backups'],
    ['appearance', 'Appearance'],
    ['account', 'Account'],
  ])('each section renders at /settings/%s', async (section, heading) => {
    mountAt(`/settings/${section}`);
    expect(await screen.findByRole('heading', { level: 2, name: heading })).toBeTruthy();
    const nav = screen.getByRole('navigation', { name: 'Settings sections' });
    const current = within(nav).getAllByRole('link').filter((a) => a.getAttribute('aria-current') === 'page');
    expect(current.map((a) => a.getAttribute('href'))).toEqual([`/settings/${section}`]);
  });

  it('shows one section at a time', async () => {
    mountAt('/settings/account');
    await screen.findByRole('heading', { level: 2, name: 'Account' });
    expect(screen.queryByRole('heading', { level: 2, name: 'System status' })).toBeNull();
    expect(screen.queryByRole('heading', { level: 2, name: 'GitHub' })).toBeNull();
  });

  it('unknown section falls back to system', async () => {
    mountAt('/settings/nope');
    expect(await screen.findByRole('heading', { level: 2, name: 'System status' })).toBeTruthy();
    await waitFor(() => expect(pathname()).toBe('/settings/system'));
  });

  it('old hash links open the matching section', async () => {
    mountAt('/settings#settings-registries');
    expect(await screen.findByRole('heading', { level: 2, name: 'Registries' })).toBeTruthy();
    await waitFor(() => expect(pathname()).toBe('/settings/registries'));
  });

  it('an old hash link also wins over a section already in the path', async () => {
    mountAt('/settings/system#settings-github');
    expect(await screen.findByRole('heading', { level: 2, name: 'GitHub' })).toBeTruthy();
  });

  it('an unknown hash leaves the section alone', async () => {
    mountAt('/settings/account#settings-nope');
    expect(await screen.findByRole('heading', { level: 2, name: 'Account' })).toBeTruthy();
  });

  it('section links navigate in the app', async () => {
    const user = userEvent.setup();
    mountAt('/settings/system');
    await user.click(within(await screen.findByRole('navigation', { name: 'Settings sections' })).getByRole('link', { name: 'Account' }));
    expect(await screen.findByRole('heading', { level: 2, name: 'Account' })).toBeTruthy();
    expect(pathname()).toBe('/settings/account');
  });
});

describe('Settings test buttons', () => {
  it('GitHub test reports success and failure', async () => {
    const user = userEvent.setup();
    let ok = true;
    fake.restore();
    fake = installFakeFetch({
      ...baseRoutes,
      'POST /api/settings/github/test': () => ({ body: ok ? { ok: true, login: 'octocat', message: 'Token works' } : { ok: false, login: '', message: 'Bad credentials' } }),
    });
    mountAt('/settings/github');
    await user.click(await screen.findByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText(/Connected: Token works/)).toBeTruthy();
    ok = false;
    await user.click(screen.getByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText(/Failed: Bad credentials/)).toBeTruthy();
  });

  it('registry test reports success and failure', async () => {
    const user = userEvent.setup();
    let ok = true;
    fake.restore();
    fake = installFakeFetch({
      ...baseRoutes,
      'POST /api/settings/registries/r1/test': () => ({ body: ok ? { ok: true, message: 'Login succeeded' } : { ok: false, message: 'unauthorized' } }),
    });
    mountAt('/settings/registries');
    await user.click(await screen.findByRole('button', { name: 'Test Docker Hub' }));
    expect(await screen.findByText(/Login succeeded/)).toBeTruthy();
    ok = false;
    await user.click(screen.getByRole('button', { name: 'Test Docker Hub' }));
    expect(await screen.findByText(/unauthorized/)).toBeTruthy();
  });
});

describe('Settings diagnostics', () => {
  it('Copy diagnostics includes version and status and excludes secrets', async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined);
    mountAt('/settings/system');
    // The version arrives from the server; wait for it before copying.
    await screen.findByText('0.2.0');
    await user.click(screen.getByRole('button', { name: 'Copy diagnostics' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const text = writeText.mock.calls[0][0];
    expect(text).toContain('Stackstead 0.2.0');
    expect(text).toMatch(/Docker: /);
    expect(text).toMatch(/Events: /);
    expect(text).toMatch(/Metrics: /);
    expect(text).not.toContain(DECOY_SECRET);
    expect(text).not.toMatch(/token|password|secret/i);
  });
});

describe('Settings appearance', () => {
  it('theme and density controls still persist', async () => {
    const user = userEvent.setup();
    const dispose = initAppearance({ root: document.createElement('html') });
    try {
      mountAt('/settings/appearance');
      await user.click(await screen.findByRole('button', { name: 'Light' }));
      await user.click(screen.getByRole('button', { name: 'Comfortable' }));
      expect(Object.values({ ...localStorage })).toEqual(expect.arrayContaining(['light', 'comfortable']));
    } finally {
      dispose();
    }
  });
});

const CONFIG = {
  schedule: { enabled: true, frequency: 'weekly', weekday: 1, time: '03:00' },
  local: { enabled: true, path: '/var/backups/stackstead', keepLast: 7 },
  s3: { enabled: true, bucket: 'atlas-backups', prefix: 'stackstead', region: 'eu-central-1', endpoint: '', pathStyle: false, accessKeyId: 'AKIAEXAMPLE', secretKeySet: true },
  encryption: { enabled: true, passphraseSet: true, keyId: 'k1' },
  pausedAfterRestore: false,
};

describe('Settings registries', () => {
  it('registries list first and the form opens on Add', async () => {
    const user = userEvent.setup();
    mountAt('/settings/registries');
    expect(await screen.findByRole('button', { name: 'Edit Docker Hub' })).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByLabelText('Name')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Add registry' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByLabelText('Name')).toBeTruthy();
  });
});

describe('Settings backups', () => {
  it('backups section summarises and links instead of embedding the schedule form', async () => {
    fake.restore();
    fake = installFakeFetch({ ...baseRoutes, 'GET /api/backups/config': { body: CONFIG } });
    mountAt('/settings/backups');
    expect(await screen.findByText('Weekly on Monday at 03:00')).toBeTruthy();
    expect(screen.getByText(/\/var\/backups\/stackstead/)).toBeTruthy();
    expect(screen.getByText(/s3:\/\/atlas-backups\/stackstead/)).toBeTruthy();
    expect(screen.getByText('On')).toBeTruthy();
    expect(screen.getByRole('link', { name: /backup schedule/i }).getAttribute('href')).toBe('/backups?tab=schedule');
    // No editing controls and no secrets here.
    expect(screen.queryByRole('button', { name: /save/i })).toBeNull();
    expect(screen.queryByLabelText(/passphrase|secret/i)).toBeNull();
    expect(document.body.textContent).not.toContain('AKIAEXAMPLE');
  });

  it('shows the link and a notice, with no invented values, when the config cannot load', async () => {
    fake.restore();
    fake = installFakeFetch({ ...baseRoutes, 'GET /api/backups/config': { status: 500, body: { error: 'boom' } } });
    mountAt('/settings/backups');
    expect(await screen.findByText(/Could not load the backup settings/)).toBeTruthy();
    expect(screen.getByRole('link', { name: /backup schedule/i }).getAttribute('href')).toBe('/backups?tab=schedule');
    expect(screen.queryByText('Schedule')).toBeNull();
  });
});
