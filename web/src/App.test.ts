import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { rearmUnauthorized } from '$lib/api/client';
import { expireSession, session } from '$lib/api/session';
import { navigate, route } from '$lib/router';
import { stopLive } from '$lib/stores/live';
import { setUnsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from './vitest-setup';
import App from './App.svelte';

let fake: FakeFetch;
let stream: FakeStream;
let me: FakeResponse;
let setupStatus: boolean;
let status: { docker: string; compose: string; events: string; metricsDegraded: boolean };
let stacksRes: FakeResponse;
let running: unknown[];

beforeEach(() => {
  toasts.reset();
  rearmUnauthorized();
  me = { body: { username: 'admin' } };
  setupStatus = false;
  status = { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false };
  stacksRes = { body: [] };
  running = [];
  fake = installFakeFetch({
    'GET /api/me': () => me,
    'GET /api/setup/status': () => ({ body: { needsSetup: setupStatus } }),
    'GET /api/status': () => ({ body: status }),
    'GET /api/stacks': () => stacksRes,
    'GET /api/containers': () => ({ body: [] }),
    'GET /api/networks': () => ({ body: [] }),
    'GET /api/operations?state=running': () => ({ body: running }),
    'POST /api/logout': { status: 204 },
    'POST /api/login': { status: 204 },
    'GET /api/settings/metrics-token': { body: { configured: false } },
  });
  stream = installFakeStream({ '/api/events': [] });
});
afterEach(() => {
  setUnsavedChanges(false);
  stopLive();
  expireSession();
  stream.restore();
  fake.restore();
});

const user = () => userEvent.setup();
const at = (path: string) => navigate(path, { replace: true });

describe('App landmarks', () => {
  it.each(['/', '/stacks/new', '/stacks/import', '/settings', '/containers', '/networks', '/nope'])('has exactly one main landmark on %s', async (path) => {
    at(path);
    render(App);
    await screen.findByRole('navigation', { name: 'Main' });
    await screen.findByRole('heading', { level: 1 }).catch(() => undefined);
    expect(screen.getAllByRole('main')).toHaveLength(1);
  });

  it('has exactly one main landmark on Login and Setup', async () => {
    me = { status: 401, body: { error: 'unauthorized' } };
    at('/');
    const view = render(App);
    await screen.findByLabelText('Username');
    expect(screen.getAllByRole('main')).toHaveLength(1);
    view.unmount();
    setupStatus = true;
    at('/');
    render(App);
    await screen.findByRole('heading', { name: /set up/i });
    expect(screen.getAllByRole('main')).toHaveLength(1);
  });
});

describe('App focus on navigation', () => {
  it('moves focus to the page heading after a client-side navigation, not on first load', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    expect(document.activeElement).toBe(document.body);
    await user().click(screen.getByRole('link', { name: 'Settings' }));
    const h1 = await screen.findByRole('heading', { level: 1, name: 'Settings' });
    await waitFor(() => expect(document.activeElement).toBe(h1));
  });

  it('does not move focus when only the ?tab= query changes', async () => {
    at('/settings');
    render(App);
    const h1 = await screen.findByRole('heading', { level: 1, name: 'Settings' });
    const button = within(screen.getByRole('main')).getAllByRole('button')[0];
    button.focus();
    navigate('/settings?tab=logs');
    await new Promise((r) => setTimeout(r, 50));
    expect(document.activeElement).toBe(button);
    expect(document.activeElement).not.toBe(h1);
  });

  it('does not steal focus the new page already placed inside main', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    const main = screen.getByRole('main');
    // Runs as soon as the new page's DOM exists, before the app's post-render focus step.
    const observer = new MutationObserver(() => {
      const field = main.querySelector<HTMLElement>('input, button');
      if (field && main.querySelector('h1')?.textContent === 'Settings') {
        field.focus();
        observer.disconnect();
      }
    });
    observer.observe(main, { childList: true, subtree: true });
    navigate('/settings');
    const h1 = await screen.findByRole('heading', { level: 1, name: 'Settings' });
    await new Promise((r) => setTimeout(r, 50));
    expect(main.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).not.toBe(h1);
    observer.disconnect();
  });

  it('collapses a burst of changes into at most two running-operations refetches', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 500)); // let the initial resync settle
    const runs = () => fake.calls.filter((c) => c.path === '/api/operations?state=running').length;
    const before = runs();
    for (let i = 1; i <= 10; i++) stream.push('/api/events', `event: change\ndata: {"project":"p${i}","seq":${i}}\n\n`);
    await new Promise((r) => setTimeout(r, 1500));
    expect(runs() - before).toBeGreaterThanOrEqual(1);
    expect(runs() - before).toBeLessThanOrEqual(2);
  });
});

