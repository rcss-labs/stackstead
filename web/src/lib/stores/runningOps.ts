// Running operations, shared by the top bar, the palette and the stack list. An operation ending emits no
// container change, so besides reacting to changes and explicit refresh ticks the store rechecks with
// backoff while anything is running. Bursts of changes are debounced, a resync fetches at once, and
// responses that arrive out of order or after stop are dropped by sequence number.
import { writable, type Readable } from '$lib/store';
import type { Operation } from '../api/types';
import { debounce } from './debounce';
import type { LiveChange } from './live';

const BACKOFF_BASE_MS = 2000;
const BACKOFF_MAX_MS = 10000;

export function runningStackNames(ops: Operation[]): Set<string> {
  return new Set(ops.map((o) => o.stack));
}

/** One independent running-operations list; the module-level exports below are the local server's instance. */
export function createRunningOpsStore() {
  const state = writable<Operation[]>([]);
  const runningOps: Readable<Operation[]> = { subscribe: state.subscribe };

  let stopActive: (() => void) | null = null;

  function startRunningOps(deps: {
    list: () => Promise<Operation[]>;
    changes: Readable<LiveChange | null>;
    refresh: Readable<number>;
    doc?: Document;
  }): () => void {
    const doc = deps.doc ?? document;
    const hidden = () => doc.visibilityState === 'hidden';
    stopActive?.(); // idempotent: a second start replaces the first instead of doubling subscriptions
    let seq = 0;
    let stopped = false;
    let attempt = 0;
    let hadRunning = false;
    let recheckTimer: ReturnType<typeof setTimeout> | undefined;

    const scheduleRecheck = () => {
      if (hidden()) return; // becoming visible fetches again and resumes the backoff
      recheckTimer = setTimeout(() => void fetchOps(), Math.min(BACKOFF_BASE_MS * 2 ** attempt, BACKOFF_MAX_MS));
      attempt++;
    };

    async function fetchOps(): Promise<void> {
      clearTimeout(recheckTimer);
      const mine = ++seq;
      try {
        const ops = await deps.list();
        if (mine !== seq) return;
        state.set(ops);
        hadRunning = ops.length > 0;
        if (hadRunning) scheduleRecheck();
      } catch {
        // Keep the last value; keep trying only if we were tracking a running operation.
        if (mine === seq && hadRunning) scheduleRecheck();
      }
    }

    /** A change or an explicit refresh starts over with the shortest backoff. */
    const fetchFresh = () => {
      if (stopped || hidden()) return; // nothing polls after stop or while the tab is hidden
      attempt = 0;
      void fetchOps();
    };

    const debounced = debounce(fetchFresh);
    fetchFresh();

    let firstChange = true;
    const unsubscribeChanges = deps.changes.subscribe((c) => {
      // The current value is replayed on subscribe; the initial fetch covers it.
      if (firstChange) {
        firstChange = false;
        return;
      }
      if (stopped || !c) return;
      if (c.resync) debounced.flush();
      else debounced.trigger();
    });

    const onVisibility = () => {
      if (hidden()) {
        clearTimeout(recheckTimer);
        debounced.cancel();
      } else {
        fetchFresh();
      }
    };
    doc.addEventListener('visibilitychange', onVisibility);

    let firstTick = true;
    const unsubscribeRefresh = deps.refresh.subscribe(() => {
      if (firstTick) {
        firstTick = false;
        return;
      }
      if (stopped) return;
      debounced.cancel();
      fetchFresh();
    });

    const stop = () => {
      if (stopped) return;
      stopped = true;
      debounced.cancel();
      clearTimeout(recheckTimer);
      unsubscribeChanges();
      unsubscribeRefresh();
      doc.removeEventListener('visibilitychange', onVisibility);
      seq++; // a response still in flight must not land after stop
      if (stopActive === stop) {
        stopActive = null;
        state.set([]); // never show another session's operations
      }
    };
    stopActive = stop;
    return stop;
  }

  return { runningOps, startRunningOps };
}

const local = createRunningOpsStore();
export const { runningOps, startRunningOps } = local;
