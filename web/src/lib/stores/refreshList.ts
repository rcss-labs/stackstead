// Shared list refresh rules: changes, visible intervals and stale-response guards.
import { writable, type Readable } from '$lib/store';
import { debounce } from './debounce';
import type { LiveChange } from './live';

const REFRESH_MS = 10_000;

export interface RefreshListDeps<T> {
  list: () => Promise<T[]>;
  changes: Readable<LiveChange | null>;
  key: (row: T) => string;
  doc?: Document;
}

export function createRefreshList<T>(deps: RefreshListDeps<T>) {
  const doc = deps.doc ?? document;
  const rowsStore = writable<T[] | undefined>(undefined);
  const errorStore = writable<string | undefined>(undefined);

  let started = false;
  let timer: ReturnType<typeof setInterval> | undefined;
  let unsubscribeChanges: (() => void) | undefined;

  // Generation guard: a response that arrives after stop, hide, or a newer fetch is dropped.
  // Interval ticks skip while a fetch is in flight so a slow server still gets to answer.
  let generation = 0;
  let busy = false;

  async function load(interval = false) {
    if (interval && busy) return;
    const mine = ++generation;
    busy = true;
    try {
      const rows = await deps.list();
      if (mine !== generation) return;
      rowsStore.set(rows);
      errorStore.set(undefined);
    } catch (err) {
      if (mine !== generation) return;
      errorStore.set(err instanceof Error ? err.message : String(err));
    } finally {
      if (mine === generation) busy = false;
    }
  }

  const onChangeFetch = debounce(() => void load());

  function stopTimers() {
    generation++;
    busy = false;
    clearInterval(timer);
    timer = undefined;
    onChangeFetch.cancel();
  }

  function startTimers() {
    timer = setInterval(() => void load(true), REFRESH_MS);
  }

  function onVisibility() {
    stopTimers();
    if (doc.visibilityState === 'hidden') return;
    void load();
    startTimers();
  }

  return {
    rows: { subscribe: rowsStore.subscribe } as Readable<T[] | undefined>,
    error: { subscribe: errorStore.subscribe } as Readable<string | undefined>,
    /** Apply a row the server just returned (an action's result) and drop any fetch that started before it. */
    patch(fresh: T) {
      generation++;
      busy = false;
      rowsStore.update((rows) => rows?.map((r) => (deps.key(r) === deps.key(fresh) ? fresh : r)));
    },
    /** Fetch now (Retry, or after an action changed a row). */
    refresh() {
      if (started && doc.visibilityState !== 'hidden') void load();
    },
    start() {
      if (started) return;
      started = true;
      doc.addEventListener('visibilitychange', onVisibility);
      if (doc.visibilityState !== 'hidden') {
        void load();
        startTimers();
      }
      // Subscribing replays the current value; skip it so start() fetches once.
      let first = true;
      unsubscribeChanges = deps.changes.subscribe((c) => {
        if (first) {
          first = false;
          return;
        }
        if (!c || doc.visibilityState === 'hidden') return;
        if (c.resync) onChangeFetch.flush();
        else onChangeFetch.trigger();
      });
    },
    stop() {
      if (!started) return;
      started = false;
      stopTimers();
      unsubscribeChanges?.();
      unsubscribeChanges = undefined;
      doc.removeEventListener('visibilitychange', onVisibility);
    },
  };
}
