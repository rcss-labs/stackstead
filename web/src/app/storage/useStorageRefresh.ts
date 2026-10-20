import { useCallback, useEffect, useRef, useState } from 'react';
import { createStorageRefresh } from '$lib/storage/controller';
import { changes } from '$lib/stores/live';
import { operationsRefresh } from '$lib/stores/stacks';
import { useStore } from '../hooks/useStore';

export interface StorageRefreshState<T> {
  data: T | undefined;
  error: string | undefined;
  refresh: () => void;
}

/** Binds the storage refresh controller (event, operation and size-polling refetches) to a component. */
export function useStorageRefresh<T>(load: () => Promise<T>, refreshing: (data: T) => boolean): StorageRefreshState<T> {
  // The controller outlives renders; it reads the latest callbacks through refs instead of closing over stale ones.
  const loadRef = useRef(load);
  const refreshingRef = useRef(refreshing);
  useEffect(() => {
    loadRef.current = load;
    refreshingRef.current = refreshing;
  });
  const [controller] = useState(() =>
    createStorageRefresh<T>({
      load: () => loadRef.current(),
      refreshing: (data) => refreshingRef.current(data),
      changes,
      operations: operationsRefresh,
    }),
  );
  // StrictMode runs mount, cleanup, mount back to back. Deferring start to a microtask lets the cleanup
  // cancel it, so the throwaway first mount never fetches; start and stop are idempotent either way.
  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) controller.start();
    });
    return () => {
      cancelled = true;
      controller.stop();
    };
  }, [controller]);
  const data = useStore(controller.data);
  const error = useStore(controller.error);
  const refresh = useCallback(() => controller.refresh(), [controller]);
  return { data, error, refresh };
}
