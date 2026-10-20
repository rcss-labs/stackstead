// One host snapshot for the All stacks status panel, combined from every server that answered.
import type { HostSnapshot } from '../api/types';

export interface HostInput {
  name: string;
  /** Present when the server's /api/host answered. */
  host?: HostSnapshot;
  /** Why there is no snapshot ("unreachable", "unavailable"…). */
  reason?: string;
}

export interface CombinedHost {
  /** Undefined when no server answered. */
  host: HostSnapshot | undefined;
  /** One line per excluded server. */
  notes: string[];
}

const sum = (values: number[]) => values.reduce((total, v) => total + v, 0);

/**
 * CPU percent is the core-weighted average of the servers that report one, cores are summed;
 * memory and disk (used = total - free) are summed. Sections only some servers have cover just those servers.
 */
export function combineHosts(inputs: HostInput[]): CombinedHost {
  const notes = inputs.filter((i) => !i.host).map((i) => `${i.name} not included: ${i.reason ?? 'unavailable'}`);
  const hosts = inputs.flatMap((i) => (i.host ? [i.host] : []));
  if (hosts.length === 0) return { host: undefined, notes };

  const label = `${hosts.length} ${hosts.length === 1 ? 'server' : 'servers'}`;
  const cpus = hosts.flatMap((h) => (h.cpu ? [h.cpu] : []));
  const measured = cpus.filter((c): c is { percent: number; cores: number } => c.percent !== null);
  const measuredCores = sum(measured.map((c) => c.cores));
  const memories = hosts.flatMap((h) => (h.memory ? [h.memory] : []));
  const disks = hosts.flatMap((h) => (h.disk ? [h.disk] : []));

  const host: HostSnapshot = {
    hostname: label,
    os: '',
    source: hosts.every((h) => h.source === 'docker') ? 'docker' : 'proc',
    sampledAt: hosts.map((h) => h.sampledAt).sort().at(-1) ?? '',
    containers: null,
    cpu:
      cpus.length === 0
        ? null
        : {
            percent: measuredCores > 0 ? sum(measured.map((c) => c.percent * c.cores)) / measuredCores : null,
            cores: sum(cpus.map((c) => c.cores)),
          },
    memory:
      memories.length === 0
        ? null
        : {
            used: sum(memories.map((m) => m.used)),
            total: sum(memories.map((m) => m.total)),
            swapUsed: sum(memories.map((m) => m.swapUsed)),
            swapTotal: sum(memories.map((m) => m.swapTotal)),
          },
    disk: disks.length === 0 ? null : { path: label, free: sum(disks.map((d) => d.free)), total: sum(disks.map((d) => d.total)) },
    network: null,
    diskIO: null,
    volumes: null,
    uptime: null,
    restarts24h: null,
  };
  return { host, notes };
}
