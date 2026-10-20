// The combined stack list across servers (GET /api/fleet/stacks), polled while the All stacks screen is
// mounted and the tab is visible. The server bounds each peer at 3 s, so one slow peer never stalls a poll.
import { writable, type Readable } from '$lib/store';
import type { FleetStacks } from '../api/types';

export const FLEET_POLL_INTERVAL_MS = 5_000;

export interface FleetState {
  /** The last successful answer; kept across failed polls so the screen does not blank. */
  data: FleetStacks | undefined;
  loading: boolean;
  error: string;
}

export interface Fleet {
  state: Readable<FleetState>;
  start(): void;
  stop(): void;
  refresh(): Promise<void>;
}

export function createFleet(deps: { load: () => Promise<FleetStacks>; doc?: Document }): Fleet {
  const doc = deps.doc ?? document;
  const state = writable<FleetState>({ data: undefined, loading: true, error: '' });
  let timer: ReturnType<typeof setInterval> | undefined;
  let running = false;
  let seq = 0;

  async function refresh(): Promise<void> {
    const mine = ++seq;
    try {
      const data = await deps.load();
      if (mine === seq) state.set({ data, loading: false, error: '' });
    } catch (e) {
      if (mine === seq) state.update((s) => ({ ...s, loading: false, error: e instanceof Error ? e.message : String(e) }));
    }
  }

  const pause = () => {
    clearInterval(timer);
    timer = undefined;
  };
  const resume = () => {
    pause();
    void refresh();
    timer = setInterval(() => void refresh(), FLEET_POLL_INTERVAL_MS);
  };
  const onVisibility = () => (doc.visibilityState === 'hidden' ? pause() : resume());

  return {
    state: { subscribe: state.subscribe },
    start() {
      if (running) return;
      running = true;
      doc.addEventListener('visibilitychange', onVisibility);
      if (doc.visibilityState !== 'hidden') resume();
    },
    stop() {
      if (!running) return;
      running = false;
      pause();
      doc.removeEventListener('visibilitychange', onVisibility);
      seq++; // an in-flight response must not land after stop
    },
    refresh,
  };
}

/** Maps an app path into a server's URL space; a null slug is this server. */
export function serverPath(slug: string | null, path: string): string {
  return slug === null ? path : `/s/${encodeURIComponent(slug)}${path}`;
}
