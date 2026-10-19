// Polls GET /api/host for the shell's host block. Same visibility/interval rules as the
// dashboard controller: nothing runs while the tab is hidden, and becoming visible fetches at once.
import { writable, type Readable } from '$lib/store';
import type { HostSnapshot } from '../api/types';

const hostStore = writable<HostSnapshot | undefined>(undefined);
export const host: Readable<HostSnapshot | undefined> = { subscribe: hostStore.subscribe };

/** One point of the host history (sparklines); null when the snapshot section is null. */
export interface HostSample {
  t: number;
  cpu: number | null;
  mem: number | null;
  rx: number | null;
}

/** Five minutes at the 10 s poll interval. */
export const HOST_HISTORY_MAX = 30;

const historyStore = writable<HostSample[]>([]);
export const hostHistory: Readable<HostSample[]> = { subscribe: historyStore.subscribe };

function toSample(snapshot: HostSnapshot): HostSample {
  return {
    t: Date.parse(snapshot.sampledAt),
    cpu: snapshot.cpu?.percent ?? null,
    mem: snapshot.memory?.used ?? null,
    rx: snapshot.network?.rxBytesPerSec ?? null,
  };
}

function appendHistory(snapshot: HostSnapshot) {
  const sample = toSample(snapshot);
  historyStore.update((history) => {
    // The server may serve the same sample twice (poll faster than its own sampling).
    if (history.length > 0 && history[history.length - 1].t === sample.t) return history;
    return [...history, sample].slice(-HOST_HISTORY_MAX);
  });
}

export interface HostDeps {
  host: () => Promise<HostSnapshot>;
  doc?: Document;
  intervalMs?: number;
}

const DEFAULT_INTERVAL_MS = 10_000;

let teardown: (() => void) | undefined;
let holders = 0;

/**
 * Starts the shared host poller and returns this caller's own idempotent release function.
 * The first call starts polling with its deps; later calls only add a holder (their deps are
 * ignored). Timers, the visibility listener and the value are torn down when the last holder releases.
 */
export function startHost(deps: HostDeps): () => void {
  if (!teardown) teardown = beginPolling(deps);
  holders++;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    if (--holders === 0) {
      teardown?.();
      teardown = undefined;
    }
  };
}

function beginPolling(deps: HostDeps): () => void {
  const doc = deps.doc ?? document;
  const intervalMs = deps.intervalMs ?? DEFAULT_INTERVAL_MS;
  let timer: ReturnType<typeof setInterval> | undefined;
  let generation = 0;
  let busy = false;

  // The generation drops responses that arrive after stop/hide or after a newer fetch started;
  // interval ticks skip while a fetch is in flight so a slow endpoint still lands.
  async function load(interval = false) {
    if (interval && busy) return;
    const mine = ++generation;
    busy = true;
    try {
      const value = await deps.host();
      if (mine === generation) {
        hostStore.set(value);
        appendHistory(value);
      }
    } catch {
      // A failed fetch keeps the previous value; the next tick retries.
    } finally {
      if (mine === generation) busy = false;
    }
  }

  function pause() {
    generation++;
    busy = false;
    if (timer !== undefined) clearInterval(timer);
    timer = undefined;
  }

  function resume() {
    pause();
    void load();
    timer = setInterval(() => void load(true), intervalMs);
  }

  function onVisibility() {
    if (doc.visibilityState === 'hidden') pause();
    else resume();
  }

  doc.addEventListener('visibilitychange', onVisibility);
  if (doc.visibilityState !== 'hidden') resume();

  return () => {
    pause();
    doc.removeEventListener('visibilitychange', onVisibility);
    hostStore.set(undefined);
    historyStore.set([]);
  };
}
