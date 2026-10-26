import type { Readable } from '$lib/store';
import type { HostInterface, Network, NetworkContainer, NetworkDriver } from '../api/types';
import type { LiveChange } from '../stores/live';
import { createRefreshList } from '../stores/refreshList';

export type NetworkOwner =
  | { kind: 'builtin' }
  | { kind: 'stack'; stack: string }
  | { kind: 'compose'; project: string }
  | { kind: 'unmanaged' };

export function networkOwner(n: Network): NetworkOwner {
  if (n.builtin) return { kind: 'builtin' };
  if (n.stack) return { kind: 'stack', stack: n.stack };
  if (n.project) return { kind: 'compose', project: n.project };
  return { kind: 'unmanaged' };
}

export function removeDisabledReason(n: Network): string | undefined {
  if (n.builtin) return "System networks can't be removed";
  const count = n.containers.length;
  if (count) return `Detach ${count} container${count === 1 ? '' : 's'} first`;
  // The server refuses these too: compose owns the network's lifecycle.
  if (n.stack) return `Owned by stack ${n.stack}`;
  return undefined;
}

export function connectDisabledReason(n: Network): string | undefined {
  if (n.driver === 'host' || n.driver === 'null') return `Containers can't join the ${n.name} network at runtime`;
  return undefined;
}

export type ContainerStatus = 'running' | 'starting' | 'stopped' | 'failed';

export function containerStatus(c: NetworkContainer): ContainerStatus {
  if (c.state === 'dead' || c.health === 'unhealthy') return 'failed';
  if (c.state === 'exited') return c.exitCode !== null && c.exitCode !== 0 ? 'failed' : 'stopped';
  if (c.state === 'restarting' || (c.state === 'running' && c.health === 'starting')) return 'starting';
  return c.state === 'running' ? 'running' : 'stopped';
}

export const STATUS_TONE: Record<ContainerStatus, 'success' | 'warning' | 'muted' | 'danger'> = {
  running: 'success',
  starting: 'warning',
  stopped: 'muted',
  failed: 'danger',
};

export function networkHealth(n: Network): 'faint' | 'danger' | 'warning' | 'success' {
  if (n.containers.length === 0) return 'faint';
  const statuses = n.containers.map(containerStatus);
  if (statuses.includes('failed')) return 'danger';
  if (statuses.some((s) => s !== 'running')) return 'warning';
  return 'success';
}

export type NetworkFilter = 'all' | 'active' | 'internal' | 'unused';

const FILTERS: Record<NetworkFilter, (n: Network) => boolean> = {
  all: () => true,
  active: (n) => n.containers.length > 0,
  internal: (n) => n.internal,
  unused: (n) => n.containers.length === 0 && !n.builtin,
};

export function filterNetworks(rows: Network[], filter: NetworkFilter, query: string): Network[] {
  const q = query.trim().toLowerCase();
  return rows.filter(
    (n) => FILTERS[filter](n) && (!q || n.name.toLowerCase().includes(q) || n.subnets.some((s) => s.toLowerCase().includes(q))),
  );
}

export function filterCounts(rows: Network[]): Record<NetworkFilter, number> {
  const count = (f: NetworkFilter) => rows.filter(FILTERS[f]).length;
  return { all: rows.length, active: count('active'), internal: count('internal'), unused: count('unused') };
}

/** The route out of the network, as the topology's top pill shows it. */
export function upstream(n: Network): { label: string; warning: boolean } {
  if (n.driver === 'host') return { label: 'host network stack', warning: false };
  if (n.driver === 'null') return { label: 'no interface · isolated', warning: false };
  if (n.internal) return { label: 'no external route · internal', warning: true };
  if (n.driver === 'bridge') return { label: 'host · eth0 · NAT', warning: false };
  return { label: n.parent ? `${n.parent} · ${n.driver}` : n.driver, warning: false };
}

export interface StackGroup {
  /** '' for containers outside any stack or compose project. */
  stack: string;
  containers: NetworkContainer[];
}

export function groupByStack(containers: NetworkContainer[]): StackGroup[] {
  const groups = new Map<string, NetworkContainer[]>();
  for (const c of containers) groups.set(c.stack, [...(groups.get(c.stack) ?? []), c]);
  return [...groups.entries()]
    .sort(([a], [b]) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b)))
    .map(([stack, list]) => ({ stack, containers: [...list].sort((a, b) => a.name.localeCompare(b.name)) }));
}

export function sortNetworks(rows: Network[]): Network[] {
  return [...rows].sort((a, b) => a.name.localeCompare(b.name));
}

/** Drivers that attach to a host interface; Network.driver stays a string because Docker also reports overlay, host and others. */
export function isLanDriver(driver: string): boolean {
  return driver === 'macvlan' || driver === 'ipvlan';
}

export interface NetworkSummary {
  total: number;
  inUse: number;
}

export function networkSummary(rows: Network[]): NetworkSummary {
  return { total: rows.length, inUse: rows.filter((n) => n.containers.length > 0).length };
}

export function summaryParts(summary: NetworkSummary): string[] {
  return [`${summary.total} network${summary.total === 1 ? '' : 's'}`, `${summary.inUse} in use`];
}

export function needsParent(driver: NetworkDriver): boolean {
  return driver === 'macvlan' || driver === 'ipvlan';
}

type AddressFields = { subnet: string; gateway: string };

export function autofill(iface: HostInterface, current: AddressFields, lastAuto: AddressFields): AddressFields {
  return {
    subnet: !current.subnet || current.subnet === lastAuto.subnet ? iface.subnet : current.subnet,
    gateway: iface.gateway && (!current.gateway || current.gateway === lastAuto.gateway) ? iface.gateway : current.gateway,
  };
}

export function createNetworkList(deps: {
  list: () => Promise<Network[]>;
  changes: Readable<LiveChange | null>;
  doc?: Document;
}) {
  return createRefreshList({ ...deps, key: (row) => row.id });
}

/** Identifies a container across the topology and the table. */
export function containerKey(c: NetworkContainer): string {
  return `${c.stack}/${c.name}`;
}
