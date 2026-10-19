// Resolves the pending toast of an in-flight operation. Every entry point that starts an operation calls
// trackOperation; this store watches the shared running-operations list and, once a tracked operation leaves
// it, asks the server once what became of it (GET /api/operations/{id}). Nothing here polls while the tab is
// hidden, and stop() forgets every entry and drops answers that are still in flight.
import { get, type Readable } from '$lib/store';
import type { Operation } from '../api/types';
import { toasts, type ToastInput } from './toasts';

export const TRACKER_RECHECK_MS = 2000;

export interface TrackedOperation {
  id: string;
  stack: string;
  action: string;
}

interface Entry {
  toastId: number;
  stack: string;
  action: string;
  seen: boolean;
  checking: boolean;
  timer?: ReturnType<typeof setTimeout>;
}

const tracked = new Map<string, Entry>();
let stopActive: (() => void) | null = null;

const label = (op: { stack: string; action: string }) => (op.stack ? `${op.action} · ${op.stack}` : op.action);

export function operationToast(op: Pick<Operation, 'stack' | 'action' | 'state'> & { error?: string }): ToastInput {
  const name = label(op);
  switch (op.state) {
    case 'succeeded':
      return { kind: 'success', message: `${name} succeeded`, pending: false };
    case 'failed':
      return { kind: 'danger', message: `${name} failed${op.error ? `: ${op.error}` : ''}`, pending: false };
    case 'timed_out':
      return { kind: 'danger', message: `${name} timed out`, pending: false };
    case 'cancelled':
      return { kind: 'warning', message: `${name} cancelled`, pending: false };
    default:
      return { kind: 'info', message: name, pending: true };
  }
}

/** Pushes the pending toast for a started operation; tracking the same id again returns the existing toast. */
export function trackOperation(op: TrackedOperation): number {
  const existing = tracked.get(op.id);
  if (existing) {
    if (get(toasts).some((t) => t.id === existing.toastId)) return existing.toastId;
    clearTimeout(existing.timer); // the toast was dismissed (or reset): track the operation afresh
    tracked.delete(op.id);
  }
  const href = op.stack ? `/stacks/${encodeURIComponent(op.stack)}?tab=operations&op=${encodeURIComponent(op.id)}` : undefined;
  const toastId = toasts.push({
    ...operationToast({ stack: op.stack, action: op.action, state: 'running' }),
    ...(href && { link: { label: 'View', href } }),
  });
  tracked.set(op.id, { toastId, stack: op.stack, action: op.action, seen: false, checking: false });
  return toastId;
}

export function startOperationTracker(deps: {
  runningOps: Readable<Operation[]>;
  get: (id: string) => Promise<Operation>;
  doc?: Document;
}): () => void {
  const doc = deps.doc ?? document;
  stopActive?.(); // idempotent: a second start replaces the first
  let stopped = false;
  let present = new Set<string>();

  function resolve(id: string, entry: Entry, patch: ToastInput) {
    clearTimeout(entry.timer);
    tracked.delete(id);
    toasts.update(entry.toastId, patch);
  }

  async function check(id: string) {
    const entry = tracked.get(id);
    if (!entry || entry.checking || stopped) return;
    entry.checking = true;
    clearTimeout(entry.timer);
    entry.timer = undefined;
    try {
      const op = await deps.get(id);
      if (stopped || tracked.get(id) !== entry) return;
      if (op.state === 'running') {
        if (!present.has(id) && doc.visibilityState !== 'hidden') {
          entry.timer = setTimeout(() => void check(id), TRACKER_RECHECK_MS);
        }
      } else {
        resolve(id, entry, operationToast(op));
      }
    } catch (e) {
      if (stopped || tracked.get(id) !== entry) return;
      if ((e as { status?: number }).status === 404) {
        resolve(id, entry, { kind: 'danger', message: `${label(entry)} no longer available`, pending: false });
      } // any other failure keeps the entry; the next list change retries
    } finally {
      entry.checking = false;
    }
  }

  const unsubscribe = deps.runningOps.subscribe((ops) => {
    if (stopped) return;
    present = new Set(ops.map((o) => o.id));
    for (const [id, entry] of tracked) {
      if (present.has(id)) {
        entry.seen = true;
        clearTimeout(entry.timer);
        entry.timer = undefined;
      } else {
        void check(id);
      }
    }
  });

  const onVisibility = () => {
    if (stopped || doc.visibilityState === 'hidden') return;
    for (const id of tracked.keys()) if (!present.has(id)) void check(id);
  };
  doc.addEventListener('visibilitychange', onVisibility);

  const stop = () => {
    if (stopped) return;
    stopped = true;
    unsubscribe();
    doc.removeEventListener('visibilitychange', onVisibility);
    for (const entry of tracked.values()) clearTimeout(entry.timer);
    tracked.clear();
    if (stopActive === stop) stopActive = null;
  };
  stopActive = stop;
  return stop;
}
