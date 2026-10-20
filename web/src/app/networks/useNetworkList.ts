import { useCallback, useEffect, useState } from 'react';
import { api } from '$lib/api/client';
import type { Network } from '$lib/api/types';
import { createNetworkList } from '$lib/networks/networks';
import { changes } from '$lib/stores/live';
import { useStore } from '../hooks/useStore';

export interface NetworkListState {
  rows: Network[] | undefined;
  error: string | undefined;
  refresh: () => void;
}

export function useNetworkList(): NetworkListState {
  const [list] = useState(() => createNetworkList({ list: api.networks, changes }));
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
  return { rows, error, refresh };
}
