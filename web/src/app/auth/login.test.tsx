import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { get } from '$lib/store';
import { onUnauthorized } from '$lib/api/client';
import { expireSession, session } from '$lib/api/session';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { fill, location, mountAuth } from './authTestKit';

let fake: FakeFetch;
beforeEach(() => onUnauthorized(() => {}));
afterEach(async () => {
  cleanup();
  expireSession();
  fake?.restore();
  window.localStorage.clear();
  window.sessionStorage.clear();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases subscriptions on the next task
});

const okLogin = () =>
  installFakeFetch({
    'POST /api/login': { body: {} },
    'GET /api/me': { body: { username: 'ann' } },
  });

describe('Login', () => {
  it('shows the host, the localhost note and the brand heading', () => {
    fake = installFakeFetch({});
    mountAuth('/login');
    expect(screen.getByText(window.location.host)).toBeTruthy();
    expect(screen.getByText('Localhost only. For remote access use a TLS reverse proxy.')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'Sign in to Stackstead' })).toBeTruthy();
    const img = document.querySelector('img') as HTMLImageElement;
    expect(img.getAttribute('alt')).toBe('');
    expect(img.getAttribute('src')).toBe('/logo-art.png');
  });

  it('submits typed values, logs in and navigates to next', async () => {
    fake = okLogin();
    mountAuth('/login?next=/stacks/web');
    const u = await fill('ann', 'secret-pass');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(location()).toBe('/stacks/web'));
    expect(fake.calls.find((c) => c.path === '/api/login')?.body).toEqual({ username: 'ann', password: 'secret-pass' });
    expect(get(session).state).toBe('authenticated');
  });

  it('Enter submits', async () => {
    fake = okLogin();
    mountAuth('/login');
    const u = await fill('ann', 'pw');
    await u.keyboard('{Enter}');
    await waitFor(() => expect(location()).toBe('/'));
  });

  it.each(['//evil', 'https://evil.example/x', 'javascript:alert(1)', '/\\evil'])('rejects the external next %j', async (next) => {
    fake = okLogin();
    mountAuth(`/login?next=${encodeURIComponent(next)}`);
    const u = await fill('ann', 'pw');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(location()).toBe('/'));
  });

  it('401 shows an inline alert, keeps the username and drops the password from the DOM', async () => {
    fake = installFakeFetch({ 'POST /api/login': { status: 401, body: { error: 'nope' } } });
    mountAuth('/login');
    const u = await fill('ann', 'bad-pass');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    expect((await screen.findByRole('alert')).textContent).toContain('invalid username or password');
    expect((screen.getByLabelText('Username') as HTMLInputElement).value).toBe('ann');
    expect((screen.getByLabelText('Password') as HTMLInputElement).value).toBe('');
    expect(document.body.innerHTML).not.toContain('bad-pass');
  });

  it('never puts the typed password in the DOM while typing', async () => {
    fake = installFakeFetch({});
    mountAuth('/login');
    await fill('ann', 'typed-secret');
    expect(document.body.innerHTML).not.toContain('typed-secret');
    expect(screen.getByLabelText('Password').hasAttribute('value')).toBe(false);
  });

  it('429 shows the server message', async () => {
    fake = installFakeFetch({ 'POST /api/login': { status: 429, body: { error: 'too many attempts, try later' } } });
    mountAuth('/login');
    const u = await fill('ann', 'bad-pass');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('too many attempts, try later');
    expect(alert.textContent).not.toContain('bad-pass');
  });

  it('network error shows a generic message', async () => {
    fake = installFakeFetch({});
    globalThis.fetch = (() => Promise.reject(new TypeError('down'))) as typeof fetch;
    mountAuth('/login');
    const u = await fill('ann', 'pw');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/could not reach|try again/i);
  });

  it('disables submit while pending and sets the title', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    fake = installFakeFetch({
      'POST /api/login': async () => {
        await gate;
        return { status: 401, body: { error: 'x' } };
      },
    });
    mountAuth('/login');
    expect(document.title).toContain('Sign in');
    const u = await fill('ann', 'pw');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(screen.getByRole('button', { name: 'Sign in' }).getAttribute('aria-disabled')).toBe('true');
    release();
    await screen.findByRole('alert');
  });

  it('sends one login when the same button is clicked twice in one act', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    fake = installFakeFetch({
      'POST /api/login': async () => {
        await gate;
        return { body: {} };
      },
      'GET /api/me': { body: { username: 'ann' } },
    });
    mountAuth('/login');
    await fill('ann', 'pw');
    const button = screen.getByRole('button', { name: 'Sign in' });
    act(() => {
      fireEvent.click(button);
      fireEvent.click(button);
    });
    release();
    await waitFor(() => expect(location()).toBe('/'));
    expect(fake.calls.filter((c) => c.path === '/api/login')).toHaveLength(1);
  });

  it('a successful sign-in stores nothing in the browser', async () => {
    fake = okLogin();
    mountAuth('/login');
    const u = await fill('ann', 'secret-pass');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(location()).toBe('/'));
    for (const store of [{ ...localStorage }, { ...sessionStorage }]) {
      const dump = JSON.stringify(store);
      expect(dump).not.toContain('ann');
      expect(dump).not.toContain('secret-pass');
    }
    const carrying = fake.calls.filter((c) => JSON.stringify(c.body ?? '').includes('secret-pass') || c.path.includes('secret-pass'));
    expect(carrying.map((c) => `${c.method} ${c.path}`)).toEqual(['POST /api/login']);
    expect(location()).not.toContain('secret-pass');
  });

  it('the password field keeps its autocomplete and type', () => {
    fake = installFakeFetch({});
    mountAuth('/login');
    const pw = screen.getByLabelText('Password') as HTMLInputElement;
    expect(pw.getAttribute('autocomplete')).toBe('current-password');
    expect(pw.type).toBe('password');
    expect(screen.getByLabelText('Username').getAttribute('autocomplete')).toBe('username');
  });
});
