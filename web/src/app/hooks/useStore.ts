import { useCallback, useSyncExternalStore } from 'react';
import type { Readable } from '$lib/store';

/**
 * Binds a `$lib/store` readable to React through `useSyncExternalStore`, so
 * every existing controller (live stream, debounce, guards) is reused as-is.
 *
 * Caller rule: treat the returned value as immutable. Stores that `update`
 * with a new object re-render; a store that `set`s the same object also
 * re-renders (objects always notify, as in svelte/store).
 *
 * Each store gets one ref-counted hub holding the single real subscription, so
 * the store's start notifier runs once for the first consumer and its stop once
 * after the last one leaves. Invariant: a hub entry exists only while its store
 * subscription is live. A render that never commits opens the hub too (to read
 * the value), so release is deferred with setTimeout(0) rather than done in
 * render or a microtask; passive effects of transitions may run in a later task.
 */

interface Hub {
  value: unknown;
  unsubscribe: () => void;
  listeners: Set<() => void>;
  refs: number;
  releaseTimer: ReturnType<typeof setTimeout> | undefined;
}

const hubs = new WeakMap<Readable<unknown>, Hub>();

function scheduleRelease(store: Readable<unknown>, hub: Hub): void {
  if (hub.releaseTimer !== undefined) clearTimeout(hub.releaseTimer);
  hub.releaseTimer = setTimeout(() => {
    hub.releaseTimer = undefined;
    if (hub.refs > 0) return;
    hub.unsubscribe();
    if (hubs.get(store) === hub) hubs.delete(store);
  }, 0);
}

function acquire<T>(store: Readable<T>): Hub {
  let hub = hubs.get(store);
  if (!hub) {
    const created: Hub = { value: undefined, unsubscribe: () => {}, listeners: new Set(), refs: 0, releaseTimer: undefined };
    hubs.set(store, created);
    let initial = true;
    created.unsubscribe = store.subscribe((value) => {
      created.value = value;
      // The first delivery is synchronous and only seeds the value.
      if (!initial) created.listeners.forEach((listener) => listener());
    });
    initial = false;
    hub = created;
    // Not yet held by a committed consumer (e.g. first render): release if nobody subscribes.
    scheduleRelease(store, hub);
  }
  return hub;
}

export function useStore<T>(store: Readable<T>): T {
  const subscribe = useCallback(
    (onStoreChange: () => void) => {
      const hub = acquire(store);
      if (hub.releaseTimer !== undefined) {
        clearTimeout(hub.releaseTimer);
        hub.releaseTimer = undefined;
      }
      hub.listeners.add(onStoreChange);
      hub.refs += 1;
      return () => {
        hub.listeners.delete(onStoreChange);
        hub.refs -= 1;
        if (hub.refs === 0) scheduleRelease(store, hub);
      };
    },
    [store],
  );
  const getSnapshot = useCallback(() => acquire(store).value as T, [store]);
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
