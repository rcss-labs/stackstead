import { useEffect, useState } from 'react';
import type { Alert, MetricsSummary, StorageOverview } from '$lib/api/types';
import { createDashboard } from '$lib/dashboard/controller';
import type { DashboardErrors } from '$lib/dashboard/controller';
import { useServer } from '../servers/ServerContext';
import { useStore } from './useStore';

export interface DashboardData {
  alerts: Alert[] | undefined;
  summary: MetricsSummary | undefined;
  storage: StorageOverview | undefined;
  errors: DashboardErrors;
  /** Fetches alerts at once; for use after a lifecycle action. */
  refreshAlerts: () => void;
}

/** One dashboard controller per mount: its timers run while the screen is mounted and stop with it. */
export function useDashboard(): DashboardData {
  const { api, changes } = useServer();
  const [dashboard] = useState(() =>
    createDashboard({ alerts: api.alerts, summary: api.metricsSummary, storage: api.storageOverview, changes }),
  );
  useEffect(() => {
    dashboard.start();
    return () => dashboard.stop();
  }, [dashboard]);

  return {
    alerts: useStore(dashboard.alerts),
    summary: useStore(dashboard.summary),
    storage: useStore(dashboard.storage),
    errors: useStore(dashboard.errors),
    refreshAlerts: dashboard.refreshAlerts,
  };
}
