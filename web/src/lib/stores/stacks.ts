// The stack list, kept current from the live `changes` signal. The server
// publishes one change per project, but the list endpoint is all-or-nothing,
// so every relevant change (known project, or an unknown one = new stack)
// refetches the whole list. Bursts are debounced; a resync (reconnect after
// missed events) refetches immediately; responses that arrive out of order are
// dropped by sequence number.
import { writable, type Readable } from '$lib/store';
import type { Change, StackSummary } from '../api/types';

export interface StacksState {
  items: StackSummary[];
  loading: boolean;
  error?: string;
}

const DEBOUNCE_MS = 300;
const MAX_WAIT_MS = 1000;

/** One independent stack list; the module-level exports below are the local server's instance. */
export function createStacksStore() {
  const state = writable<StacksState>({ items: [], loading: false });
  const stacks: Readable<StacksState> = { subscribe: state.subscribe };

  let list: (() => Promise<StackSummary[]>) | null = null;
  let seq = 0;

  let stopActive: (() => void) | null = null;

  /** Bumped to ask screens to re-check running operations (e.g. when an operation ends). */
  const opsTick = writable(0);
  const operationsRefresh: Readable<number> = { subscribe: opsTick.subscribe };
  function refreshRunningOps(): void {
    opsTick.update((n) => n + 1);
  }

  function startStacks(deps: {
    list: () => Promise<StackSummary[]>;
    changes: Readable<(Change & { resync?: true }) | null>;
  }): () => void {
    stopActive?.(); // idempotent: a second start replaces the first instead of doubling subscriptions
    list = deps.list;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let pendingSince: number | undefined;
    let stopped = false;
    let first = true;

    const clearPending = () => {
      clearTimeout(timer);
      timer = undefined;
      pendingSince = undefined;
    };

    state.update((s) => ({ ...s, loading: s.items.length === 0 }));
    void refreshStacks();

    const unsubscribe = deps.changes.subscribe((c) => {
      // Svelte delivers the current value on subscribe; the initial fetch above covers it.
      if (first) {
        first = false;
        return;
      }
      if (stopped || !c) return;
      if (c.resync) {
        clearPending();
        void refreshStacks();
        return;
      }
      // Debounce, but never wait longer than MAX_WAIT_MS in total so sustained activity still updates the list.
      const now = Date.now();
      pendingSince ??= now;
      clearTimeout(timer);
      timer = setTimeout(
        () => {
          clearPending();
          void refreshStacks();
        },
        Math.max(0, Math.min(DEBOUNCE_MS, pendingSince + MAX_WAIT_MS - now)),
      );
    });

    const stop = () => {
      if (stopped) return;
      stopped = true;
      clearPending();
      unsubscribe();
      if (stopActive === stop) {
        stopActive = null;
        list = null;
        seq++; // a response still in flight must not land after stop
        state.set({ items: [], loading: false }); // never show another session's stacks
      }
    };
    stopActive = stop;
    return stop;
  }

  async function refreshStacks(): Promise<void> {
    const fetchList = list;
    if (!fetchList) return;
    const mine = ++seq;
    try {
      const items = await fetchList();
      if (mine !== seq) return;
      state.set({ items, loading: false });
    } catch (e) {
      if (mine !== seq) return;
      state.update((s) => ({ ...s, loading: false, error: e instanceof Error ? e.message : String(e) }));
    }
  }

  return { stacks, operationsRefresh, refreshRunningOps, startStacks, refreshStacks };
}

const local = createStacksStore();
export const { stacks, operationsRefresh, refreshRunningOps, startStacks, refreshStacks } = local;
