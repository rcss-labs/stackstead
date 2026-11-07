import type { LiveState } from '$lib/stores/live';

/**
 * Plain text for a support request. It is built from named fields only (version and the live
 * status signals), never from a whole API payload, so nothing secret can ride along.
 */
export function buildDiagnostics(version: string, live: LiveState): string {
  const metrics = live.metricsDegraded ? 'degraded' : live.metricsIntervalSeconds ? `ok, every ${live.metricsIntervalSeconds} s` : 'ok';
  const docker = live.docker === 'ok' && live.dockerVersion ? `ok, ${live.dockerVersion}` : live.docker;
  return [
    `Stackstead ${version || 'unknown version'}`,
    `Docker: ${docker}`,
    `Compose: ${live.compose}`,
    `Events: ${live.events}`,
    `Metrics: ${metrics}`,
    ...(live.arch ? [`Architecture: ${live.arch}`] : []),
  ].join('\n');
}
