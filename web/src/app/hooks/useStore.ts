import { useCallback, useSyncExternalStore } from 'react';
import { get } from '$lib/store';
import type { Readable } from '$lib/store';

/**
 * Binds a `$lib/store` readable to React through `useSyncExternalStore`, so
 * every existing controller (live stream, debounce, guards) is reused as-is.
 *
 * Caller rule: treat the returned value as immutable. Stores that `update`
 * with a new object re-render; a store that `set`s the same object also
 * re-renders (objects always notify, as in svelte/store).
 */

interface Snapshot {
  value: unknown;
  subscribers: number;
}

// Latest value delivered per store. Keeps getSnapshot stable between renders
// and avoids opening a fresh subscription on every render.
const snapshots = new WeakMap<Readable<unknown>, Snapshot>();

function getSnapshotFor<T>(store: Readable<T>): T {
  let snapshot = snapshots.get(store);
  if (!snapshot) {
    // Not subscribed yet (first render): one-off read, then cached.
    snapshot = { value: get(store), subscribers: 0 };
    snapshots.set(store, snapshot);
  }
  return snapshot.value as T;
}

export function useStore<T>(store: Readable<T>): T {
  const subscribe = useCallback(
    (onStoreChange: () => void) => {
      let snapshot = snapshots.get(store);
      if (!snapshot) {
        snapshot = { value: undefined, subscribers: 0 };
        snapshots.set(store, snapshot);
      }
      const entry = snapshot;
      entry.subscribers += 1;
      let initial = true;
      const unsubscribe = store.subscribe((value) => {
        entry.value = value;
        // The first delivery is synchronous and only refreshes the cache.
        if (!initial) onStoreChange();
      });
      initial = false;
      return () => {
        unsubscribe();
        entry.subscribers -= 1;
        // Drop the cache once unused so a later mount reads a fresh value.
        if (entry.subscribers === 0 && snapshots.get(store) === entry) snapshots.delete(store);
      };
    },
    [store],
  );
  const getSnapshot = useCallback(() => getSnapshotFor(store), [store]);
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
