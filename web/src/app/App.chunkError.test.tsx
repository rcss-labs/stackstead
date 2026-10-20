// A lazy screen whose chunk fails to load (e.g. asset hashes changed by an upgrade while a tab was open)
// must not blank the app: the shell stays and the page area offers a reload.
import { act, cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { rearmUnauthorized } from '$lib/api/client';
import { expireSession } from '$lib/api/session';
import { stopLive } from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../vitest-setup';
import { App } from './App';
import { resetPalettePrefetchForTests } from './shell/PaletteHost';

vi.mock('./routes/NetworkList', () => {
  throw new Error('Failed to fetch dynamically imported module');
});
vi.mock('./routes/Login', () => {
  throw new Error('Failed to fetch dynamically imported module');
});

let fake: FakeFetch;
let stream: FakeStream;
let me: FakeResponse;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

beforeEach(() => {
  toasts.reset();
  rearmUnauthorized();
  // React reports errors caught by a boundary on the console; the fallback is what is under test.
  vi.spyOn(console, 'error').mockImplementation(() => {});
  me = { body: { username: 'admin' } };
  fake = installFakeFetch({
    'GET /api/me': () => me,
    'GET /api/setup/status': { body: { needsSetup: false } },
    'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    'GET /api/stacks': { body: [] },
    'GET /api/host': { body: {} },
    'GET /api/operations?state=running': { body: [] },
  });
  stream = installFakeStream({ '/api/events': [] });
});
afterEach(async () => {
  resetPalettePrefetchForTests();
  cleanup();
  stopLive();
  expireSession();
  stream.restore();
  fake.restore();
  vi.restoreAllMocks();
  await sleep(5);
});

const at = (path: string) =>
  act(() => {
    window.history.replaceState(null, '', path);
    window.dispatchEvent(new PopStateEvent('popstate'));
  });

describe('App when a lazy screen fails to load', () => {
  it('keeps the shell and shows a reload fallback inside main', async () => {
    at('/networks');
    render(<App />);
    // The boot spinner is a main too; wait for the page before taking the landmark.
    await screen.findByRole('heading', { name: 'Could not load this page' });
    const main = screen.getByRole('main');
    expect(within(main).getByRole('heading', { name: 'Could not load this page' })).toBeTruthy();
    expect(within(main).getByRole('button', { name: 'Reload' })).toBeTruthy();
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeTruthy();
    expect(screen.getAllByRole('main')).toHaveLength(1);
  });

  it('recovers when the user navigates to another page', async () => {
    at('/networks');
    render(<App />);
    await screen.findByRole('heading', { name: 'Could not load this page' });
    await userEvent.setup().click(within(screen.getByRole('navigation', { name: 'Main' })).getByRole('link', { name: 'Settings' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'Could not load this page' })).toBeNull();
  });

  it('shows the same fallback when the Login screen fails to load', async () => {
    me = { status: 401, body: { error: 'unauthorized' } };
    at('/');
    render(<App />);
    // The boot spinner is a main too; wait for the page before taking the landmark.
    await screen.findByRole('heading', { name: 'Could not load this page' });
    const main = screen.getByRole('main');
    expect(within(main).getByRole('heading', { name: 'Could not load this page' })).toBeTruthy();
    expect(within(main).getByRole('button', { name: 'Reload' })).toBeTruthy();
  });
});