describe('App routing and session', () => {
  it('anonymous users land on Login carrying next', async () => {
    me = { status: 401, body: { error: 'unauthorized' } };
    at('/stacks/web?tab=logs');
    render(App);
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(get(route).path).toBe('/login');
    expect(get(route).query.get('next')).toBe('/stacks/web?tab=logs');
    expect(stream.requests).toHaveLength(0);
  });

  it('first run goes to Setup', async () => {
    me = { status: 401, body: { error: 'unauthorized' } };
    setupStatus = true;
    at('/');
    render(App);
    await waitFor(() => expect(get(route).path).toBe('/setup'));
    expect(await screen.findByRole('heading', { name: /set up/i })).toBeTruthy();
  });

  it('authenticated users get the stack list, one events connection and a status fetch', async () => {
    at('/');
    render(App);
    expect(await screen.findByRole('heading', { name: 'Stacks' })).toBeTruthy();
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    await waitFor(() => expect(fake.calls.some((c) => c.path === '/api/status')).toBe(true));
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Log out' }).getAttribute('title')).toBe('admin · Log out');
  });

  it('renders the Containers page at /containers with its title', async () => {
    at('/containers');
    render(App);
    expect(await screen.findByRole('heading', { level: 1, name: 'Containers' })).toBeTruthy();
    await waitFor(() => expect(document.title).toBe('Containers · Stackstead'));
    expect(within(screen.getByRole('navigation', { name: 'Main' })).getByRole('link', { name: 'Containers' }).getAttribute('aria-current')).toBe('page');
    await waitFor(() => expect(fake.calls.some((c) => c.path === '/api/containers')).toBe(true));
  });

  it('renders Networks at /networks with its title and current navigation', async () => {
    at('/networks'); render(App);
    expect(await screen.findByRole('heading', { level: 1, name: 'Networks' })).toBeTruthy();
    await waitFor(() => expect(document.title).toBe('Networks · Stackstead'));
    expect(screen.getByRole('link', { name: 'Networks' }).getAttribute('aria-current')).toBe('page');
    await waitFor(() => expect(fake.calls.some(c => c.path === '/api/networks')).toBe(true));
  });

  it('shows "Container not found" (not "no longer exists") after navigating from a found container to an unknown one', async () => {
    stream.restore();
    fake.restore();
    fake = installFakeFetch({
      'GET /api/me': () => me,
      'GET /api/setup/status': () => ({ body: { needsSetup: false } }),
      'GET /api/status': () => ({ body: status }),
      'GET /api/stacks': () => stacksRes,
      'GET /api/containers': () => ({
        body: [{ id: 'aaa000aaa000', name: 'a', image: 'x:1', state: 'running', status: 'Up', health: 'none', exitCode: null, createdAt: '2026-01-01T00:00:00Z', project: '', service: '', stack: '', managed: 'none', ports: [], networks: {}, cpu: null, mem: null, memLimit: null, restarts24h: 0 }],
      }),
      'GET /api/containers/aaa000aaa000': { status: 500, body: { error: 'x' } },
      'GET /api/operations?state=running': () => ({ body: [] }),
    });
    stream = installFakeStream({ '/api/events': [] });
    at('/containers/a');
    render(App);
    await screen.findByRole('heading', { level: 1, name: 'a' });
    navigate('/containers/b');
    await screen.findByText('Container not found');
    expect(screen.queryByText('This container no longer exists')).toBeNull();
  });

  it('shows a not-found view with a link home for unknown paths', async () => {
    at('/nope/at/all');
    render(App);
    expect(await screen.findByRole('heading', { name: /not found/i })).toBeTruthy();
    await user().click(within(screen.getByRole('main')).getByRole('link', { name: /stacks/i }));
    expect(get(route).path).toBe('/');
  });

  it('a 401 from any call stops the live stream and shows Login once', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    const signal = stream.requests[0].signal!;
    const { refreshStacks } = await import('$lib/stores/stacks');
    stacksRes = { status: 401, body: { error: 'unauthorized' } };
    await refreshStacks();
    await refreshStacks();
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(signal.aborted).toBe(true);
    expect(get(session).state).toBe('anonymous');
    expect(get(route).path).toBe('/login');
    expect(stream.requests).toHaveLength(1);
  });

  it('nav links navigate through the router without a reload and mark the current page', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    const nav = screen.getByRole('navigation', { name: 'Main' });
    await user().click(within(nav).getByRole('link', { name: 'Settings' }));
    expect(get(route).path).toBe('/settings');
    expect(await screen.findByRole('heading', { name: 'Settings' })).toBeTruthy();
    expect(within(nav).getByRole('link', { name: 'Settings' }).getAttribute('aria-current')).toBe('page');
  });

  it('shows the running operations count in the nav', async () => {
    running = [{ id: 'a', stack: 'x' }, { id: 'b', stack: 'y' }];
    at('/');
    render(App);
    expect(await screen.findByText(/2 running/)).toBeTruthy();
  });

  it('logout returns to Login; a failed logout warns', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    fake.restore();
    fake = installFakeFetch({
      'GET /api/me': { status: 401, body: { error: 'x' } },
      'GET /api/setup/status': { body: { needsSetup: false } },
      'POST /api/logout': { status: 500, body: { error: 'boom' } },
    });
    await user().click(screen.getByRole('button', { name: 'Log out' }));
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(get(toasts).some((t) => t.kind === 'warning' && /logout/i.test(t.message))).toBe(true);
    expect(get(route).path).toBe('/login');
    expect(get(route).query.get('next')).toBeNull();
  });

  it('confirms before a link leaves a page with unsaved changes', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    setUnsavedChanges(true);
    const nav = screen.getByRole('navigation', { name: 'Main' });
    await user().click(within(nav).getByRole('link', { name: 'Settings' }));
    const dlg = await screen.findByRole('alertdialog');
    expect(get(route).path).toBe('/');
    await user().click(within(dlg).getByRole('button', { name: 'Cancel' }));
    expect(get(route).path).toBe('/');
    await user().click(within(nav).getByRole('link', { name: 'Settings' }));
    await user().click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(get(route).path).toBe('/settings'));
  });
});

