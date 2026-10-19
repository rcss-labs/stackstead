import { get, writable } from '$lib/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Operation } from '../api/types';
import type { LiveChange } from './live';
import { runningOps, runningStackNames, startRunningOps } from './runningOps';

const op = (stack: string) => ({ id: `op-${stack}`, stack, action: 'up', state: 'running' }) as Operation;

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

function setup(doc?: Document) {
  let result: Operation[] = [];
  let failing = false;
  const list = vi.fn(async () => {
    if (failing) throw new Error('boom');
    return result;
  });
  const changes = writable<LiveChange | null>(null);
  const refresh = writable(0);
  return {
    list,
    changes,
    refresh,
    setResult: (r: Operation[]) => (result = r),
    setFailing: (f: boolean) => (failing = f),
    start: () => startRunningOps({ list, changes, refresh, doc }),
  };
}

let stop: (() => void) | undefined;

describe('running operations store', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    stop?.();
    stop = undefined;
    vi.useRealTimers();
  });

  it('fetches once on start and is empty after stop', async () => {
    const t = setup();
    t.setResult([op('web')]);
    stop = t.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(t.list).toHaveBeenCalledTimes(1);
    expect(get(runningOps)).toEqual([op('web')]);
    stop();
    expect(get(runningOps)).toEqual([]);
  });

  it('a burst of ten changes collapses into one debounced fetch, a resync fetches at once', async () => {
    const t = setup();
    stop = t.start();
    await vi.advanceTimersByTimeAsync(0);
    t.list.mockClear();
    for (let i = 1; i <= 10; i++) t.changes.set({ project: `p${i}`, seq: i });
    await vi.advanceTimersByTimeAsync(299);
    expect(t.list).toHaveBeenCalledTimes(0);
    await vi.advanceTimersByTimeAsync(2);
    expect(t.list).toHaveBeenCalledTimes(1);
    t.changes.set({ project: '', seq: 11, resync: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(t.list).toHaveBeenCalledTimes(2);
  });

  it('while an operation runs it rechecks at 2 s, 4 s, 8 s then every 10 s, and stops once nothing runs', async () => {
    const t = setup();
    t.setResult([op('web')]);
    stop = t.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(t.list).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1999);
    expect(t.list).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(t.list).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(4000);
    expect(t.list).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(8000);
    expect(t.list).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(10000);
    expect(t.list).toHaveBeenCalledTimes(5);
    await vi.advanceTimersByTimeAsync(10000);
    expect(t.list).toHaveBeenCalledTimes(6);
    t.setResult([]);
    await vi.advanceTimersByTimeAsync(10000);
    expect(t.list).toHaveBeenCalledTimes(7);
    expect(get(runningOps)).toEqual([]);
    await vi.advanceTimersByTimeAsync(30000);
    expect(t.list).toHaveBeenCalledTimes(7);
  });

  it('refreshRunningOps fetches immediately and resets the backoff', async () => {
    const t = setup();
    t.setResult([op('web')]);
    stop = t.start();
    await vi.advanceTimersByTimeAsync(2000 + 4000); // calls at 0, 2 s, 6 s; next recheck is 8 s later
    expect(t.list).toHaveBeenCalledTimes(3);
    t.refresh.update((n) => n + 1);
    await vi.advanceTimersByTimeAsync(0);
    expect(t.list).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(1999);
    expect(t.list).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(1);
    expect(t.list).toHaveBeenCalledTimes(5); // 2 s again, not 8 s
  });

  it('a failed recheck retries only while tracking a running operation', async () => {
    const t = setup();
    t.setResult([op('web')]);
    stop = t.start();
    await vi.advanceTimersByTimeAsync(0);
    t.setFailing(true);
    await vi.advanceTimersByTimeAsync(2000);
    expect(t.list).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(4000);
    expect(t.list).toHaveBeenCalledTimes(3); // kept retrying
    expect(get(runningOps)).toEqual([op('web')]); // last value kept
    stop();

    const idle = setup();
    idle.setFailing(true);
    stop = idle.start();
    await vi.advanceTimersByTimeAsync(60000);
    expect(idle.list).toHaveBeenCalledTimes(1);
  });

  it('a late response after stop is dropped and the list stays empty', async () => {
    let release!: (ops: Operation[]) => void;
    const list = vi.fn(() => new Promise<Operation[]>((r) => (release = r)));
    stop = startRunningOps({ list, changes: writable(null), refresh: writable(0) });
    stop();
    release([op('web')]);
    await vi.advanceTimersByTimeAsync(20000);
    expect(get(runningOps)).toEqual([]);
    expect(list).toHaveBeenCalledTimes(1);
  });

  it('stop cancels a pending recheck so nothing is written back after logout', async () => {
    const t = setup();
    t.setResult([op('web')]);
    stop = t.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(t.list).toHaveBeenCalledTimes(1);
    stop();
    await vi.advanceTimersByTimeAsync(30000);
    expect(t.list).toHaveBeenCalledTimes(1);
    expect(get(runningOps)).toEqual([]);
  });

  describe('hidden tab', () => {
    it('makes no calls while hidden, fetches once on becoming visible, then resumes the backoff', async () => {
      const t = setup();
      const d = fakeDoc();
      t.setResult([op('web')]);
      stop = startRunningOps({ list: t.list, changes: t.changes, refresh: t.refresh, doc: d.doc });
      await vi.advanceTimersByTimeAsync(0);
      expect(t.list).toHaveBeenCalledTimes(1);
      d.setState('hidden');
      t.changes.set({ project: 'p', seq: 1 });
      t.refresh.update((n) => n + 1);
      await vi.advanceTimersByTimeAsync(30000);
      expect(t.list).toHaveBeenCalledTimes(1);
      d.setState('visible');
      await vi.advanceTimersByTimeAsync(0);
      expect(t.list).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(2000);
      expect(t.list).toHaveBeenCalledTimes(3);
    });

    it('a response that lands while hidden schedules no recheck', async () => {
      let release!: (ops: Operation[]) => void;
      const list = vi.fn(() => new Promise<Operation[]>((r) => (release = r)));
      const d = fakeDoc();
      stop = startRunningOps({ list, changes: writable(null), refresh: writable(0), doc: d.doc });
      d.setState('hidden');
      release([op('web')]);
      await vi.advanceTimersByTimeAsync(30000);
      expect(list).toHaveBeenCalledTimes(1);
    });

    it('stop removes the visibility listener', async () => {
      const t = setup();
      const d = fakeDoc('hidden');
      stop = startRunningOps({ list: t.list, changes: t.changes, refresh: t.refresh, doc: d.doc });
      stop();
      d.setState('visible');
      await vi.advanceTimersByTimeAsync(0);
      expect(t.list).not.toHaveBeenCalled();
    });
  });

  it('runningStackNames returns the distinct stack names', () => {
    expect(runningStackNames([op('a'), op('b'), op('a')])).toEqual(new Set(['a', 'b']));
  });
});
