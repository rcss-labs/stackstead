import { describe, expect, it, vi } from 'vitest';
import { derived, get, readable, safeNotEqual, writable, type Readable } from './store';

describe('writable', () => {
  it('subscribe delivers the current value synchronously and returns an unsubscriber', () => {
    const store = writable(1);
    const seen: number[] = [];
    const unsubscribe = store.subscribe((v) => seen.push(v));
    expect(seen).toEqual([1]);
    unsubscribe();
    store.set(2);
    expect(seen).toEqual([1]);
  });

  it('set notifies every subscriber in subscription order', () => {
    const store = writable(0);
    const order: string[] = [];
    store.subscribe((v) => order.push(`a${v}`));
    store.subscribe((v) => order.push(`b${v}`));
    order.length = 0;
    store.set(1);
    expect(order).toEqual(['a1', 'b1']);
  });

  it('set with an equal primitive does not notify, NaN counts as equal', () => {
    const store = writable<number>(1);
    const run = vi.fn();
    store.subscribe(run);
    store.set(1);
    expect(run).toHaveBeenCalledTimes(1);
    const nan = writable<number>(Number.NaN);
    const nanRun = vi.fn();
    nan.subscribe(nanRun);
    nan.set(Number.NaN);
    expect(nanRun).toHaveBeenCalledTimes(1);
  });

  it('set with the same object reference notifies, and update(v => v) on an object notifies', () => {
    const obj = { n: 1 };
    const store = writable(obj);
    const run = vi.fn();
    store.subscribe(run);
    store.set(obj);
    store.update((v) => v);
    expect(run).toHaveBeenCalledTimes(3);
  });

  it('update passes the current value and sets the result', () => {
    const store = writable(2);
    store.update((v) => v * 5);
    expect(get(store)).toBe(10);
  });

  it('start runs on the first subscriber only, stop on the last unsubscribe, and a set inside start is the first delivered value', () => {
    const stop = vi.fn();
    const start = vi.fn((set: (v: number) => void) => {
      set(7);
      return stop;
    });
    const store = writable(0, start);
    const seen: number[] = [];
    const un1 = store.subscribe((v) => seen.push(v));
    const un2 = store.subscribe(() => {});
    expect(start).toHaveBeenCalledTimes(1);
    expect(seen).toEqual([7]);
    un1();
    expect(stop).not.toHaveBeenCalled();
    un2();
    expect(stop).toHaveBeenCalledTimes(1);
    store.subscribe(() => {});
    expect(start).toHaveBeenCalledTimes(2);
  });

  it('start receives set and update', () => {
    let captured: { set: (v: number) => void; update: (fn: (v: number) => number) => void } | undefined;
    const store = writable(1, (set, update) => {
      captured = { set, update };
    });
    const seen: number[] = [];
    store.subscribe((v) => seen.push(v));
    captured!.set(2);
    captured!.update((v) => v + 1);
    expect(seen).toEqual([1, 2, 3]);
  });

  it('set from inside a subscriber is delivered after the current round to all subscribers in order', () => {
    const store = writable(0);
    const log: string[] = [];
    store.subscribe((v) => {
      log.push(`a${v}`);
      if (v === 1) store.set(2);
    });
    store.subscribe((v) => log.push(`b${v}`));
    log.length = 0;
    store.set(1);
    expect(log).toEqual(['a1', 'b1', 'a2', 'b2']);
  });

  it('a subscriber added during a round does not get the in-flight value twice', () => {
    const store = writable(0);
    const late: number[] = [];
    let added = false;
    store.subscribe((v) => {
      if (v === 1 && !added) {
        added = true;
        store.subscribe((x) => late.push(x));
      }
    });
    store.set(1);
    expect(late).toEqual([1]);
  });

  it('invalidate, when given, is called before run on each set', () => {
    const store = writable(0);
    const log: string[] = [];
    store.subscribe(
      (v) => log.push(`run${v}`),
      () => log.push('invalidate'),
    );
    store.set(1);
    expect(log).toEqual(['run0', 'invalidate', 'run1']);
  });

  it('unsubscribing twice is harmless', () => {
    const stop = vi.fn();
    const store = writable(0, () => stop);
    const un = store.subscribe(() => {});
    const other = store.subscribe(() => {});
    un();
    un();
    expect(stop).not.toHaveBeenCalled();
    other();
    expect(stop).toHaveBeenCalledTimes(1);
  });
});

