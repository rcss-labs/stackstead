import type { ContainerInfo, ServiceDetail } from '$lib/api/types';

export interface ServiceCause {
  title: string;
  detail: string;
  /** Exit 137 on a service that has a memory limit: raising the limit is a sensible first step. */
  raiseLimit: boolean;
}

/** Docker prints the code in parentheses: "Exited (137) 3 minutes ago", "Restarting (1) 4 seconds ago". */
function exitCode(container: ContainerInfo): number | undefined {
  const match = /\((\d+)\)/.exec(container.status);
  return match ? Number(match[1]) : undefined;
}

/**
 * Why a service is failing, from what its containers report. Null while nothing is wrong, and for a clean
 * stop (exit 0), which is a state and not a failure.
 */
export function serviceCause(service: ServiceDetail): ServiceCause | null {
  const memory = service.config.limits.memory;
  for (const container of service.containers) {
    if (container.state === 'restarting') {
      return { title: 'Restarting repeatedly', detail: 'It keeps stopping. Check the logs for the error.', raiseLimit: false };
    }
    if (container.state === 'exited') {
      const code = exitCode(container);
      if (code === undefined || code === 0) continue;
      if (code === 137) {
        return {
          title: 'Exited with code 137',
          detail: memory
            ? `Killed, often for exceeding its memory limit (${memory}). Raise the limit, or check the logs.`
            : 'The process was killed. Check the logs.',
          raiseLimit: !!memory,
        };
      }
      return { title: `Exited with code ${code}`, detail: 'Check the logs for the error.', raiseLimit: false };
    }
    if (container.state === 'dead') return { title: 'Container is dead', detail: 'Start the stack again to replace it.', raiseLimit: false };
    if (container.health === 'unhealthy') return { title: 'Healthcheck is failing', detail: 'It runs, but reports unhealthy. Check the logs.', raiseLimit: false };
  }
  return null;
}
