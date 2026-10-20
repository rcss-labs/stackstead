import { useCallback, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { ApiError } from '$lib/api/client';
import type { Action, StackStatus } from '$lib/api/types';
import { lifecycleDisabledReason } from '$lib/stacks/lifecycle';
import { runningStackNames } from '$lib/stores/runningOps';
import { toasts } from '$lib/stores/toasts';
import { useStore } from '../hooks/useStore';
import { useServer } from '../servers/ServerContext';

export interface StackActions {
  /** Resolves true when the action started an operation; failures are toasted and resolve false. */
  act: (name: string, action: Action) => Promise<boolean>;
  /** Stacks with an action request in flight. */
  acting: ReadonlySet<string>;
  /** Why lifecycle actions are unavailable for a stack right now, or undefined when they are allowed. */
  reasonFor: (name: string, status: StackStatus | undefined) => string | undefined;
}

export function useStackAction(): StackActions {
  const navigate = useNavigate();
  const { api, live, refreshStatus, runningOps, trackOperation, refreshRunningOps, href } = useServer();
  const liveState = useStore(live);
  const running = useStore(runningOps);
  const [acting, setActing] = useState<ReadonlySet<string>>(new Set());
  // A ref answers a second click that lands before the state update re-renders.
  const inFlight = useRef(new Set<string>());

  const act = useCallback(
    async (name: string, action: Action) => {
      if (inFlight.current.has(name)) return false;
      inFlight.current.add(name);
      setActing(new Set(inFlight.current));
      try {
        const op = await api.act(name, action);
        trackOperation(op);
        navigate(href(`/stacks/${encodeURIComponent(name)}?tab=operations&op=${encodeURIComponent(op.id)}`));
        refreshRunningOps();
        return true;
      } catch (e) {
        const message = e instanceof Error ? e.message : String(e);
        if (e instanceof ApiError && e.status === 503) void refreshStatus();
        toasts.push({ kind: e instanceof ApiError && e.status === 409 ? 'warning' : 'danger', message: `Could not run ${action}: ${message}` });
        return false;
      } finally {
        inFlight.current.delete(name);
        setActing(new Set(inFlight.current));
      }
    },
    [navigate, api, trackOperation, refreshRunningOps, refreshStatus, href],
  );

  const busyNames = runningStackNames(running);
  const reasonFor = (name: string, status: StackStatus | undefined) =>
    lifecycleDisabledReason({ live: liveState, status, busy: busyNames.has(name) || acting.has(name) });

  return { act, acting, reasonFor };
}
