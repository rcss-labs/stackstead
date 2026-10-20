import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { api } from '$lib/api/client';
import { createStackDetail, type StackDetailController } from '$lib/stacks/detail';
import { changes, refreshStatus } from '$lib/stores/live';
import { trackOperation } from '$lib/stores/operationTracker';
import { operationsRefresh, refreshRunningOps, refreshStacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { useStore } from '../hooks/useStore';

/**
 * The page's controller, created once per mounted screen (the route is keyed on the name) and started and
 * stopped by one effect. Every store is read through useStore; nothing is mirrored into React state.
 */
export function useStackDetail(name: string) {
  const navigate = useNavigate();
  // The controller outlives renders; reading the latest navigate through a ref keeps it from holding a stale one.
  const navigateRef = useRef(navigate);
  navigateRef.current = navigate;

  const [ctl] = useState<StackDetailController>(() =>
    createStackDetail(name, {
      stack: api.stack,
      operations: api.stackOperations,
      health: api.healthHistory,
      compose: api.compose,
      metricsHistory: api.metricsHistory,
      act: api.act,
      remove: api.remove,
      cancel: api.cancel,
      removeService: api.removeService,
      changes,
      operationsRefresh,
      refreshStacks,
      refreshRunningOps,
      refreshStatus,
      track: trackOperation,
      navigate: (href, opts) => navigateRef.current(href, opts),
      toasts,
    }),
  );

  useEffect(() => {
    ctl.start();
    return () => ctl.stop();
  }, [ctl]);

  return {
    ctl,
    detail: useStore(ctl.detail),
    notFound: useStore(ctl.notFound),
    loadError: useStore(ctl.loadError),
    operations: useStore(ctl.operations),
    health: useStore(ctl.health),
    compose: useStore(ctl.compose),
    composeError: useStore(ctl.composeError),
    cpuHour: useStore(ctl.cpuHour),
    acting: useStore(ctl.acting),
    removing: useStore(ctl.removing),
  };
}
