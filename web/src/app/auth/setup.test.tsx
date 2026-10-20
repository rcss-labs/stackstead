import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { get } from '$lib/store';
import { onUnauthorized } from '$lib/api/client';
import { expireSession, session } from '$lib/api/session';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { currentPath, fillForm, mountAuth, needsSetup, PW, submit } from './authTestKit';

let fake: FakeFetch;
beforeEach(() => {
  onUnauthorized(() => {});
  toasts.reset();
});
afterEach(async () => {
  cleanup();
  expireSession();
  fake?.restore();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases subscriptions on the next task
});

/** Boots into needs-setup, then swaps the routes for the ones under test. */
async function setupWith(routes: Parameters<typeof installFakeFetch>[0]) {
  const boot = await needsSetup();
  boot.restore();
  fake = installFakeFetch(routes);
}

describe('Setup', () => {
  it('shows the length rule and token hint', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    expect(screen.getByRole('heading', { level: 1, name: 'Set up Stackstead' })).toBeTruthy();
    expect(screen.getByText(/at least 12 characters/i)).toBeTruthy();
    expect(screen.getByText(/unless you open this page from the machine itself/i)).toBeTruthy();
    expect(document.title).toContain('Set up');
  });

  it('mismatch blocks submit with an inline error', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    const u = await fillForm({ confirm: 'something-else-entirely' });
    await submit(u);
    expect(screen.getByText(/passwords do not match/i)).toBeTruthy();
    expect(fake.calls.some((c) => c.path === '/api/setup')).toBe(false);
  });

  it('short password blocks submit', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    const u = await fillForm({ pw: 'short', confirm: 'short' });
    await submit(u);
    expect(screen.getByText('Password must be at least 12 characters.')).toBeTruthy();
    expect(fake.calls.some((c) => c.path === '/api/setup')).toBe(false);
  });

  it('403 explains the token requirement', async () => {
    await setupWith({ 'POST /api/setup': { status: 403, body: { error: 'forbidden' } } });
    mountAuth('/setup');
    const u = await fillForm();
    await submit(u);
    expect((await screen.findByRole('alert')).textContent).toMatch(/setup token/i);
  });

  it('400 shows the server message', async () => {
    await setupWith({ 'POST /api/setup': { status: 400, body: { error: 'username is reserved' } } });
    mountAuth('/setup');
    const u = await fillForm();
    await submit(u);
    expect((await screen.findByRole('alert')).textContent).toContain('username is reserved');
  });

  it('success sends the token, logs in and navigates home', async () => {
    await setupWith({
      'POST /api/setup': { body: {} },
      'POST /api/login': { body: {} },
      'GET /api/me': { body: { username: 'admin' } },
    });
    mountAuth('/setup');
    const u = await fillForm({ token: 'tok-123' });
    await submit(u);
    await waitFor(() => expect(currentPath()).toBe('/'));
    expect(fake.calls.find((c) => c.path === '/api/setup')?.body).toEqual({ username: 'admin', password: PW, setupToken: 'tok-123' });
    expect(get(session).state).toBe('authenticated');
  });

  it('never puts the typed setup token in the DOM, while typing or after a failed setup', async () => {
    const token = 'setup-secret-9f3a7c';
    await setupWith({ 'POST /api/setup': { status: 403, body: { error: 'forbidden' } } });
    mountAuth('/setup');
    const u = await fillForm({ token });
    expect((screen.getByLabelText(/Setup token/) as HTMLInputElement).value).toBe(token);
    expect(document.body.innerHTML).not.toContain(token);
    expect(document.body.querySelector(`[value*="${token}"]`)).toBeNull();
    await submit(u);
    await screen.findByRole('alert');
    expect(document.body.innerHTML).not.toContain(token);
    expect(document.body.querySelector(`[value*="${token}"]`)).toBeNull();
  });

  it('omits the setup token when the field is blank', async () => {
    await setupWith({
      'POST /api/setup': { body: {} },
      'POST /api/login': { body: {} },
      'GET /api/me': { body: { username: 'admin' } },
    });
    mountAuth('/setup');
    const u = await fillForm();
    await submit(u);
    await waitFor(() => expect(currentPath()).toBe('/'));
    const body = fake.calls.find((c) => c.path === '/api/setup')?.body as Record<string, unknown>;
    expect(body).toMatchObject({ username: 'admin', password: PW });
    expect('setupToken' in body).toBe(false);
  });

  it('routes to /login when setup was already completed', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    act(() => expireSession());
    await waitFor(() => expect(currentPath()).toBe('/login'));
  });

  it('409 means setup is already complete: toast and go to /login', async () => {
    await setupWith({ 'POST /api/setup': { status: 409, body: { error: 'already set up' } } });
    mountAuth('/setup');
    const u = await fillForm();
    await submit(u);
    await waitFor(() => expect(currentPath()).toBe('/login'));
    expect(get(toasts).some((t) => /already complete/i.test(t.message))).toBe(true);
    expect(get(session).state).toBe('anonymous');
  });

  it('setup ok but login fails: toast and go to /login', async () => {
    await setupWith({
      'POST /api/setup': { body: {} },
      'POST /api/login': { status: 500, body: { error: 'boom' } },
    });
    mountAuth('/setup');
    const u = await fillForm();
    await submit(u);
    await waitFor(() => expect(currentPath()).toBe('/login'));
    expect(get(toasts).some((t) => /account was created/i.test(t.message))).toBe(true);
    expect(get(session).state).toBe('anonymous');
  });

  it('focuses the first invalid field on client-side failure', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    const u = await fillForm({ pw: 'short', confirm: 'short' });
    await submit(u);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Password')));
  });

  it('focuses the confirm field on mismatch', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    const u = await fillForm({ confirm: 'something-else-entirely' });
    await submit(u);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Confirm password')));
  });

  it('counts password length in characters, not UTF-16 units', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    const u = await fillForm({ pw: '😀'.repeat(6), confirm: '😀'.repeat(6) });
    await submit(u);
    expect(screen.getByText('Password must be at least 12 characters.')).toBeTruthy();
  });

  it('clears both password fields after a server failure and never renders them', async () => {
    await setupWith({ 'POST /api/setup': { status: 403, body: { error: 'forbidden' } } });
    mountAuth('/setup');
    const u = await fillForm();
    expect(document.body.innerHTML).not.toContain(PW);
    await submit(u);
    await screen.findByRole('alert');
    expect((screen.getByLabelText('Password') as HTMLInputElement).value).toBe('');
    expect((screen.getByLabelText('Confirm password') as HTMLInputElement).value).toBe('');
    expect(document.body.innerHTML).not.toContain(PW);
    expect(screen.getByRole('alert').textContent).not.toContain(PW);
  });

  it('sends one setup request when the same button is clicked twice in one act', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    await setupWith({
      'POST /api/setup': async () => {
        await gate;
        return { body: {} };
      },
      'POST /api/login': { body: {} },
      'GET /api/me': { body: { username: 'admin' } },
    });
    mountAuth('/setup');
    await fillForm();
    const button = screen.getByRole('button', { name: 'Create account' });
    act(() => {
      fireEvent.click(button);
      fireEvent.click(button);
    });
    release();
    await waitFor(() => expect(currentPath()).toBe('/'));
    expect(fake.calls.filter((c) => c.path === '/api/setup')).toHaveLength(1);
  });

  it('the password fields keep new-password autocomplete and the token field is plain text', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    for (const label of ['Password', 'Confirm password']) {
      const input = screen.getByLabelText(label) as HTMLInputElement;
      expect(input.getAttribute('autocomplete')).toBe('new-password');
      expect(input.type).toBe('password');
    }
    const token = screen.getByLabelText(/Setup token/) as HTMLInputElement;
    expect(token.getAttribute('autocomplete')).toBe('off');
    expect(token.getAttribute('spellcheck')).toBe('false');
    expect(token.type).toBe('text');
  });
});
