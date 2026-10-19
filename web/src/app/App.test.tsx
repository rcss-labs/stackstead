// Port of web/src/App.test.ts for the React app frame (routing, session guards, focus, titles,
// banners, unsaved-changes guard). Screens are placeholders until Plans 17–22, so these App tests
// are deferred to the screen plans that make them meaningful:
// - "shows Container not found (not no longer exists) after navigating between containers" (Plan 19)
// - the "Image not found" / "Volume not found" text on storage detail paths (Plan 21)
// - the Login and Setup form details beyond the minimal login used here (Plan 22)
// - the /api/containers and /api/networks fetches asserted by the Containers/Networks route tests (Plans 19/21)
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { get } from '$lib/store';
import { rearmUnauthorized } from '$lib/api/client';
import { expireSession, session } from '$lib/api/session';
import { stopLive } from '$lib/stores/live';
import { stacks } from '$lib/stores/stacks';
import { setUnsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../vitest-setup';
import { App } from './App';
import { ROUTES } from './routes';

let fake: FakeFetch;
let stream: FakeStream;
let me: FakeResponse;
let setupStatus: boolean;
let status: { docker: string; compose: string; events: string; metricsDegraded: boolean };
let stacksRes: FakeResponse;
let running: unknown[];

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

beforeEach(() => {
  toasts.reset();
  rearmUnauthorized();
  document.title = '';
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
    'GET /api/host': () => ({ body: {} }),
    'GET /api/operations?state=running': () => ({ body: running }),
    'POST /api/logout': { status: 204 },
    'POST /api/login': { status: 204 },
    'POST /api/stacks/web/actions/restart': () => {
      running = [{ id: 'op1', stack: 'web', action: 'restart', state: 'running', startedAt: '2026-10-19T10:00:00Z' }];
      return { body: running[0] };
    },
  });
  stream = installFakeStream({ '/api/events': [] });
});
afterEach(async () => {
  // Unmount before resetting the stores so no guard reacts to the reset.
  cleanup();
  setUnsavedChanges(false);
  stopLive();
  expireSession();
  stream.restore();
  fake.restore();
  // useStore releases its store subscriptions on the next task after unmount.
  await sleep(5);
});

const user = () => userEvent.setup();
/** Replaces the URL and tells BrowserRouter, as a back/forward or external change would. */
const at = (path: string) =>
  act(() => {
    window.history.replaceState(null, '', path);
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
const push = (path: string) =>
  act(() => {
    window.history.pushState(null, '', path);
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
const loc = () => ({ path: window.location.pathname, next: new URLSearchParams(window.location.search).get('next') });
const mainNav = () => screen.getByRole('navigation', { name: 'Main' });

describe('App landmarks', () => {
  it.each(['/', '/stacks/new', '/stacks/import', '/settings', '/containers', '/networks', '/nope'])('has exactly one main landmark on %s', async (path) => {
    at(path);
    render(<App />);
    await screen.findByRole('navigation', { name: 'Main' });
    await screen.findByRole('heading', { level: 1 }).catch(() => undefined);
    expect(screen.getAllByRole('main')).toHaveLength(1);
  });

  it('has exactly one main landmark on Login and Setup', async () => {
    me = { status: 401, body: { error: 'unauthorized' } };
    at('/');
    const view = render(<App />);
    await screen.findByLabelText('Username');
    expect(screen.getAllByRole('main')).toHaveLength(1);
    view.unmount();
    setupStatus = true;
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: /set up/i });
    expect(screen.getAllByRole('main')).toHaveLength(1);
  });
});

describe('App focus on navigation', () => {
  it('moves focus to the page heading after a client-side navigation, not on first load', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    expect(document.activeElement).toBe(document.body);
    await user().click(within(mainNav()).getByRole('link', { name: 'Settings' }));
    const h1 = await screen.findByRole('heading', { level: 1, name: 'Settings' });
    await waitFor(() => expect(document.activeElement).toBe(h1));
  });

  it('does not move focus when only the ?tab= query changes', async () => {
    at('/settings');
    render(<App />);
    const h1 = await screen.findByRole('heading', { level: 1, name: 'Settings' });
    // Focus outside main, so only the path-vs-query rule (not "the page placed focus") keeps it there.
    const link = within(mainNav()).getByRole('link', { name: 'Settings' });
    link.focus();
    await push('/settings?tab=logs');
    await sleep(50);
    expect(document.activeElement).toBe(link);
    expect(document.activeElement).not.toBe(h1);
  });

  // Focus a new page placed inside main itself is guarded by useFocusOnNavigate.test.tsx "keeps focus a new page placed inside main".

  it("focuses a lazy screen's heading once it renders", async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    // No other test loads the container detail screen, so its lazy chunk is still pending here.
    await push('/containers/web');
    const h1 = await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(document.activeElement).toBe(h1));
  });

  it('collapses a burst of changes into at most two running-operations refetches', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    await sleep(500); // let the initial resync settle
    const runs = () => fake.calls.filter((c) => c.path === '/api/operations?state=running').length;
    const before = runs();
    for (let i = 1; i <= 10; i++) stream.push('/api/events', `event: change\ndata: {"project":"p${i}","seq":${i}}\n\n`);
    await sleep(1500);
    expect(runs() - before).toBeGreaterThanOrEqual(1);
    expect(runs() - before).toBeLessThanOrEqual(2);
  });
});

