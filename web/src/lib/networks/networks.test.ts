import { get, writable } from '$lib/store';
import { describe, expect, it, vi } from 'vitest';
import type { HostInterface, Network } from '../api/types';
import type { LiveChange } from '../stores/live';
import { autofill, createNetworkList, needsParent, networkOwner, networkSummary, removeDisabledReason, sortNetworks, summaryParts } from './networks';

function network(over: Partial<Network> = {}): Network {
  return { id: 'n', name: 'lan', driver: 'bridge', scope: 'local', subnets: [], gateways: [], ipRange: '', parent: '', ipvlanMode: '', internal: false, builtin: false, project: '', stack: '', containers: [], ...over };
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
  [{ builtin: true, stack: 'shop', containers: [{ name: 'web', ipv4: '' }] }, 'Built-in networks cannot be removed'],
  [{ stack: 'shop', containers: [{ name: 'web', ipv4: '' }] }, 'Owned by stack shop'],
  [{ project: 'legacy' }, undefined],
  [{ project: 'legacy', containers: [{ name: 'web', ipv4: '' }] }, 'In use by 1 container'],
  [{ containers: [{ name: 'web', ipv4: '' }, { name: 'db', ipv4: '' }] }, 'In use by 2 containers'],
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

describe('networkSummary', () => {
  it('counts stacks, LAN drivers and built-ins', () => {
    const rows = [network({ name: 'z' }), network({ name: 'd', project: 'legacy' }), network({ name: 'c', stack: 'my stack' }), network({ name: 'bridge', builtin: true, driver: 'macvlan' })];
    const summary = networkSummary(rows);
    expect(summary).toEqual({ total: 4, stacks: 1, lan: 1, builtin: 1 });
    expect(summaryParts(summary)).toEqual(['4 networks', '1 owned by stacks', '1 LAN network', '1 built-in']);
  });

  it('uses singular forms and omits empty parts', () => {
    expect(summaryParts(networkSummary([network()]))).toEqual(['1 network']);
    expect(summaryParts(networkSummary([network({ driver: 'ipvlan' }), network({ driver: 'ipvlan' })]))).toEqual(['2 networks', '2 LAN networks']);
    expect(summaryParts(networkSummary([]))).toEqual(['0 networks']);
  });
});
