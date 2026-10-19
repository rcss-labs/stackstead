import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { get } from '$lib/store';
import { installFakeFetch, type FakeFetch } from '../vitest-setup';
import { onUnauthorized } from '../lib/api/client';
import { bootstrapSession, expireSession, session } from '../lib/api/session';
import { navigate, route } from '../lib/router';
import { toasts } from '../lib/stores/toasts';
import Setup from './Setup.svelte';

let fake: FakeFetch;
beforeEach(() => onUnauthorized(() => {}));
afterEach(() => fake?.restore());

const PW = 'correct-horse-battery';

async function fillForm(opts: { confirm?: string; token?: string; pw?: string } = {}) {
  const u = userEvent.setup();
  await u.type(screen.getByLabelText('Username'), 'admin');
  await u.type(screen.getByLabelText('Password'), opts.pw ?? PW);
  await u.type(screen.getByLabelText('Confirm password'), opts.confirm ?? PW);
  if (opts.token) await u.type(screen.getByLabelText(/Setup token/), opts.token);
  return u;
}
const submit = (u: ReturnType<typeof userEvent.setup>) => u.click(screen.getByRole('button', { name: 'Create account' }));

async function needsSetup() {
  fake = installFakeFetch({
    'GET /api/me': { status: 401, body: { error: 'x' } },
    'GET /api/setup/status': { body: { needsSetup: true } },
    'POST /api/setup': { body: {} },
    'POST /api/login': { body: {} },
  });
  await bootstrapSession();
  navigate('/setup');
}

describe('Setup', () => {
  it('shows the length rule and token hint', async () => {
    await needsSetup();
    render(Setup);
    expect(screen.getByText(/at least 12 characters/i)).toBeTruthy();
    expect(screen.getByText(/unless you open this page from the machine itself/i)).toBeTruthy();
    expect(document.title).toContain('Set up');
  });

  it('mismatch blocks submit with an inline error', async () => {
    await needsSetup();
    render(Setup);
    const u = await fillForm({ confirm: 'something-else-entirely' });
    await submit(u);
    expect(screen.getByText(/passwords do not match/i)).toBeTruthy();
    expect(fake.calls.some((c) => c.path === '/api/setup')).toBe(false);
  });

  it('short password blocks submit', async () => {
    await needsSetup();
    render(Setup);
    const u = await fillForm({ pw: 'short', confirm: 'short' });
    await submit(u);
    expect(screen.getByText('Password must be at least 12 characters.')).toBeTruthy();
    expect(fake.calls.some((c) => c.path === '/api/setup')).toBe(false);
  });

  it('403 explains the token requirement', async () => {
    await needsSetup();
    fake.restore();
    fake = installFakeFetch({ 'POST /api/setup': { status: 403, body: { error: 'forbidden' } } });
    render(Setup);
    const u = await fillForm();
    await submit(u);
    expect((await screen.findByRole('alert')).textContent).toMatch(/setup token/i);
  });

  it('400 shows the server message', async () => {
    await needsSetup();
    fake.restore();
    fake = installFakeFetch({ 'POST /api/setup': { status: 400, body: { error: 'username is reserved' } } });
    render(Setup);
    const u = await fillForm();
    await submit(u);
    expect((await screen.findByRole('alert')).textContent).toContain('username is reserved');
  });

  it('success sends the token, logs in and navigates home', async () => {
    await needsSetup();
    fake.restore();
    fake = installFakeFetch({
      'POST /api/setup': { body: {} },
      'POST /api/login': { body: {} },
      'GET /api/me': { body: { username: 'admin' } },
    });
    render(Setup);
    const u = await fillForm({ token: 'tok-123' });
    await submit(u);
    await waitFor(() => expect(get(route).path).toBe('/'));
    expect(fake.calls.find((c) => c.path === '/api/setup')?.body).toMatchObject({ username: 'admin', password: PW });
    expect(JSON.stringify(fake.calls.find((c) => c.path === '/api/setup')?.body)).toContain('tok-123');
    expect(get(session).state).toBe('authenticated');
  });

  it('routes to /login when setup was already completed', async () => {
    await needsSetup();
    render(Setup);
    expireSession();
    await waitFor(() => expect(get(route).path).toBe('/login'));
  });

  it('409 means setup is already complete: toast and go to /login', async () => {
    await needsSetup();
    fake.restore();
    toasts.reset();
    fake = installFakeFetch({ 'POST /api/setup': { status: 409, body: { error: 'already set up' } } });
    render(Setup);
    const u = await fillForm();
    await submit(u);
    await waitFor(() => expect(get(route).path).toBe('/login'));
    expect(get(toasts).some((t) => /already complete/i.test(t.message))).toBe(true);
    expect(get(session).state).toBe('anonymous');
  });

  it('setup ok but login fails: toast and go to /login', async () => {
    await needsSetup();
    fake.restore();
    toasts.reset();
    fake = installFakeFetch({
      'POST /api/setup': { body: {} },
      'POST /api/login': { status: 500, body: { error: 'boom' } },
    });
    render(Setup);
    const u = await fillForm();
    await submit(u);
    await waitFor(() => expect(get(route).path).toBe('/login'));
    expect(get(toasts).some((t) => /account was created/i.test(t.message))).toBe(true);
    expect(get(session).state).toBe('anonymous');
  });

  it('focuses the first invalid field on client-side failure', async () => {
    await needsSetup();
    render(Setup);
    let u = await fillForm({ pw: 'short', confirm: 'short' });
    await submit(u);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Password')));
  });

  it('focuses the confirm field on mismatch', async () => {
    await needsSetup();
    render(Setup);
    const u = await fillForm({ confirm: 'something-else-entirely' });
    await submit(u);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Confirm password')));
  });

  it('counts password length in characters, not UTF-16 units', async () => {
    await needsSetup();
    render(Setup);
    const u = await fillForm({ pw: '😀'.repeat(6), confirm: '😀'.repeat(6) });
    await submit(u);
    expect(screen.getByText('Password must be at least 12 characters.')).toBeTruthy();
  });
});