describe('App session edge cases', () => {
  it('an authenticated user opening /login is sent to next, and /setup to the list', async () => {
    at('/login?next=%2Fsettings');
    render(App);
    await waitFor(() => expect(get(route).path).toBe('/settings'));
    expect(screen.queryByRole('heading', { name: /not found/i })).toBeNull();
    at('/setup');
    await waitFor(() => expect(get(route).path).toBe('/'));
  });

  it('a 401 on the events stream sends the user to login once', async () => {
    stream.restore();
    stream = installFakeStream({ '/api/events': { responses: [{ status: 401, body: { error: 'unauthorized' } }] } });
    at('/settings');
    render(App);
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(get(session).state).toBe('anonymous');
    expect(get(route).path).toBe('/login');
    expect(get(route).query.get('next')).toBe('/settings');
    expect(stream.requests).toHaveLength(1);
  });

  it('a late API 401 after an events-stream 401 keeps next', async () => {
    stream.restore();
    stream = installFakeStream({ '/api/events': { responses: [{ status: 401, body: { error: 'unauthorized' } }] } });
    at('/settings');
    render(App);
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(get(route).query.get('next')).toBe('/settings');
    stacksRes = { status: 401, body: {} };
    const { request } = await import('$lib/api/client');
    await request('GET', '/api/stacks').catch(() => {});
    expect(get(route).path).toBe('/login');
    expect(get(route).query.get('next')).toBe('/settings');
  });

  it('logging in again after a 401 opens exactly one new events stream', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    const { refreshStacks } = await import('$lib/stores/stacks');
    stacksRes = { status: 401, body: {} };
    await refreshStacks();
    await screen.findByLabelText('Username');
    stacksRes = { body: [] };
    fake.calls.length = 0;
    const u = user();
    await u.type(screen.getByLabelText('Username'), 'admin');
    await u.type(screen.getByLabelText('Password'), 'pw');
    await u.click(screen.getByRole('button', { name: /log in|sign in/i }));
    await screen.findByRole('heading', { name: 'Stacks' });
    await new Promise((r) => setTimeout(r, 100));
    expect(stream.requests).toHaveLength(2);
    expect(stream.requests[0].signal!.aborted).toBe(true);
    expect(get(route).path).toBe('/');
  });

  it('logout asks first when there are unsaved changes', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    setUnsavedChanges(true);
    await user().click(screen.getByRole('button', { name: 'Log out' }));
    const dlg = await screen.findByRole('alertdialog');
    expect(get(session).state).toBe('authenticated');
    await user().click(within(dlg).getByRole('button', { name: 'Cancel' }));
    expect(get(session).state).toBe('authenticated');
  });

  it('download links are not intercepted by the unsaved-changes guard', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    setUnsavedChanges(true);
    const a = document.createElement('a');
    a.href = '/export.txt';
    a.setAttribute('download', '');
    a.textContent = 'dl';
    screen.getByRole('main').append(a);
    a.addEventListener('click', (e) => e.preventDefault());
    await fireEvent.click(a);
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });
});

