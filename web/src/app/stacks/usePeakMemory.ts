import { useEffect, useState } from 'react';
import { formatBytes } from '$lib/format';
import { useServer } from '../servers/ServerContext';

const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * A hint for the memory limit field: the highest memory sample the metrics store holds for this service over
 * the last 24 hours. Undefined while loading, when there is no history, or when the request fails (the hint
 * is a convenience, so a failure is silent). Samples are bucket averages, so this can sit below the true peak.
 */
export function usePeakMemoryHint(stackName: string, serviceName: string): string | undefined {
  const { api } = useServer();
  const [hint, setHint] = useState<string>();
  useEffect(() => {
    const controller = new AbortController();
    const to = new Date();
    const from = new Date(to.getTime() - DAY_MS);
    api
      .metricsHistory(stackName, { from: from.toISOString(), to: to.toISOString(), service: serviceName }, controller.signal)
      .then(({ points }) => {
        const peak = points.reduce((max, point) => (point.service === serviceName ? Math.max(max, point.mem) : max), 0);
        setHint(peak > 0 ? `Highest sample in the last 24h: ${formatBytes(peak)}.` : undefined);
      })
      .catch(() => {});
    return () => controller.abort();
  }, [api, stackName, serviceName]);
  return hint;
}
