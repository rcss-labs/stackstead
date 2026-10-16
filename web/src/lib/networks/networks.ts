import type { Readable } from 'svelte/store';
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
