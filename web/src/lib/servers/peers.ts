// The connected-servers list (GET /api/peers), polled while the tab is visible. Peers change slowly
// (status flips, added/removed), so the sidebar and the /s/{slug} routes share this one list.
import { writable, type Readable } from '$lib/store';
import type { PeerView, ServerSelf } from '../api/types';

export const PEERS_POLL_INTERVAL_MS = 30_000;

export interface PeersState {
  items: PeerView[];
  loaded: boolean;
}

const state = writable<PeersState>({ items: [], loaded: false });
export const peers: Readable<PeersState> = { subscribe: state.subscribe };

/** Peers the rest of the app treats as existing: a pairing still in progress is not one yet. */
export function connectedPeers(items: PeerView[]): PeerView[] {
  return items.filter((p) => p.status !== 'pending');
}

const selfNameState = writable<string | undefined>(undefined);
/** This server's own name; undefined until it loads (or if it cannot). */
export const selfName: Readable<string | undefined> = { subscribe: selfNameState.subscribe };

/** Pushes a name the user just saved, so the sidebar follows without a reload. */
export function setSelfName(name: string): void {
  if (name) selfNameState.set(name);
}

let list: (() => Promise<PeerView[]>) | null = null;
let loadSelf: (() => Promise<ServerSelf>) | null = null;
let selfRequested = false;
let seq = 0;

/** Asks for this server's name once, the first time a connected server exists. */
function ensureSelfName(items: PeerView[]): void {
  if (selfRequested || !loadSelf || connectedPeers(items).length === 0) return;
  selfRequested = true;
  const fetchSelf = loadSelf;
  fetchSelf()
    .then((self) => {
      if (loadSelf === fetchSelf && self.name) selfNameState.set(self.name);
    })
    .catch(() => {
      if (loadSelf === fetchSelf) selfRequested = false; // retry on a later poll; the heading falls back meanwhile
    });
}

export async function refreshPeers(): Promise<void> {
  const fetchList = list;
  if (!fetchList) return;
  const mine = ++seq;
  try {
    const items = await fetchList();
    if (mine === seq) {
      state.set({ items, loaded: true });
      ensureSelfName(items);
    }
  } catch {
    // Keep the last list; the next tick retries.
  }
}

let stopActive: (() => void) | null = null;

/** Fetches now, then every 30 s while the tab is visible; becoming visible fetches at once. */
export function startPeers(deps: { list: () => Promise<PeerView[]>; self?: () => Promise<ServerSelf>; doc?: Document }): () => void {
  stopActive?.();
  const doc = deps.doc ?? document;
  list = deps.list;
  loadSelf = deps.self ?? null;
  selfRequested = false;
  let timer: ReturnType<typeof setInterval> | undefined;

  const pause = () => {
    clearInterval(timer);
    timer = undefined;
  };
  const resume = () => {
    pause();
    void refreshPeers();
    timer = setInterval(() => void refreshPeers(), PEERS_POLL_INTERVAL_MS);
  };
  const onVisibility = () => (doc.visibilityState === 'hidden' ? pause() : resume());

  doc.addEventListener('visibilitychange', onVisibility);
  if (doc.visibilityState !== 'hidden') resume();

  const stop = () => {
    if (stopActive !== stop) return;
    stopActive = null;
    pause();
    doc.removeEventListener('visibilitychange', onVisibility);
    list = null;
    loadSelf = null;
    selfNameState.set(undefined);
    seq++; // an in-flight response must not land after stop
    state.set({ items: [], loaded: false });
  };
  stopActive = stop;
  return stop;
}
