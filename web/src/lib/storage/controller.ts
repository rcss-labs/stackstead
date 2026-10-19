import { writable, type Readable } from 'svelte/store';
import type { LiveChange } from '../stores/live';
import { debounce } from '../stores/debounce';

/** Storage only polls while a disk-usage producer is refreshing. Events still refresh inventory. */
export function createStorageRefresh<T>(deps: {
  load: () => Promise<T>; refreshing: (data: T) => boolean;
  changes: Readable<LiveChange | null>; operations: Readable<number>; doc?: Document;
}) {
  const doc = deps.doc ?? document;
  const data = writable<T | undefined>();
  const error = writable<string | undefined>();
  let started = false;
  let generation = 0;
  let busy = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let unsubs: (() => void)[] = [];
  let pending = false;
  const visible = () => started && doc.visibilityState !== 'hidden';
  function schedule() {
    clearTimeout(timer); timer = undefined;
    if (pending && visible()) timer = setTimeout(() => void load(true), 10_000);
  }
  async function load(interval = false) {
    if (!visible() || (interval && busy)) return;
    clearTimeout(timer); timer = undefined;
    const mine = ++generation; busy = true;
    try {
      const fresh = await deps.load();
      if (mine !== generation || !visible()) return;
      pending = deps.refreshing(fresh); data.set(fresh); error.set(undefined);
    } catch (err) {
      if (mine !== generation || !visible()) return;
      error.set(err instanceof Error ? err.message : String(err));
    } finally {
      if (mine === generation) { busy = false; schedule(); }
    }
  }
  const changed = debounce(() => void load());
  function invalidate() { generation++; busy = false; clearTimeout(timer); timer = undefined; changed.cancel(); }
  function visibility() { invalidate(); if (visible()) void load(); }
  return {
    data: { subscribe: data.subscribe } as Readable<T | undefined>,
    error: { subscribe: error.subscribe } as Readable<string | undefined>,
    refresh() { if (visible()) void load(); },
    start() {
      if (started) return; started = true;
      doc.addEventListener('visibilitychange', visibility);
      if (visible()) void load();
      let firstChange = true; let firstOperation = true;
      unsubs = [deps.changes.subscribe(c => {
        if (firstChange) { firstChange = false; return; }
        if (!c || !visible()) return;
        if (c.resync) changed.flush(); else changed.trigger();
      }), deps.operations.subscribe(() => {
        if (firstOperation) { firstOperation = false; return; }
        if (visible()) changed.trigger();
      })];
    },
    stop() { if (!started) return; started = false; invalidate(); unsubs.forEach(unsub => unsub()); unsubs = []; doc.removeEventListener('visibilitychange', visibility); },
  };
}