describe('App banners', () => {
  it('docker unreachable when status says unavailable', async () => {
    status = { ...status, docker: 'unavailable' };
    at('/');
    render(App);
    expect(await screen.findByText(/Docker is unreachable/)).toBeTruthy();
  });

  it('docker unreachable when the events stream reports disconnected', async () => {
    at('/');
    render(App);
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    stream.push('/api/events', 'event: status\ndata: {"events":"disconnected","metricsDegraded":false}\n\n');
    expect(await screen.findByText(/Docker is unreachable/)).toBeTruthy();
  });

  it('compose unavailable and metrics degraded banners', async () => {
    status = { ...status, compose: 'unavailable' };
    at('/');
    render(App);
    expect(await screen.findByText(/docker compose plugin not found/)).toBeTruthy();
    expect(screen.queryByText(/metrics/i, { selector: 'strong' })).toBeNull();
    stream.push('/api/events', 'event: status\ndata: {"events":"connected","metricsDegraded":true}\n\n');
    expect(await screen.findByText(/Metrics are degraded/)).toBeTruthy();
  });

  it('no banners when everything is healthy', async () => {
    at('/');
    render(App);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(fake.calls.some((c) => c.path === '/api/status')).toBe(true));
    expect(screen.queryByText(/unreachable|not found|degraded/i, { selector: 'strong' })).toBeNull();
  });
});

it('renders Storage at /storage with title and current rail', async () => {
  at('/storage'); render(App);
  expect(await screen.findByRole('heading', { level: 1, name: 'Storage' })).toBeTruthy();
  await waitFor(() => expect(document.title).toBe('Storage · Stackstead'));
  expect(screen.getByRole('link', { name: 'Storage' }).getAttribute('aria-current')).toBe('page');
});
