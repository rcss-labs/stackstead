import { get, writable } from '$lib/store';
import { describe, expect, it, vi } from 'vitest';
import type { HostInterface, Network } from '../api/types';
import type { LiveChange } from '../stores/live';
import type { NetworkContainer } from '../api/types';
import {
  autofill,
  connectDisabledReason,
  containerStatus,
  createNetworkList,
  filterCounts,
  filterNetworks,
  groupByStack,
  needsParent,
  networkHealth,
  networkOwner,
  networkSummary,
  removeDisabledReason,
  sortNetworks,
  summaryParts,
  upstream,
} from './networks';

function network(over: Partial<Network> = {}): Network {
  return { id: 'n', fullId: 'n', name: 'lan', driver: 'bridge', scope: 'local', subnets: [], gateways: [], ipRange: '', parent: '', ipvlanMode: '', internal: false, attachable: false, ipv6: false, created: '', builtin: false, project: '', stack: '', containers: [], ...over };
}
function container(over: Partial<NetworkContainer> = {}): NetworkContainer {
  return { id: 'c', name: 'web', ipv4: '', mac: '', stack: '', state: 'running', health: '', exitCode: null, ...over };
}
const iface: HostInterface = { name: 'eth0', ipv4: '10.0.0.2', subnet: '10.0.0.0/24', gateway: '10.0.0.1', default: true };
const empty = { subnet: '', gateway: '' };

it.each([
  [{ builtin: true, stack: 'shop', project: 'compose' }, { kind: 'builtin' }],
  [{ stack: 'shop', project: 'compose' }, { kind: 'stack', stack: 'shop' }],
  [{ project: 'compose' }, { kind: 'compose', project: 'compose' }],
  [{}, { kind: 'unmanaged' }],
] as [Partial<Network>, unknown][])('identifies ownership in priority order: %j', (over, owner) => {
  expect(networkOwner(network(over))).toEqual(owner);
});

it.each([
  [{ builtin: true, stack: 'shop', containers: [container()] }, "System networks can't be removed"],
  [{ stack: 'shop', containers: [container()] }, 'Detach 1 container first'],
  [{ stack: 'shop' }, 'Owned by stack shop'],
  [{ project: 'legacy' }, undefined],
  [{ project: 'legacy', containers: [container()] }, 'Detach 1 container first'],
  [{ containers: [container(), container({ name: 'db' })] }, 'Detach 2 containers first'],
  [{}, undefined],
] as [Partial<Network>, string | undefined][])('explains removal availability: %j', (over, reason) => {
  expect(removeDisabledReason(network(over))).toBe(reason);
});

it('sorts networks by name without mutating the input', () => {
  const rows = [network({ name: 'z' }), network({ name: 'a' })];
  expect(sortNetworks(rows).map((n) => n.name)).toEqual(['a', 'z']);
  expect(rows.map((n) => n.name)).toEqual(['z', 'a']);
});

it.each([['bridge', false], ['macvlan', true], ['ipvlan', true]] as const)('parent requirement for %s', (driver, expected) => {
  expect(needsParent(driver)).toBe(expected);
});

it('fills empty fields from the interface', () => {
  expect(autofill(iface, empty, empty)).toEqual({ subnet: iface.subnet, gateway: iface.gateway });
});
it('keeps user edits independently for each field', () => {
  expect(autofill(iface, { subnet: 'custom', gateway: '' }, empty)).toEqual({ subnet: 'custom', gateway: iface.gateway });
  expect(autofill(iface, { subnet: '', gateway: 'custom' }, empty)).toEqual({ subnet: iface.subnet, gateway: 'custom' });
});
it('replaces fields that equal the last auto-filled values', () => {
  const previous = { subnet: '192.168.1.0/24', gateway: '192.168.1.1' };
  expect(autofill(iface, previous, previous)).toEqual({ subnet: iface.subnet, gateway: iface.gateway });
});
it('leaves the gateway alone when the interface has none', () => {
  const previous = { subnet: 'old', gateway: 'old-gateway' };
  expect(autofill({ ...iface, gateway: '' }, previous, previous)).toEqual({ subnet: iface.subnet, gateway: 'old-gateway' });
});
it('creates a network list keyed by id even if names change', async () => {
  const c = createNetworkList({ list: async () => [network()], changes: writable<LiveChange | null>(null) });
  c.start();
  await vi.waitFor(() => expect(get(c.rows)).toHaveLength(1));
  c.patch(network({ name: 'renamed' }));
  expect(get(c.rows)?.[0].name).toBe('renamed');
  c.stop();
});

