import { describe, expect, it } from 'vitest';
import type { HostSnapshot } from '$lib/api/types';
import { combineHosts } from './fleetHost';

const host = (over: Partial<HostSnapshot> = {}): HostSnapshot => ({
  hostname: 'h',
  os: 'linux',
  source: 'proc',
  sampledAt: '2026-10-20T10:00:00Z',
  containers: null,
  cpu: { percent: 10, cores: 4 },
  memory: { used: 2, total: 8, swapUsed: 0, swapTotal: 0 },
  disk: { path: '/', free: 60, total: 100 },
  network: null,
  diskIO: null,
  volumes: null,
  uptime: null,
  restarts24h: null,
  ...over,
});

describe('combineHosts', () => {
  it('weights CPU by cores and sums cores, memory and disk across servers', () => {
    const { host: combined, notes } = combineHosts([
      { name: 'ratsrv01', host: host({ cpu: { percent: 10, cores: 2 }, memory: { used: 2, total: 8, swapUsed: 0, swapTotal: 0 }, disk: { path: '/', free: 60, total: 100 } }) },
      { name: 'ratsrv02', host: host({ cpu: { percent: 40, cores: 6 }, memory: { used: 4, total: 16, swapUsed: 0, swapTotal: 0 }, disk: { path: '/', free: 100, total: 300 } }) },
    ]);
    expect(notes).toEqual([]);
    expect(combined?.hostname).toBe('2 servers');
    expect(combined?.cpu).toEqual({ percent: 32.5, cores: 8 });
    expect(combined?.memory).toMatchObject({ used: 6, total: 24 });
    expect(combined?.disk).toMatchObject({ total: 400, free: 160 });
  });

  it('leaves a server without a CPU percent out of the average but still counts its cores', () => {
    const { host: combined } = combineHosts([
      { name: 'a', host: host({ cpu: { percent: null, cores: 4 } }) },
      { name: 'b', host: host({ cpu: { percent: 50, cores: 4 } }) },
    ]);
    expect(combined?.cpu).toEqual({ percent: 50, cores: 8 });
  });

  it('keeps the percent null when no server reports one, and a section null when none has it', () => {
    const { host: combined } = combineHosts([
      { name: 'a', host: host({ cpu: { percent: null, cores: 2 }, memory: null }) },
      { name: 'b', host: host({ cpu: { percent: null, cores: 2 }, memory: null }) },
    ]);
    expect(combined?.cpu).toEqual({ percent: null, cores: 4 });
    expect(combined?.memory).toBeNull();
  });

  it('excludes a failed server and says why', () => {
    const { host: combined, notes } = combineHosts([
      { name: 'ratsrv01', host: host() },
      { name: 'ratsrv02', reason: 'unreachable' },
    ]);
    expect(notes).toEqual(['ratsrv02 not included: unreachable']);
    expect(combined?.hostname).toBe('1 server');
    expect(combined?.cpu).toEqual({ percent: 10, cores: 4 });
  });

  it('has no combined snapshot when nothing answered', () => {
    expect(combineHosts([{ name: 'a', reason: 'unreachable' }]).host).toBeUndefined();
  });
});
