// Dependency-free re-implementation of the `svelte/store` contract (writable,
// readable, derived, get). The controllers must outlive Svelte, and React reads
// them through a single hook; reproducing Svelte's exact notification rules
// (synchronous first delivery, queued sets during a round, safe equality) keeps
// the import switch behaviour-neutral.

export type Subscriber<T> = (value: T) => void;
export type Unsubscriber = () => void;
export type Updater<T> = (value: T) => T;
export type StartStopNotifier<T> = (
  set: (value: T) => void,
  update: (fn: Updater<T>) => void,
) => void | (() => void);

export interface Readable<T> {
  subscribe(run: Subscriber<T>, invalidate?: () => void): Unsubscriber;
}

export interface Writable<T> extends Readable<T> {
  set(value: T): void;
  update(fn: Updater<T>): void;
}

export type Stores = Readable<unknown> | [Readable<unknown>, ...Readable<unknown>[]] | Readable<unknown>[];

export type StoresValues<S> =
  S extends Readable<infer U> ? U : { [K in keyof S]: S[K] extends Readable<infer U> ? U : never };

const noop = () => {};

/** Svelte's rule: NaN equals NaN, otherwise `!==`; objects and functions always count as changed. */
export function safeNotEqual(a: unknown, b: unknown): boolean {
  return a != a ? b == b : a !== b || (a !== null && typeof a === 'object') || typeof a === 'function';
}

type SubscriberEntry<T> = [Subscriber<T>, () => void];

// Shared across all stores so a set issued during a round is delivered after it.
const subscriberQueue: unknown[] = [];

export function writable<T>(value?: T, start: StartStopNotifier<T> = noop): Writable<T> {
  let stop: (() => void) | null = null;
  const subscribers = new Set<SubscriberEntry<T>>();

  function set(newValue: T): void {
    if (!safeNotEqual(value, newValue)) return;
    value = newValue;
    if (!stop) return; // not started: nobody to tell
    const runQueue = subscriberQueue.length === 0;
    for (const subscriber of subscribers) {
      subscriber[1]();
      subscriberQueue.push(subscriber, value);
    }
    if (runQueue) {
      for (let i = 0; i < subscriberQueue.length; i += 2) {
        (subscriberQueue[i] as SubscriberEntry<T>)[0](subscriberQueue[i + 1] as T);
      }
      subscriberQueue.length = 0;
    }
  }

  function update(fn: Updater<T>): void {
    set(fn(value as T));
  }

  function subscribe(run: Subscriber<T>, invalidate: () => void = noop): Unsubscriber {
    const subscriber: SubscriberEntry<T> = [run, invalidate];
    subscribers.add(subscriber);
    if (subscribers.size === 1) {
      stop = start(set, update) || noop;
    }
    run(value as T);
    return () => {
      subscribers.delete(subscriber);
      if (subscribers.size === 0 && stop) {
        stop();
        stop = null;
      }
    };
  }

  return { set, update, subscribe };
}

export function readable<T>(value?: T, start?: StartStopNotifier<T>): Readable<T> {
  return { subscribe: writable(value, start).subscribe };
}

export function derived<S extends Stores, T>(
  stores: S,
  fn: (values: StoresValues<S>, set: (value: T) => void, update: (fn: Updater<T>) => void) => Unsubscriber | void,
  initial?: T,
): Readable<T>;
export function derived<S extends Stores, T>(stores: S, fn: (values: StoresValues<S>) => T, initial?: T): Readable<T>;
export function derived<T>(stores: Stores, fn: Function, initial?: T): Readable<T> {
  const single = !Array.isArray(stores);
  const storesArray: Readable<unknown>[] = single ? [stores as Readable<unknown>] : (stores as Readable<unknown>[]);
  if (!storesArray.every(Boolean)) {
    throw new Error('derived() expects stores as input, got a falsy value');
  }
  const auto = fn.length < 2;

  return readable<T>(initial, (set, update) => {
    let started = false;
    const values: unknown[] = [];
    let pending = 0; // bitmask of sources that announced a change but have not delivered it yet
    let cleanup: () => void = noop;

    const sync = () => {
      if (pending) return;
      cleanup();
      const result = fn(single ? values[0] : values, set, update);
      if (auto) {
        set(result as T);
      } else {
        cleanup = typeof result === 'function' ? (result as () => void) : noop;
      }
    };

    const unsubscribers = storesArray.map((store, i) =>
      store.subscribe(
        (value) => {
          values[i] = value;
          pending &= ~(1 << i);
          if (started) sync();
        },
        () => {
          pending |= 1 << i;
        },
      ),
    );

    started = true;
    sync();

    return () => {
      for (const unsubscribe of unsubscribers) unsubscribe();
      cleanup();
      // Queued callbacks may still fire after unsubscribing; they must not re-run fn.
      started = false;
    };
  });
}

export function get<T>(store: Readable<T>): T {
  let value: T | undefined;
  store.subscribe((v) => (value = v))();
  return value as T;
}
