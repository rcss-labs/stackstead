import { act, renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';
import { bootstrapSession, expireSession } from '$lib/api/session';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { useSessionLost } from './useSessionGuard';

let fake: FakeFetch;
afterEach(() => {
  fake?.restore();
  expireSession();
  window.history.replaceState(null, '', '/');
});

/** Boots an authenticated session with the address bar at `url`, then reports a lost session. */
async function loseSessionAt(url: string) {
  fake = installFakeFetch({ 'GET /api/me': { body: { username: 'admin' } } });
  await act(async () => {
    await bootstrapSession();
  });
  window.history.replaceState(null, '', url);
  const wrapper = ({ children }: { children: ReactNode }) => <MemoryRouter>{children}</MemoryRouter>;
  const { result } = renderHook(() => ({ lost: useSessionLost(), here: useLocation() }), { wrapper });
  act(() => result.current.lost());
  return result.current.here.pathname + result.current.here.search;
}

describe('useSessionLost', () => {
  it('carries the page the user was on as next', async () => {
    expect(await loseSessionAt('/settings')).toBe('/login?next=%2Fsettings');
  });

  it.each(['/', '/login', '/login?next=%2Fsettings'])('carries no next from %s', async (url) => {
    expect(await loseSessionAt(url)).toBe('/login');
  });

  it('never carries /setup as next', async () => {
    expect(await loseSessionAt('/setup')).toBe('/login');
  });
});
