import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../vitest-setup';
import { onUnauthorized } from '../lib/api/client';
import { session } from '../lib/api/session';
import { navigate, route } from '../lib/router';
import Login, { safeNext } from './Login.svelte';

let fake: FakeFetch;
beforeEach(() => onUnauthorized(() => {}));
afterEach(() => fake?.restore());

function fill(user: string, pass: string) {
  return (async () => {
    const u = userEvent.setup();
    await u.type(screen.getByLabelText('Username'), user);
    await u.type(screen.getByLabelText('Password'), pass);
    return u;
  })();
}

describe('safeNext', () => {
  it('keeps same-origin paths', () => {
    expect(safeNext('/stacks/web')).toBe('/stacks/web');
    expect(safeNext('/stacks?x=1#a')).toBe('/stacks?x=1#a');
    expect(safeNext('/%2f%2fevil')).toBe('/%2f%2fevil'); // still a path on this origin
  });
  it.each([
    'https://evil',
    '//evil',
    '/\\evil',
    '\\\\evil',
    'javascript:alert(1)',
    'stacks',
    '/\t/evil',
    '/a\\b',
    '/\n/evil',
    '/.//evil',
    '/a/..//evil',
    'JAVASCRIPT:alert(1)',
    'data:text/html,x',
    '',
    null,
  ])('rejects %j', (v) => {
    expect(safeNext(v)).toBe('/');
  });
});

describe('Login', () => {
  it('submits typed values, logs in and navigates to next', async () => {
    fake = installFakeFetch({
      'POST /api/login': { body: {} },
      'GET /api/me': { body: { username: 'ann' } },
    });
    navigate('/login?next=/stacks/web');
    render(Login);
    const u = await fill('ann', 'secret-pass');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(get(route).path).toBe('/stacks/web'));
    expect(fake.calls.find((c) => c.path === '/api/login')?.body).toEqual({ username: 'ann', password: 'secret-pass' });
    expect(get(session).state).toBe('authenticated');
  });

  it('Enter submits', async () => {
    fake = installFakeFetch({
      'POST /api/login': { body: {} },
      'GET /api/me': { body: { username: 'ann' } },
    });
    navigate('/login');
    render(Login);
    const u = await fill('ann', 'pw');
    await u.keyboard('{Enter}');
    await waitFor(() => expect(get(route).path).toBe('/'));
  });

  it('rejects an external next', async () => {
    fake = installFakeFetch({
      'POST /api/login': { body: {} },
      'GET /api/me': { body: { username: 'ann' } },
    });
    navigate('/login?next=//evil');
    render(Login);
    const u = await fill('ann', 'pw');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(get(route).path).toBe('/'));
  });

  it('401 shows an inline alert and keeps the username', async () => {
    fake = installFakeFetch({ 'POST /api/login': { status: 401, body: { error: 'nope' } } });
    navigate('/login');
    render(Login);
    const u = await fill('ann', 'bad');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    expect((await screen.findByRole('alert')).textContent).toContain('invalid username or password');
    expect((screen.getByLabelText('Username') as HTMLInputElement).value).toBe('ann');
  });

  it('429 shows the server message', async () => {
    fake = installFakeFetch({ 'POST /api/login': { status: 429, body: { error: 'too many attempts, try later' } } });
    navigate('/login');
    render(Login);
    const u = await fill('ann', 'bad');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    expect((await screen.findByRole('alert')).textContent).toContain('too many attempts, try later');
  });

  it('network error shows a generic message', async () => {
    fake = installFakeFetch({});
    globalThis.fetch = (() => Promise.reject(new TypeError('down'))) as typeof fetch;
    navigate('/login');
    render(Login);
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
    navigate('/login');
    render(Login);
    expect(document.title).toContain('Sign in');
    const u = await fill('ann', 'pw');
    await u.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(screen.getByRole('button', { name: 'Sign in' }).getAttribute('aria-disabled')).toBe('true');
    release();
    await screen.findByRole('alert');
  });
});
