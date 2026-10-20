// The connected-servers list (GET /api/peers), polled while the tab is visible. Peers change slowly
// (status flips, added/removed), so the sidebar and the /s/{slug} routes share this one list.
import { writable, type Readable } from '$lib/store';
import type { PeerView } from '../api/types';

export const PEERS_POLL_INTERVAL_MS = 30_000;

export interface PeersState {
  items: PeerView[];
  loaded: boolean;
}

const state = writable<PeersState>({ items: [], loaded: false });
export const peers: Readable<PeersState> = { subscribe: state.subscribe };

let list: (() => Promise<PeerView[]>) | null = null;
let seq = 0;

export async function refreshPeers(): Promise<void> {
  const fetchList = list;
  if (!fetchList) return;
  const mine = ++seq;
  try {
    const items = await fetchList();
    if (mine === seq) state.set({ items, loaded: true });
  } catch {
    // Keep the last list; the next tick retries.
  }
}

let stopActive: (() => void) | null = null;

/** Fetches now, then every 30 s while the tab is visible; becoming visible fetches at once. */
export function startPeers(deps: { list: () => Promise<PeerView[]>; doc?: Document }): () => void {
  stopActive?.();
  const doc = deps.doc ?? document;
  list = deps.list;
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
    seq++; // an in-flight response must not land after stop
    state.set({ items: [], loaded: false });
  };
  stopActive = stop;
  return stop;
}
