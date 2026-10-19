import { get } from '$lib/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { HostSnapshot } from '../api/types';
import { host, startHost } from './host';

const snap = (hostname: string) => ({ hostname }) as HostSnapshot;

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

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

let stop: (() => void) | undefined;

describe('host poller', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    stop?.();
    stop = undefined;
    vi.useRealTimers();
  });

  it('fetches on start and every 10 s while visible', async () => {
    const fetchHost = vi.fn(async () => snap('h'));
    stop = startHost({ host: fetchHost, doc: fakeDoc().doc });
    expect(fetchHost).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fetchHost).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fetchHost).toHaveBeenCalledTimes(3);
    expect(get(host)?.hostname).toBe('h');
  });

  it('pauses while hidden and fetches once when visible again', async () => {
    const fetchHost = vi.fn(async () => snap('h'));
    const d = fakeDoc();
    stop = startHost({ host: fetchHost, doc: d.doc });
    d.setState('hidden');
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetchHost).toHaveBeenCalledTimes(1);
    d.setState('visible');
    expect(fetchHost).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fetchHost).toHaveBeenCalledTimes(3);
  });

  it('a response after stop is ignored and the value is cleared', async () => {
    const slow = deferred<HostSnapshot>();
    stop = startHost({ host: () => slow.promise, doc: fakeDoc().doc });
    stop();
    stop = undefined;
    slow.resolve(snap('late'));
    await vi.advanceTimersByTimeAsync(0);
    expect(get(host)).toBeUndefined();
  });

  it('clears a stored value on stop', async () => {
    stop = startHost({ host: async () => snap('h'), doc: fakeDoc().doc });
    await vi.advanceTimersByTimeAsync(0);
    expect(get(host)?.hostname).toBe('h');
    stop();
    stop = undefined;
    expect(get(host)).toBeUndefined();
  });

  it('an older slow response does not overwrite a newer one', async () => {
    const slow = deferred<HostSnapshot>();
    const calls = [slow.promise, Promise.resolve(snap('new'))];
    const d = fakeDoc();
    stop = startHost({ host: () => calls.shift()!, doc: d.doc });
    // Hide and show again: the second fetch starts while the first is still pending.
    d.setState('hidden');
    d.setState('visible');
    await vi.advanceTimersByTimeAsync(0);
    expect(get(host)?.hostname).toBe('new');
    slow.resolve(snap('old'));
    await vi.advanceTimersByTimeAsync(0);
    expect(get(host)?.hostname).toBe('new');
  });

  it('skips interval ticks while a fetch is in flight', async () => {
    const slow = deferred<HostSnapshot>();
    const fetchHost = vi.fn(() => slow.promise);
    stop = startHost({ host: fetchHost, doc: fakeDoc().doc });
    await vi.advanceTimersByTimeAsync(30_000);
    expect(fetchHost).toHaveBeenCalledTimes(1);
  });

  it('a failure keeps the previous snapshot', async () => {
    const fetchHost = vi.fn<() => Promise<HostSnapshot>>().mockResolvedValueOnce(snap('keep')).mockRejectedValue(new Error('down'));
    stop = startHost({ host: fetchHost, doc: fakeDoc().doc });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fetchHost).toHaveBeenCalledTimes(2);
    expect(get(host)?.hostname).toBe('keep');
  });

  it('starting twice keeps one interval', async () => {
    const fetchHost = vi.fn(async () => snap('h'));
    const d = fakeDoc();
    const stopA = startHost({ host: fetchHost, doc: d.doc });
    const stopB = startHost({ host: fetchHost, doc: d.doc });
    stop = () => {
      stopA();
      stopB();
    };
    await vi.advanceTimersByTimeAsync(10_000);
    expect(fetchHost).toHaveBeenCalledTimes(2);
  });
});