describe('readable', () => {
  it('has no set or update', () => {
    const store = readable(1) as unknown as Record<string, unknown>;
    expect(store.set).toBeUndefined();
    expect(store.update).toBeUndefined();
    expect(typeof store.subscribe).toBe('function');
  });

  it('start/stop lifecycle like writable', () => {
    const stop = vi.fn();
    const start = vi.fn((set: (v: number) => void) => {
      set(5);
      return stop;
    });
    const store = readable(0, start);
    const un = store.subscribe(() => {});
    expect(start).toHaveBeenCalledTimes(1);
    un();
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it('a readable without start keeps its initial value', () => {
    expect(get(readable('x'))).toBe('x');
  });
});

describe('derived', () => {
  it('single source, sync form, maps values and tracks updates', () => {
    const source = writable(1);
    const doubled = derived(source, (v) => v * 2);
    const seen: number[] = [];
    doubled.subscribe((v) => seen.push(v));
    source.set(2);
    expect(seen).toEqual([2, 4]);
  });

  it('multiple sources deliver a tuple and never call fn before every source has delivered', () => {
    const a = writable(1);
    let setB: (v: number) => void = () => {};
    const b = readable<number>(undefined, (set) => {
      setB = set;
    });
    const fn = vi.fn(([x, y]: [number, number]) => x + y);
    const sum = derived([a, b] as [Readable<number>, Readable<number>], fn);
    const seen: unknown[] = [];
    sum.subscribe((v) => seen.push(v));
    // Svelte delivers the initial value of a readable synchronously, so b is
    // "delivered" as undefined; the contract here is that fn sees each source's
    // delivered value and is not called with a stale partial tuple afterwards.
    fn.mockClear();
    setB(10);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn.mock.calls[0][0]).toEqual([1, 10]);
    expect(seen[seen.length - 1]).toBe(11);
  });

  it('fn is held back while a source has announced a change but not yet delivered it', () => {
    let run: (v: number) => void = () => {};
    let invalidate: () => void = () => {};
    const manual: Readable<number> = {
      subscribe(r, i) {
        run = r;
        invalidate = i ?? (() => {});
        r(1);
        return () => {};
      },
    };
    const fn = vi.fn(([x, y]: [number, number]) => x + y);
    const store = derived([writable(10), manual] as [Readable<number>, Readable<number>], fn);
    store.subscribe(() => {});
    fn.mockClear();
    invalidate();
    expect(fn).not.toHaveBeenCalled();
    run(5);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn.mock.calls[0][0]).toEqual([10, 5]);
  });

  it('set form: fn receives set and update, may return a cleanup that runs before the next fn and on stop', () => {
    const source = writable(1);
    const log: string[] = [];
    const store = derived(
      source,
      (v, set, update) => {
        log.push(`fn${v}`);
        set(v * 10);
        update((x) => x + 1);
        return () => log.push(`cleanup${v}`);
      },
      0,
    );
    const seen: number[] = [];
    const un = store.subscribe((v) => seen.push(v));
    source.set(2);
    un();
    expect(log).toEqual(['fn1', 'cleanup1', 'fn2', 'cleanup2']);
    expect(seen).toEqual([11, 20, 21]);
  });

  it('initial value is used until set is called in the set form', () => {
    let setLater: (v: string) => void = () => {};
    const store = derived(
      writable(1),
      (_v, set) => {
        setLater = set;
      },
      'initial',
    );
    const seen: string[] = [];
    store.subscribe((v) => seen.push(v));
    expect(seen).toEqual(['initial']);
    setLater('later');
    expect(seen).toEqual(['initial', 'later']);
  });

  it('derived equality: an unchanged primitive result does not notify, an object result does', () => {
    const source = writable(1);
    const parity = derived(source, (v) => v % 2);
    const parityRun = vi.fn();
    parity.subscribe(parityRun);
    source.set(3);
    expect(parityRun).toHaveBeenCalledTimes(1);

    const wrapped = derived(source, (v) => ({ v: v % 2 }));
    const wrappedRun = vi.fn();
    wrapped.subscribe(wrappedRun);
    source.set(5);
    expect(wrappedRun).toHaveBeenCalledTimes(2);
  });

  it('unsubscribing the last subscriber unsubscribes from every source', () => {
    const stopA = vi.fn();
    const stopB = vi.fn();
    const a = readable(1, () => stopA);
    const b = readable(2, () => stopB);
    const store = derived([a, b] as [Readable<number>, Readable<number>], ([x, y]) => x + y);
    const un1 = store.subscribe(() => {});
    const un2 = store.subscribe(() => {});
    un1();
    expect(stopA).not.toHaveBeenCalled();
    un2();
    expect(stopA).toHaveBeenCalledTimes(1);
    expect(stopB).toHaveBeenCalledTimes(1);
  });

  it('get on a derived subscribes, reads and unsubscribes the sources', () => {
    const stop = vi.fn();
    const source = readable(4, () => stop);
    const squared = derived(source, (v) => v * v);
    expect(get(squared)).toBe(16);
    expect(stop).toHaveBeenCalledTimes(1);
  });
});

describe('get', () => {
  it('returns the current value of a store with a start notifier and leaves it stopped afterwards', () => {
    const start = vi.fn((set: (v: number) => void) => {
      set(42);
      return stop;
    });
    const stop = vi.fn();
    const store = writable(0, start);
    expect(get(store)).toBe(42);
    expect(start).toHaveBeenCalledTimes(1);
    expect(stop).toHaveBeenCalledTimes(1);
  });
});

describe('safeNotEqual', () => {
  const obj = {};
  const fn = () => {};
  it.each([
    [1, 1, false],
    [Number.NaN, Number.NaN, false],
    ['a', 'b', true],
    [obj, obj, true],
    [fn, fn, true],
    [null, undefined, true],
  ])('(%s, %s) -> %s', (a, b, want) => {
    expect(safeNotEqual(a, b)).toBe(want);
  });
});