describe('App routing and session', () => {
  it('anonymous users land on Login carrying next', async () => {
    me = { status: 401, body: { error: 'unauthorized' } };
    at('/stacks/web?tab=logs');
    render(<App />);
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    await waitFor(() => expect(loc().path).toBe('/login'));
    expect(window.location.search).toBe('?next=%2Fstacks%2Fweb%3Ftab%3Dlogs');
    expect(loc().next).toBe('/stacks/web?tab=logs');
    expect(stream.requests).toHaveLength(0);
  });

  it('first run goes to Setup', async () => {
    me = { status: 401, body: { error: 'unauthorized' } };
    setupStatus = true;
    at('/');
    render(<App />);
    await waitFor(() => expect(loc().path).toBe('/setup'));
    expect(await screen.findByRole('heading', { name: /set up/i })).toBeTruthy();
  });

  it('authenticated users get the stack list, one events connection and a status fetch', async () => {
    at('/');
    render(<App />);
    expect(await screen.findByRole('heading', { name: 'Stacks' })).toBeTruthy();
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    await waitFor(() => expect(fake.calls.some((c) => c.path === '/api/status')).toBe(true));
    expect(mainNav()).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Log out' }).getAttribute('title')).toBe('admin · Log out');
  });

  it('renders the Containers page at /containers with its title', async () => {
    at('/containers');
    render(<App />);
    expect(await screen.findByRole('heading', { level: 1, name: 'Containers' })).toBeTruthy();
    await waitFor(() => expect(document.title).toBe('Containers · Stackstead'));
    expect(within(mainNav()).getByRole('link', { name: 'Containers' }).getAttribute('aria-current')).toBe('page');
  });

  it('renders Networks at /networks with its title and current navigation', async () => {
    at('/networks');
    render(<App />);
    expect(await screen.findByRole('heading', { level: 1, name: 'Networks' })).toBeTruthy();
    await waitFor(() => expect(document.title).toBe('Networks · Stackstead'));
    expect(within(mainNav()).getByRole('link', { name: 'Networks' }).getAttribute('aria-current')).toBe('page');
  });

  it('shows a not-found view with a link home for unknown paths', async () => {
    at('/nope/at/all');
    render(<App />);
    expect(await screen.findByRole('heading', { name: /not found/i })).toBeTruthy();
    await waitFor(() => expect(document.title).toBe('Page not found · Stackstead'));
    await user().click(within(screen.getByRole('main')).getByRole('link', { name: /stacks/i }));
    expect(loc().path).toBe('/');
  });

  it('a 401 from any call stops the live stream and shows Login once', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    const signal = stream.requests[0].signal!;
    const { refreshStacks } = await import('$lib/stores/stacks');
    stacksRes = { status: 401, body: { error: 'unauthorized' } };
    await act(async () => {
      await refreshStacks();
      await refreshStacks();
    });
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(signal.aborted).toBe(true);
    expect(get(session).state).toBe('anonymous');
    expect(loc().path).toBe('/login');
    expect(loc().next).toBeNull(); // "/" carries no next
    expect(stream.requests).toHaveLength(1);
  });

  it('stops polling operations after a session loss', async () => {
    running = [{ id: 'op1', stack: 'web', action: 'restart', state: 'running', startedAt: '2026-10-19T10:00:00Z' }];
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    const { refreshStacks } = await import('$lib/stores/stacks');
    stacksRes = { status: 401, body: { error: 'unauthorized' } };
    await act(async () => {
      await refreshStacks();
    });
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    const runs = () => fake.calls.filter((c) => c.path === '/api/operations?state=running').length;
    const before = runs();
    await sleep(500);
    expect(runs()).toBe(before);
  });

  it('nav links navigate through the router without a reload and mark the current page', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    const marker = screen.getByRole('main');
    await user().click(within(mainNav()).getByRole('link', { name: 'Settings' }));
    expect(loc().path).toBe('/settings');
    expect(await screen.findByRole('heading', { name: 'Settings' })).toBeTruthy();
    expect(screen.getByRole('main')).toBe(marker); // same document, same frame
    expect(within(mainNav()).getByRole('link', { name: 'Settings' }).getAttribute('aria-current')).toBe('page');
  });

  it('shows the running operations count in the nav', async () => {
    running = [
      { id: 'a', stack: 'x', action: 'up' },
      { id: 'b', stack: 'y', action: 'pull' },
    ];
    at('/');
    render(<App />);
    expect(await screen.findByText(/2 running/)).toBeTruthy();
  });

  it('logout returns to Login; a failed logout warns', async () => {
    at('/settings');
    render(<App />);
    await screen.findByRole('heading', { name: 'Settings' });
    fake.restore();
    fake = installFakeFetch({
      'GET /api/me': { status: 401, body: { error: 'x' } },
      'GET /api/setup/status': { body: { needsSetup: false } },
      'POST /api/logout': { status: 500, body: { error: 'boom' } },
    });
    await user().click(screen.getByRole('button', { name: 'Log out' }));
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(get(toasts).some((t) => t.kind === 'warning' && t.message === 'Logout may not have completed on the server')).toBe(true);
    expect(loc().path).toBe('/login');
    expect(loc().next).toBeNull();
  });

  it('confirms before a link leaves a page with unsaved changes', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    act(() => setUnsavedChanges(true));
    await user().click(within(mainNav()).getByRole('link', { name: 'Settings' }));
    const dlg = await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' });
    expect(loc().path).toBe('/');
    await user().click(within(dlg).getByRole('button', { name: 'Cancel' }));
    expect(loc().path).toBe('/');
    await user().click(within(mainNav()).getByRole('link', { name: 'Settings' }));
    await user().click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(loc().path).toBe('/settings'));
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy();
  });
});