it.each([
  [{ state: 'running' }, 'running'],
  [{ state: 'running', health: 'healthy' }, 'running'],
  [{ state: 'running', health: 'starting' }, 'starting'],
  [{ state: 'running', health: 'unhealthy' }, 'failed'],
  [{ state: 'restarting' }, 'starting'],
  [{ state: 'created' }, 'stopped'],
  [{ state: 'paused' }, 'stopped'],
  [{ state: 'exited', exitCode: 0 }, 'stopped'],
  [{ state: 'exited', exitCode: 137 }, 'failed'],
  [{ state: 'dead' }, 'failed'],
  [{ state: '' }, 'stopped'],
] as [Partial<NetworkContainer>, string][])('derives a container status from %j', (over, status) => {
  expect(containerStatus(container(over))).toBe(status);
});

it.each([
  [[], 'faint'],
  [[container(), container({ state: 'exited', exitCode: 1 }), container({ state: 'created' })], 'danger'],
  [[container(), container({ state: 'created' })], 'warning'],
  [[container(), container({ health: 'healthy' })], 'success'],
] as [NetworkContainer[], string][])('aggregates network health over its containers (%#)', (containers, tone) => {
  expect(networkHealth(network({ containers }))).toBe(tone);
});

describe('filters', () => {
  const rows = [
    network({ name: 'bridge', builtin: true, subnets: ['172.17.0.0/16'], containers: [container()] }),
    network({ name: 'none', builtin: true, driver: 'null' }),
    network({ name: 'shop_back', internal: true, subnets: ['10.9.0.0/24'] }),
    network({ name: 'media', subnets: ['192.168.40.0/24'], containers: [container()] }),
    network({ name: 'spare' }),
  ];
  const names = (filtered: Network[]) => filtered.map((n) => n.name);

  it.each([
    ['all', ['bridge', 'none', 'shop_back', 'media', 'spare']],
    ['active', ['bridge', 'media']],
    ['internal', ['shop_back']],
    ['unused', ['shop_back', 'spare']],
  ] as const)('the %s chip keeps matching networks', (filter, expected) => {
    expect(names(filterNetworks(rows, filter, ''))).toEqual(expected);
  });

  it('matches the query against name or subnet, case-insensitively, together with the chip', () => {
    expect(names(filterNetworks(rows, 'all', 'SHOP'))).toEqual(['shop_back']);
    expect(names(filterNetworks(rows, 'all', '192.168'))).toEqual(['media']);
    expect(names(filterNetworks(rows, 'active', '10.9'))).toEqual([]);
    expect(names(filterNetworks(rows, 'all', '  '))).toHaveLength(5);
  });

  it('counts each chip over the whole list', () => {
    expect(filterCounts(rows)).toEqual({ all: 5, active: 2, internal: 1, unused: 2 });
  });
});

it('summarises total and in-use networks', () => {
  expect(summaryParts(networkSummary([network({ containers: [container()] }), network()]))).toEqual(['2 networks', '1 in use']);
  expect(summaryParts(networkSummary([network()]))).toEqual(['1 network', '0 in use']);
});

it.each([
  [{}, undefined],
  [{ name: 'host', driver: 'host' }, "Containers can't join the host network at runtime"],
  [{ name: 'none', driver: 'null' }, "Containers can't join the none network at runtime"],
] as [Partial<Network>, string | undefined][])('explains connect availability: %j', (over, reason) => {
  expect(connectDisabledReason(network(over))).toBe(reason);
});

it.each([
  [{}, { label: 'host · eth0 · NAT', warning: false }],
  [{ internal: true }, { label: 'no external route · internal', warning: true }],
  [{ driver: 'host' }, { label: 'host network stack', warning: false }],
  [{ driver: 'null' }, { label: 'no interface · isolated', warning: false }],
  [{ driver: 'macvlan', parent: 'enp3s0' }, { label: 'enp3s0 · macvlan', warning: false }],
  [{ driver: 'overlay' }, { label: 'overlay', warning: false }],
] as [Partial<Network>, unknown][])('labels the upstream route of %j', (over, expected) => {
  expect(upstream(network(over))).toEqual(expected);
});

it('groups containers by stack, sorted, with standalone containers last', () => {
  const groups = groupByStack([container({ name: 'b', stack: 'shop' }), container({ name: 'x' }), container({ name: 'a', stack: 'shop' }), container({ name: 'm', stack: 'media' })]);
  expect(groups.map((g) => [g.stack, g.containers.map((c) => c.name)])).toEqual([
    ['media', ['m']],
    ['shop', ['a', 'b']],
    ['', ['x']],
  ]);
});
