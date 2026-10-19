import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '$lib/api/client';
import type { Operation } from '$lib/api/types';
import { debounce } from '$lib/stores/debounce';
import { changes } from '$lib/stores/live';
import { operationsRefresh } from '$lib/stores/stacks';
import { useStore } from './useStore';

/**
 * Running operations for the top bar. Every container event is a change, but the badge only needs the
 * settled state: the first change and resyncs fetch at once, bursts collapse through the debounce, and an
 * explicit operationsRefresh fetches again. Signed out, nothing is fetched and the list is empty.
 */
export function useRunningOps(authenticated: boolean): Operation[] {
  const [ops, setOps] = useState<Operation[]>([]);
  const change = useStore(changes);
  const refreshTick = useStore(operationsRefresh);
  const sequence = useRef(0);
  const first = useRef(true);

  const refresh = useMemo(
    () =>
      debounce(() => {
        const mine = ++sequence.current;
        api
          .runningOperations()
          .then((running) => {
            if (mine === sequence.current) setOps(running);
          })
          .catch(() => {});
      }),
    [],
  );
  useEffect(() => () => refresh.cancel(), [refresh]);

  useEffect(() => {
    if (!authenticated) {
      refresh.cancel();
      sequence.current++; // drop a response still in flight
      first.current = true;
      setOps([]);
      return;
    }
    if (first.current || change?.resync) {
      first.current = false;
      refresh.flush();
    } else {
      refresh.trigger();
    }
  }, [authenticated, change, refreshTick, refresh]);

  return ops;
}
