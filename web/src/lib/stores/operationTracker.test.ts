import { get, writable } from '$lib/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Operation } from '../api/types';
import { operationToast, startOperationTracker, trackOperation, TRACKER_RECHECK_MS } from './operationTracker';
import { toasts } from './toasts';

const running = (id: string, stack = 'web', action = 'up'): Operation => ({ id, stack, action, state: 'running', startedAt: '2026-10-19T10:00:00Z' });
const finished = (state: Operation['state'], extra: Partial<Operation> = {}): Operation => ({ ...running('op1'), state, ...extra });

function fakeDoc(state: 'visible' | 'hidden' = 'visible') {
  const target = new EventTarget();
  const doc = target as unknown as Document & { visibilityState: string };
  Object.defineProperty(doc, 'visibilityState', { value: state, writable: true, configurable: true });
  return {
    doc,
    setState(next: 'visible' | 'hidden') {
      (doc as { visibilityState: string }).visibilityState = next;
      target.dispatchEvent(new Event('visibilitychange'));
    },
  };
}

let ops: ReturnType<typeof writable<Operation[]>>;
let getOp: ReturnType<typeof vi.fn<(id: string) => Promise<Operation>>>;
let stop: (() => void) | undefined;

const start = (doc?: Document) => (stop = startOperationTracker({ runningOps: ops, get: getOp, doc }));
const toast = () => get(toasts)[0];

beforeEach(() => {
  vi.useFakeTimers();
  toasts.reset();
  ops = writable<Operation[]>([]);
  getOp = vi.fn<(id: string) => Promise<Operation>>();
});
afterEach(() => {
  stop?.();
  stop = undefined;
  startOperationTracker({ runningOps: ops, get: getOp, doc: fakeDoc().doc })(); // starting and stopping forgets every tracked entry
  toasts.reset();
  vi.useRealTimers();
});

describe('operationToast', () => {
  it('reads the action alone for a host operation', () => {
    expect(operationToast({ stack: '', action: 'storage-cleanup', state: 'succeeded' })).toEqual({
      kind: 'success',
      message: 'storage-cleanup succeeded',
      pending: false,
    });
  });
});

describe('operation tracker', () => {
  it('trackOperation pushes one pending info toast with the View link and returns its id', () => {
    const id = trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    expect(get(toasts)).toHaveLength(1);
    expect(toast()).toMatchObject({
      id,
      kind: 'info',
      message: 'up · web',
      pending: true,
      link: { label: 'View', href: '/stacks/web?tab=operations&op=op1' },
    });
    expect(trackOperation({ id: 'op1', stack: 'web', action: 'up' })).toBe(id);
    expect(get(toasts)).toHaveLength(1);
  });

  it('an operation seen in the running list resolves to success when it disappears and the operation reports succeeded', async () => {
    start();
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    ops.set([running('op1')]);
    await vi.advanceTimersByTimeAsync(0);
    expect(getOp).not.toHaveBeenCalled();
    getOp.mockResolvedValueOnce(finished('succeeded'));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(getOp).toHaveBeenCalledTimes(1);
    expect(getOp).toHaveBeenCalledWith('op1');
    expect(toast()).toMatchObject({ kind: 'success', message: 'up · web succeeded', link: { label: 'View', href: '/stacks/web?tab=operations&op=op1' } });
    expect(toast().pending).toBeUndefined();
    await vi.advanceTimersByTimeAsync(5000);
    expect(get(toasts)).toHaveLength(0);
  });

  it.each([
    ['failed', { error: 'exit 1' }, 'danger', 'up · web failed: exit 1'],
    ['failed', {}, 'danger', 'up · web failed'],
    ['timed_out', {}, 'danger', 'up · web timed out'],
    ['cancelled', {}, 'warning', 'up · web cancelled'],
  ] as const)('failure kind %s resolves to %s', async (state, extra, kind, message) => {
    start();
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    getOp.mockResolvedValueOnce(finished(state, extra));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(toast()).toMatchObject({ kind, message });
    expect(toast().pending).toBeUndefined();
  });

  it('absent but still running waits and rechecks after 2 s', async () => {
    start();
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    getOp.mockResolvedValueOnce(running('op1'));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(getOp).toHaveBeenCalledTimes(1);
    expect(toast().pending).toBe(true);
    getOp.mockResolvedValueOnce(finished('succeeded'));
    await vi.advanceTimersByTimeAsync(TRACKER_RECHECK_MS);
    expect(getOp).toHaveBeenCalledTimes(2);
    expect(toast()).toMatchObject({ kind: 'success', message: 'up · web succeeded' });
  });

  it('an absent id is checked once per list change, not once per render', async () => {
    start();
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    getOp.mockReturnValue(new Promise(() => {}));
    ops.set([]);
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(getOp).toHaveBeenCalledTimes(1);
  });

  it('no recheck timer while hidden, one check on becoming visible', async () => {
    const d = fakeDoc('hidden');
    start(d.doc);
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    getOp.mockResolvedValueOnce(running('op1'));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(getOp).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(TRACKER_RECHECK_MS * 3);
    expect(getOp).toHaveBeenCalledTimes(1);
    getOp.mockResolvedValueOnce(finished('succeeded'));
    d.setState('visible');
    await vi.advanceTimersByTimeAsync(0);
    expect(getOp).toHaveBeenCalledTimes(2);
    expect(toast()).toMatchObject({ kind: 'success' });
  });

  it('a 404 resolves the toast as danger no longer available', async () => {
    start();
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    getOp.mockRejectedValueOnce(Object.assign(new Error('not found'), { status: 404 }));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(toast()).toMatchObject({ kind: 'danger', message: 'up · web no longer available' });
    expect(toast().pending).toBeUndefined();
  });

  it('a failed get keeps the entry and retries on the next change', async () => {
    start();
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    getOp.mockRejectedValueOnce(new Error('offline'));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(toast().pending).toBe(true);
    getOp.mockResolvedValueOnce(finished('succeeded'));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(getOp).toHaveBeenCalledTimes(2);
    expect(toast()).toMatchObject({ kind: 'success' });
  });

  it('stop cancels timers and ignores late answers', async () => {
    start();
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    let answer!: (op: Operation) => void;
    getOp.mockReturnValueOnce(new Promise<Operation>((resolve) => (answer = resolve)));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    stop!();
    stop = undefined;
    answer(finished('succeeded'));
    await vi.advanceTimersByTimeAsync(0);
    expect(toast()).toMatchObject({ kind: 'info', pending: true });
    expect(vi.getTimerCount()).toBe(0);
  });

  it('stop cancels an armed recheck timer', async () => {
    start();
    trackOperation({ id: 'op1', stack: 'web', action: 'up' });
    getOp.mockResolvedValueOnce(running('op1'));
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(1);
    stop!();
    stop = undefined;
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(TRACKER_RECHECK_MS * 2);
    expect(getOp).toHaveBeenCalledTimes(1);
  });

  it('host operations read the action alone', async () => {
    start();
    trackOperation({ id: 'op2', stack: '', action: 'storage-cleanup' });
    expect(toast()).toMatchObject({ message: 'storage-cleanup', pending: true });
    expect(toast().link).toBeUndefined();
    getOp.mockResolvedValueOnce({ ...finished('succeeded'), id: 'op2', stack: '', action: 'storage-cleanup' });
    ops.set([]);
    await vi.advanceTimersByTimeAsync(0);
    expect(toast()).toMatchObject({ kind: 'success', message: 'storage-cleanup succeeded' });
  });
});