describe('App session edge cases', () => {
  it('an authenticated user opening /login is sent to next, and /setup to the list', async () => {
    at('/login?next=%2Fsettings');
    render(<App />);
    await waitFor(() => expect(loc().path).toBe('/settings'));
    expect(screen.queryByRole('heading', { name: /not found/i })).toBeNull();
    at('/setup');
    await waitFor(() => expect(loc().path).toBe('/'));
  });

  it('/login?next=//evil sends an authenticated user to /', async () => {
    at('/login?next=%2F%2Fevil');
    render(<App />);
    expect(await screen.findByRole('heading', { level: 1, name: 'Stacks' })).toBeTruthy();
    expect(window.location.pathname + window.location.search).toBe('/');
  });

  it('a 401 on the events stream sends the user to login once', async () => {
    stream.restore();
    stream = installFakeStream({ '/api/events': { responses: [{ status: 401, body: { error: 'unauthorized' } }] } });
    at('/settings');
    render(<App />);
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(get(session).state).toBe('anonymous');
    expect(loc().path).toBe('/login');
    expect(loc().next).toBe('/settings');
    expect(stream.requests).toHaveLength(1);
  });

  it('a late API 401 after an events-stream 401 keeps next', async () => {
    stream.restore();
    stream = installFakeStream({ '/api/events': { responses: [{ status: 401, body: { error: 'unauthorized' } }] } });
    at('/settings');
    render(<App />);
    expect(await screen.findByLabelText('Username')).toBeTruthy();
    expect(loc().next).toBe('/settings');
    stacksRes = { status: 401, body: {} };
    const { request } = await import('$lib/api/client');
    await act(async () => {
      await request('GET', '/api/stacks').catch(() => {});
    });
    await sleep(20);
    expect(loc().path).toBe('/login');
    expect(loc().next).toBe('/settings');
  });

  it('logging in again after a 401 opens exactly one new events stream', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    const { refreshStacks } = await import('$lib/stores/stacks');
    stacksRes = { status: 401, body: {} };
    await act(() => refreshStacks());
    await screen.findByLabelText('Username');
    stacksRes = { body: [] };
    fake.calls.length = 0;
    const u = user();
    await u.type(screen.getByLabelText('Username'), 'admin');
    await u.type(screen.getByLabelText('Password'), 'pw');
    await u.click(screen.getByRole('button', { name: /log in|sign in/i }));
    await screen.findByRole('heading', { name: 'Stacks' });
    await sleep(100);
    expect(stream.requests).toHaveLength(2);
    expect(stream.requests[0].signal!.aborted).toBe(true);
    expect(loc().path).toBe('/');
  });

  it('logout asks first when there are unsaved changes', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    act(() => setUnsavedChanges(true));
    await user().click(screen.getByRole('button', { name: 'Log out' }));
    const dlg = await screen.findByRole('alertdialog');
    expect(get(session).state).toBe('authenticated');
    await user().click(within(dlg).getByRole('button', { name: 'Cancel' }));
    expect(get(session).state).toBe('authenticated');
  });

  it('download links are not intercepted by the unsaved-changes guard', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    act(() => setUnsavedChanges(true));
    const a = document.createElement('a');
    a.href = '/export.txt';
    a.setAttribute('download', '');
    a.textContent = 'dl';
    screen.getByRole('main').append(a);
    a.addEventListener('click', (e) => e.preventDefault());
    fireEvent.click(a);
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });
});

