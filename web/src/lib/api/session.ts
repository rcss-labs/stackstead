import { writable, type Readable } from 'svelte/store';
import { api, rearmUnauthorized } from './client';

export interface SessionState {
  state: 'unknown' | 'anonymous' | 'needs-setup' | 'authenticated';
  username?: string;
}

const store = writable<SessionState>({ state: 'unknown' });
export const session: Readable<SessionState> = { subscribe: store.subscribe };

/** GET /api/me decides; on 401 the setup status tells first-run from logged-out. */
export async function bootstrapSession(): Promise<void> {
  try {
    const me = await api.me();
    store.set({ state: 'authenticated', username: me.username });
    return;
  } catch (err) {
    if ((err as { status?: number }).status !== 401) {
      store.set({ state: 'anonymous' });
      throw err;
    }
  }
  try {
    const { needsSetup } = await api.setupStatus();
    store.set({ state: needsSetup ? 'needs-setup' : 'anonymous' });
  } catch (err) {
    store.set({ state: 'anonymous' });
    throw err;
  }
}

export async function login(username: string, password: string): Promise<void> {
  await api.login(username, password);
  rearmUnauthorized();
  const me = await api.me();
  store.set({ state: 'authenticated', username: me.username });
}

/** Drops local session state without a request (for the 401 redirect handler). */
export function expireSession(): void {
  store.set({ state: 'anonymous' });
}

/**
 * Always ends anonymous. Resolves {ok:false} when the server may not have
 * ended the session (anything but success or 401), so the UI can warn.
 */
export async function logout(): Promise<{ ok: boolean }> {
  let ok = true;
  try {
    await api.logout();
  } catch (err) {
    ok = (err as { status?: number }).status === 401;
  }
  store.set({ state: 'anonymous' });
  return { ok };
}

export async function setup(username: string, password: string, token?: string): Promise<void> {
  await api.setup(username, password, token);
  await login(username, password);
}
