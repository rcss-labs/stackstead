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
 * subscription is live, and is cached only once store.subscribe has succeeded.
 *
 * Release is always deferred on a timer, with two delays:
 * - Creation grace (CREATION_GRACE_MS): a hub opened in render (to read the
 *   value) has no committed consumer yet. useSyncExternalStore subscribes in a
 *   passive effect, which outside act() can run in a later macrotask than the
 *   render (default-lane and transition renders), so the hub is temporarily
 *   retained, as Relay and Apollo do. A render that never commits is released
 *   when the grace expires.
 * - After the last committed consumer unsubscribes, release runs on the next
 *   task (0 ms), so a StrictMode remount or a store swap can pick the hub up
 *   again without restarting the store.
 * A subscribe that arrives cancels any pending release.
 */

interface Hub {
  value: unknown;
  unsubscribe: () => void;
  listeners: Set<() => void>;
  refs: number;
  releaseTimer: ReturnType<typeof setTimeout> | undefined;
}

const hubs = new WeakMap<Readable<unknown>, Hub>();

const CREATION_GRACE_MS = 1000;
const LAST_CONSUMER_RELEASE_MS = 0;

function scheduleRelease(store: Readable<unknown>, hub: Hub, delayMs: number): void {
  if (hub.releaseTimer !== undefined) clearTimeout(hub.releaseTimer);
  hub.releaseTimer = setTimeout(() => {
    hub.releaseTimer = undefined;
    if (hub.refs > 0) return;
    hub.unsubscribe();
    if (hubs.get(store) === hub) hubs.delete(store);
  }, delayMs);
}

function acquire<T>(store: Readable<T>): Hub {
  let hub = hubs.get(store);
  if (!hub) {
    const created: Hub = { value: undefined, unsubscribe: () => {}, listeners: new Set(), refs: 0, releaseTimer: undefined };
    let initial = true;
    // If subscribe throws, nothing is cached and the next render tries afresh.
    created.unsubscribe = store.subscribe((value) => {
      created.value = value;
      // The first delivery is synchronous and only seeds the value.
      if (!initial) created.listeners.forEach((listener) => listener());
    });
    initial = false;
    hubs.set(store, created);
    hub = created;
    // Not yet held by a committed consumer (e.g. first render): release if nobody subscribes in time.
    scheduleRelease(store, hub, CREATION_GRACE_MS);
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
        if (hub.refs === 0) scheduleRelease(store, hub, LAST_CONSUMER_RELEASE_MS);
      };
    },
    [store],
  );
  const getSnapshot = useCallback(() => acquire(store).value as T, [store]);
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