describe('App banners', () => {
  it('docker unreachable when status says unavailable', async () => {
    status = { ...status, docker: 'unavailable' };
    at('/');
    render(<App />);
    expect(await screen.findByText(/Docker is unreachable/)).toBeTruthy();
    expect(screen.getByText('Status may be stale; lifecycle actions are disabled.')).toBeTruthy();
  });

  it('docker unreachable when the events stream reports disconnected', async () => {
    at('/');
    render(<App />);
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    act(() => stream.push('/api/events', 'event: status\ndata: {"events":"disconnected","metricsDegraded":false}\n\n'));
    expect(await screen.findByText(/Docker is unreachable/)).toBeTruthy();
  });

  it('compose unavailable and metrics degraded banners', async () => {
    status = { ...status, compose: 'unavailable' };
    at('/');
    render(<App />);
    expect(await screen.findByText(/docker compose plugin not found/)).toBeTruthy();
    expect(screen.queryByText(/metrics/i, { selector: 'strong' })).toBeNull();
    act(() => stream.push('/api/events', 'event: status\ndata: {"events":"connected","metricsDegraded":true}\n\n'));
    expect(await screen.findByText(/Metrics are degraded/)).toBeTruthy();
  });

  it('no banners when everything is healthy', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(fake.calls.some((c) => c.path === '/api/status')).toBe(true));
    expect(screen.queryByText(/unreachable|not found|degraded|reconnecting/i, { selector: 'strong' })).toBeNull();
  });
});

it('renders Storage at /storage with title and current rail', async () => {
  at('/storage');
  render(<App />);
  expect(await screen.findByRole('heading', { level: 1, name: 'Storage' })).toBeTruthy();
  await waitFor(() => expect(document.title).toBe('Storage · Stackstead'));
  expect(within(mainNav()).getByRole('link', { name: 'Storage' }).getAttribute('aria-current')).toBe('page');
});

it.each([
  ['/storage/images/aaaaaaaaaaaa', 'Images'],
  ['/storage/volumes/missing', 'Volumes'],
])('routes storage detail %s with title and breadcrumbs', async (path, crumb) => {
  at(path);
  render(<App />);
  await screen.findByRole('heading', { level: 1 });
  await waitFor(() => expect(document.title).toBe('Storage · Stackstead'));
  expect(screen.getByRole('navigation', { name: 'Breadcrumb' }).textContent).toContain(crumb);
});

it('focuses the storage detail heading after navigation while its request is pending', async () => {
  at('/networks');
  render(<App />);
  await screen.findByRole('heading', { level: 1 });
  at('/storage/images/aaaaaaaaaaaa');
  const heading = await screen.findByRole('heading', { level: 1, name: /Image/ });
  await waitFor(() => expect(document.activeElement).toBe(heading));
});

