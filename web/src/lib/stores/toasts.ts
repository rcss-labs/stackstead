import { writable } from 'svelte/store';

export type ToastKind = 'info' | 'success' | 'warning' | 'danger';

export interface ToastInput {
  kind: ToastKind;
  message: string;
  /** Milliseconds before auto-dismiss; 0 keeps the toast until dismissed. */
  timeoutMs?: number;
}

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

const DEFAULT_TIMEOUT_MS = 5000;

function createToasts() {
  const { subscribe, update, set } = writable<Toast[]>([]);
  const timers = new Map<number, ReturnType<typeof setTimeout>>();
  let nextId = 1;

  function dismiss(id: number): void {
    const timer = timers.get(id);
    if (timer !== undefined) clearTimeout(timer);
    timers.delete(id);
    update((list) => list.filter((t) => t.id !== id));
  }

  function push(input: ToastInput): number {
    const id = nextId++;
    update((list) => [...list, { id, kind: input.kind, message: input.message }]);
    const timeout = input.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    if (timeout > 0) timers.set(id, setTimeout(() => dismiss(id), timeout));
    return id;
  }

  /** Drops every toast and pending timer (used by tests and on logout). */
  function reset(): void {
    for (const timer of timers.values()) clearTimeout(timer);
    timers.clear();
    set([]);
  }

  return { subscribe, push, dismiss, reset };
}

export const toasts = createToasts();
