import { writable } from '$lib/store';

export type ToastKind = 'info' | 'success' | 'warning' | 'danger';

export interface ToastInput {
  kind: ToastKind;
  message: string;
  /** Milliseconds before auto-dismiss; 0 keeps the toast until dismissed. Default 5000, danger never expires. */
  timeoutMs?: number;
  /** Shows a spinner; implies timeoutMs 0 unless given. */
  pending?: boolean;
  link?: { label: string; href: string };
}

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
  pending?: boolean;
  link?: { label: string; href: string };
}

const DEFAULT_TIMEOUT_MS = 5000;

function createToasts() {
  const { subscribe, update: updateList, set } = writable<Toast[]>([]);
  const timers = new Map<number, ReturnType<typeof setTimeout>>();
  let nextId = 1;

  function dismiss(id: number): void {
    const timer = timers.get(id);
    if (timer !== undefined) clearTimeout(timer);
    timers.delete(id);
    updateList((list) => list.filter((t) => t.id !== id));
  }

  // Errors and pending toasts stay until dismissed or updated: they auto-vanish too fast to read or act on.
  function arm(id: number, kind: ToastKind, pending: boolean | undefined, timeoutMs: number | undefined): void {
    const previous = timers.get(id);
    if (previous !== undefined) clearTimeout(previous);
    timers.delete(id);
    const timeout = timeoutMs ?? (kind === 'danger' || pending ? 0 : DEFAULT_TIMEOUT_MS);
    if (timeout > 0) timers.set(id, setTimeout(() => dismiss(id), timeout));
  }

  function push(input: ToastInput): number {
    const id = nextId++;
    const toast: Toast = { id, kind: input.kind, message: input.message };
    if (input.pending) toast.pending = true;
    if (input.link) toast.link = input.link;
    updateList((list) => [...list, toast]);
    arm(id, input.kind, input.pending, input.timeoutMs);
    return id;
  }

  /** Patches a toast and re-arms its timer from the patched kind/timeoutMs. Unknown ids are ignored. */
  function update(id: number, patch: Partial<ToastInput>): void {
    let patched: Toast | undefined;
    updateList((list) =>
      list.map((t) => {
        if (t.id !== id) return t;
        patched = { ...t, ...(patch.kind !== undefined && { kind: patch.kind }), ...(patch.message !== undefined && { message: patch.message }) };
        if ('pending' in patch) patched.pending = patch.pending || undefined;
        if ('link' in patch) patched.link = patch.link;
        return patched;
      }),
    );
    if (patched) arm(id, patched.kind, patched.pending, patch.timeoutMs);
  }

  /** Drops every toast and pending timer (used by tests and on logout). */
  function reset(): void {
    for (const timer of timers.values()) clearTimeout(timer);
    timers.clear();
    set([]);
  }

  return { subscribe, push, update, dismiss, reset };
}

export const toasts = createToasts();
