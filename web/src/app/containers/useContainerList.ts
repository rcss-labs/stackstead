import { useCallback, useEffect, useState } from 'react';
import type { ContainerRow } from '$lib/api/types';
import { createContainerList } from '$lib/containers/list';
import { useStore } from '../hooks/useStore';
import { useServer } from '../servers/ServerContext';

export interface ContainerListState {
  rows: ContainerRow[] | undefined;
  error: string | undefined;
  refresh: () => void;
  /** Applies a fresh row at once, ahead of the next fetch. */
  patch: (row: ContainerRow) => void;
}

export function useContainerList(): ContainerListState {
  const { api, changes } = useServer();
  const [list] = useState(() => createContainerList({ list: api.containers, changes }));
  // StrictMode runs mount, cleanup, mount back to back. Deferring start to a microtask lets the cleanup
  // cancel it, so the throwaway first mount never fetches; start and stop are idempotent either way.
  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) list.start();
    });
    return () => {
      cancelled = true;
      list.stop();
    };
  }, [list]);
  const rows = useStore(list.rows);
  const error = useStore(list.error);
  const refresh = useCallback(() => list.refresh(), [list]);
  const patch = useCallback((row: ContainerRow) => list.patch(row), [list]);
  return { rows, error, refresh, patch };
}
