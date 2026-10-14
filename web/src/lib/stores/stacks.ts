// The stack list, kept current from the live `changes` signal. The server
// publishes one change per project, but the list endpoint is all-or-nothing,
// so every relevant change (known project, or an unknown one = new stack)
// refetches the whole list. Bursts are debounced; a resync (reconnect after
// missed events) refetches immediately; responses that arrive out of order are
// dropped by sequence number.
import { writable, type Readable } from 'svelte/store';
import type { Change, StackSummary } from '../api/types';

export interface StacksState {
  items: StackSummary[];
  loading: boolean;
  error?: string;
}

const DEBOUNCE_MS = 300;

const state = writable<StacksState>({ items: [], loading: false });
export const stacks: Readable<StacksState> = { subscribe: state.subscribe };

let list: (() => Promise<StackSummary[]>) | null = null;
let seq = 0;

export function startStacks(deps: {
  list: () => Promise<StackSummary[]>;
  changes: Readable<(Change & { resync?: true }) | null>;
}): () => void {
  list = deps.list;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let stopped = false;
  let first = true;

  state.update((s) => ({ ...s, loading: s.items.length === 0 }));
  void refreshStacks();

  const unsubscribe = deps.changes.subscribe((c) => {
    // Svelte delivers the current value on subscribe; the initial fetch above covers it.
    if (first) {
      first = false;
      return;
    }
    if (stopped || !c) return;
    clearTimeout(timer);
    if (c.resync) {
      timer = undefined;
      void refreshStacks();
      return;
    }
    timer = setTimeout(() => {
      timer = undefined;
      void refreshStacks();
    }, DEBOUNCE_MS);
  });

  return () => {
    stopped = true;
    clearTimeout(timer);
    unsubscribe();
    if (list === deps.list) {
      list = null;
      seq++; // a response still in flight must not land after stop
      state.set({ items: [], loading: false }); // never show another session's stacks
    }
  };
}

export async function refreshStacks(): Promise<void> {
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
