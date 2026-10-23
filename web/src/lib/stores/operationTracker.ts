// Resolves the pending toast of an in-flight operation. Every entry point that starts an operation calls
// trackOperation; this store watches the shared running-operations list and, once a tracked operation leaves
// it, asks the server once what became of it (GET /api/operations/{id}). Nothing here polls while the tab is
// hidden, and stop() forgets every entry and drops answers that are still in flight.
import { get, type Readable } from '$lib/store';
import type { Operation } from '../api/types';
import { operationSubject } from '$lib/images/ops';
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
  checking: boolean;
  timer?: ReturnType<typeof setTimeout>;
}

const label = (op: { stack: string; action: string }) => (op.stack ? `${op.action} · ${operationSubject(op).label}` : op.action);

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

/** One independent set of tracked operations; the module-level exports below are the local server's instance. */
export function createOperationTracker(opts: { href?: (path: string) => string; stoppedMessage?: string } = {}) {
  const scopeHref = opts.href ?? ((path: string) => path);
  const tracked = new Map<string, Entry>();
  let stopActive: (() => void) | null = null;

  /**
   * Pushes the pending toast for a started operation; tracking the same id again returns the existing toast
   * (unless that toast was dismissed, then it is tracked afresh). Callers must call refreshRunningOps() after
   * tracking: the tracker only reacts to running-list emissions.
   */
  function trackOperation(op: TrackedOperation): number {
    const existing = tracked.get(op.id);
    if (existing) {
      if (get(toasts).some((t) => t.id === existing.toastId)) return existing.toastId;
      clearTimeout(existing.timer); // the toast was dismissed (or reset): track the operation afresh
      tracked.delete(op.id);
    }
    const href = op.stack
      ? scopeHref(`/stacks/${encodeURIComponent(op.stack)}?tab=operations&op=${encodeURIComponent(op.id)}`)
      : undefined;
    const toastId = toasts.push({
      ...operationToast({ stack: op.stack, action: op.action, state: 'running' }),
      ...(href && { link: { label: 'View', href } }),
    });
    tracked.set(op.id, { toastId, stack: op.stack, action: op.action, checking: false });
    return toastId;
  }

  function startOperationTracker(deps: {
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
        } else if (doc.visibilityState !== 'hidden') {
          // Any other failure keeps the entry. A quiet host emits no further list change, so retry on a timer.
          entry.timer = setTimeout(() => void check(id), TRACKER_RECHECK_MS);
        }
      } finally {
        entry.checking = false;
      }
    }

    const unsubscribe = deps.runningOps.subscribe((ops) => {
      if (stopped) return;
      present = new Set(ops.map((o) => o.id));
      for (const [id, entry] of tracked) {
        if (present.has(id)) {
          clearTimeout(entry.timer);
          entry.timer = undefined;
        } else {
          void check(id);
        }
      }
    });

    const onVisibility = () => {
      if (stopped) return;
      if (doc.visibilityState === 'hidden') {
        // A recheck armed while visible must not fire into a hidden tab.
        for (const entry of tracked.values()) {
          clearTimeout(entry.timer);
          entry.timer = undefined;
        }
        return;
      }
      for (const id of tracked.keys()) if (!present.has(id)) void check(id);
    };
    doc.addEventListener('visibilitychange', onVisibility);

    const stop = () => {
      if (stopped) return;
      stopped = true;
      unsubscribe();
      doc.removeEventListener('visibilitychange', onVisibility);
      for (const entry of tracked.values()) {
        clearTimeout(entry.timer);
        // Nobody watches these any more; a pending toast would spin forever. The View link stays.
        if (opts.stoppedMessage) toasts.update(entry.toastId, { kind: 'info', message: opts.stoppedMessage, pending: false });
      }
      tracked.clear();
      if (stopActive === stop) stopActive = null;
    };
    stopActive = stop;
    return stop;
  }

  return { trackOperation, startOperationTracker };
}

const local = createOperationTracker();
export const { trackOperation, startOperationTracker } = local;
