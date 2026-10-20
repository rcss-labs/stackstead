import { useRef, useState } from 'react';
import { api, ApiError } from '$lib/api/client';
import type { ContainerRow } from '$lib/api/types';
import { BUSY_REASON } from '$lib/stacks/lifecycle';
import { live, refreshStatus } from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { useStore } from '../hooks/useStore';

export type ContainerAction = 'start' | 'stop' | 'restart';

export interface ConfirmingAction {
  row: ContainerRow;
  action: ContainerAction;
}

export interface ContainerActionState {
  /** Starts at once; stop and restart ask first. A no-op while `reasonFor(row)` applies. */
  request: (row: ContainerRow, action: ContainerAction) => void;
  confirming: ConfirmingAction | undefined;
  confirm: () => void;
  cancel: () => void;
  /** Ids of containers with an action request in flight. */
  acting: ReadonlySet<string>;
  reasonFor: (row: ContainerRow) => string | undefined;
}

/** The lifecycle actions offered for a container, in display order. */
export const actionsFor = (row: ContainerRow): ContainerAction[] => (row.state === 'running' ? ['stop', 'restart'] : ['start', 'restart']);

const DONE_TEXT: Record<ContainerAction, string> = { start: 'started', stop: 'stopped', restart: 'restarted' };

export function useContainerAction(onChanged: (row: ContainerRow, fresh: ContainerRow | undefined) => void): ContainerActionState {
  const liveState = useStore(live);
  const [acting, setActing] = useState<ReadonlySet<string>>(new Set());
  const [confirming, setConfirming] = useState<ConfirmingAction | undefined>();
  // A ref answers a second click that lands before the state update re-renders.
  const inFlight = useRef(new Set<string>());

  const reasonFor = (row: ContainerRow) =>
    liveState.docker === 'unavailable' || liveState.events === 'disconnected' ? 'Docker is unreachable' : acting.has(row.id) ? BUSY_REASON : undefined;

  async function run(row: ContainerRow, action: ContainerAction) {
    if (inFlight.current.has(row.id)) return;
    inFlight.current.add(row.id);
    setActing(new Set(inFlight.current));
    try {
      const fresh = await api.containerAction(row.id, action);
      toasts.push({ kind: 'success', message: `${row.name} ${DONE_TEXT[action]}` });
      onChanged(row, fresh);
    } catch (e) {
      if (e instanceof ApiError && e.status === 503) void refreshStatus();
      toasts.push({ kind: e instanceof ApiError && e.status === 409 ? 'warning' : 'danger', message: e instanceof Error ? e.message : String(e) });
    } finally {
      inFlight.current.delete(row.id);
      setActing(new Set(inFlight.current));
    }
  }

  function request(row: ContainerRow, action: ContainerAction) {
    // A stack-managed container is changed through its stack; the API answers 409 for a direct action.
    if (reasonFor(row) || row.managed === 'stack') return;
    if (action === 'start') void run(row, action);
    else setConfirming({ row, action });
  }

  function confirm() {
    if (!confirming) return;
    const { row, action } = confirming;
    setConfirming(undefined);
    void run(row, action);
  }

  return { request, confirming, confirm, cancel: () => setConfirming(undefined), acting, reasonFor };
}