it('every route is lazy except the overview', () => {
  const lazy = Symbol.for('react.lazy');
  const kind = (screenComponent: unknown) => (screenComponent as { $$typeof?: symbol }).$$typeof;
  expect(ROUTES.map((r) => r.path)).toEqual([
    '/',
    '/stacks/new',
    '/stacks/import',
    '/stacks/:name',
    '/containers',
    '/containers/:name',
    '/networks',
    '/storage',
    '/storage/images/:id',
    '/storage/volumes/:name',
    '/settings',
    '/login',
    '/setup',
    '*',
  ]);
  for (const route of ROUTES) {
    if (route.path === '/') expect(kind(route.screen)).not.toBe(lazy);
    else expect(kind(route.screen), route.path).toBe(lazy);
  }
});

describe('App command palette', () => {
  it('opens from the sidebar search button and from Ctrl+K, and is not mounted while signed out', async () => {
    me = { status: 401, body: { error: 'unauthorized' } };
    at('/');
    const out = render(<App />);
    await screen.findByLabelText('Username');
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull();
    out.unmount();

    me = { body: { username: 'admin' } };
    at('/');
    render(<App />);
    await screen.findByRole('navigation', { name: 'Main' });
    await user().click(screen.getAllByRole('button', { name: 'Search or run a command' })[0]);
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeTruthy();
    await user().keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeTruthy();
  });
});

const webStack = {
  name: 'web',
  path: '/srv/web/compose.yaml',
  project: 'web',
  origin: 'managed',
  status: 'running',
  serviceCount: 2,
  running: 2,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
};

async function openPalette() {
  fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
  return screen.findByRole('dialog', { name: 'Command palette' });
}

describe('App command palette actions and the unsaved-changes guard', () => {
  it('re-checks running operations after an action so the pill shows and the stack is busy', async () => {
    stacksRes = { body: [webStack] };
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    await waitFor(() => expect(get(stacks).items).toHaveLength(1));
    const runs = () => fake.calls.filter((c) => c.path === '/api/operations?state=running').length;
    await sleep(500); // let the initial fetch settle
    const before = runs();
    const u = user();
    await openPalette();
    await u.type(screen.getByRole('combobox'), 'restart web');
    await u.keyboard('{Enter}');
    await waitFor(() => expect(runs()).toBeGreaterThan(before), { timeout: 2000 });
    // One running operation reads "<action> · <stack>" in the top-bar pill.
    await waitFor(() => expect(screen.getAllByRole('status').some((el) => el.textContent?.includes('restart · web'))).toBe(true), { timeout: 2000 });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await openPalette();
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'restart web' } });
    const option = screen.getByRole('option', { name: /Restart web/ });
    expect(option.getAttribute('aria-disabled')).toBe('true');
    expect(within(option).getByText('an operation is already running')).toBeTruthy();
  });

  it('a Go to entry asks before leaving unsaved changes, and navigates when clean', async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    act(() => setUnsavedChanges(true));
    const u = user();
    await openPalette();
    await u.click(screen.getByRole('option', { name: /Settings/ }));
    expect(await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' })).toBeTruthy();
    expect(loc().path).toBe('/');
    await u.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }));
    act(() => setUnsavedChanges(false));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    await openPalette();
    await u.click(screen.getByRole('option', { name: /Settings/ }));
    await waitFor(() => expect(loc().path).toBe('/settings'));
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it("a toast's View link asks before leaving unsaved changes, and navigates when clean", async () => {
    at('/');
    render(<App />);
    await screen.findByRole('heading', { name: 'Stacks' });
    act(() => setUnsavedChanges(true));
    act(() => void toasts.push({ kind: 'info', message: 'restart · web', timeoutMs: 0, link: { label: 'View', href: '/settings?tab=x' } }));
    const u = user();
    await u.click(screen.getByRole('link', { name: 'View' }));
    expect(await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' })).toBeTruthy();
    expect(loc().path).toBe('/');
    await u.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }));
    act(() => setUnsavedChanges(false));
    await u.click(screen.getByRole('link', { name: 'View' }));
    await waitFor(() => expect(loc().path).toBe('/settings'));
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });
});
