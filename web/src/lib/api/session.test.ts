import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { api, onUnauthorized } from './client';
import { bootstrapSession, login, logout, session, setup } from './session';

let fake: FakeFetch;
afterEach(() => fake?.restore());
beforeEach(() => onUnauthorized(() => {}));

describe('bootstrapSession', () => {
  it('is authenticated when /api/me answers', async () => {
    fake = installFakeFetch({ 'GET /api/me': { body: { username: 'ann' } } });
    await bootstrapSession();
    expect(get(session)).toEqual({ state: 'authenticated', username: 'ann' });
  });

  it('401 + needsSetup -> needs-setup', async () => {
    fake = installFakeFetch({
      'GET /api/me': { status: 401, body: { error: 'x' } },
      'GET /api/setup/status': { body: { needsSetup: true } },
    });
    await bootstrapSession();
    expect(get(session).state).toBe('needs-setup');
  });

  it('401 + not needsSetup -> anonymous', async () => {
    fake = installFakeFetch({
      'GET /api/me': { status: 401, body: { error: 'x' } },
      'GET /api/setup/status': { body: { needsSetup: false } },
    });
    await bootstrapSession();
    expect(get(session).state).toBe('anonymous');
  });

  it('network error -> anonymous and the error is surfaced', async () => {
    fake = installFakeFetch({});
    globalThis.fetch = (() => Promise.reject(new TypeError('down'))) as typeof fetch;
    await expect(bootstrapSession()).rejects.toMatchObject({ status: 0 });
    expect(get(session).state).toBe('anonymous');
  });
});

describe('login/logout/setup', () => {
  it('login authenticates with the username from /api/me', async () => {
    fake = installFakeFetch({
      'POST /api/login': { status: 204 },
      'GET /api/me': { body: { username: 'ann' } },
    });
    await login('ann', 'pw');
    expect(get(session)).toEqual({ state: 'authenticated', username: 'ann' });
  });

  it('failed login rejects and leaves state alone', async () => {
    fake = installFakeFetch({
      'POST /api/logout': { status: 204 },
      'POST /api/login': { status: 401, body: { error: 'bad credentials' } },
    });
    await logout();
    await expect(login('ann', 'no')).rejects.toThrow('bad credentials');
    expect(get(session).state).toBe('anonymous');
  });

  it('logout clears to anonymous even when the request fails', async () => {
    fake = installFakeFetch({
      'POST /api/login': { status: 204 },
      'GET /api/me': { body: { username: 'ann' } },
      'POST /api/logout': { status: 401, body: { error: 'x' } },
    });
    await login('ann', 'pw');
    await logout();
    expect(get(session).state).toBe('anonymous');
  });

  it('setup then logs in automatically', async () => {
    fake = installFakeFetch({
      'POST /api/setup': { status: 204 },
      'POST /api/login': { status: 204 },
      'GET /api/me': { body: { username: 'root' } },
    });
    await setup('root', 'pw', 'tok');
    expect(fake.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'POST /api/setup',
      'POST /api/login',
      'GET /api/me',
    ]);
    expect(fake.calls[0].body).toEqual({ username: 'root', password: 'pw', setupToken: 'tok' });
    expect(get(session).state).toBe('authenticated');
  });

  it('a successful login re-arms the 401 redirect', async () => {
    const handler = vi.fn();
    onUnauthorized(handler);
    fake = installFakeFetch({
      'GET /api/stacks': { status: 401, body: { error: 'x' } },
      'POST /api/login': { status: 204 },
      'GET /api/me': { body: { username: 'ann' } },
    });
    await api.stacks().catch(() => {});
    await api.stacks().catch(() => {});
    expect(handler).toHaveBeenCalledTimes(1);
    await login('ann', 'pw');
    await api.stacks().catch(() => {});
    expect(handler).toHaveBeenCalledTimes(2);
  });
});
