import type { Readable } from '$lib/store';
import type { HostInterface, Network, NetworkDriver } from '../api/types';
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
  if (n.builtin) return 'Built-in networks cannot be removed';
  if (n.stack) return `Owned by stack ${n.stack}`;
  const count = n.containers.length;
  if (count) return `In use by ${count} container${count === 1 ? '' : 's'}`;
  return undefined;
}

export function sortNetworks(rows: Network[]): Network[] {
  return [...rows].sort((a, b) => a.name.localeCompare(b.name));
}

export interface NetworkSummary {
  total: number;
  stacks: number;
  lan: number;
  builtin: number;
}

export function networkSummary(rows: Network[]): NetworkSummary {
  return {
    total: rows.length,
    stacks: rows.filter((n) => n.stack).length,
    lan: rows.filter((n) => n.driver === 'macvlan' || n.driver === 'ipvlan').length,
    builtin: rows.filter((n) => n.builtin).length,
  };
}

/** The lead sentence's parts: the total always, the others only when present. */
export function summaryParts(summary: NetworkSummary): string[] {
  const parts = [`${summary.total} network${summary.total === 1 ? '' : 's'}`];
  if (summary.stacks > 0) parts.push(`${summary.stacks} owned by stacks`);
  if (summary.lan > 0) parts.push(`${summary.lan} LAN network${summary.lan === 1 ? '' : 's'}`);
  if (summary.builtin > 0) parts.push(`${summary.builtin} built-in`);
  return parts;
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
