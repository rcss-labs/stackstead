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

export async function logout(): Promise<void> {
  try {
    await api.logout();
  } catch {
    // The cookie may already be gone; the browser is logged out either way.
  }
  store.set({ state: 'anonymous' });
}

export async function setup(username: string, password: string, token?: string): Promise<void> {
  await api.setup(username, password, token);
  await login(username, password);
}
