import { describe, expect, it } from 'vitest';
import type { ContainerRow, StackSummary } from '../api/types';
import { nextFreePort, portRows } from './ports';

const stack = (name: string, ports: string[], status: StackSummary['status'] = 'running'): StackSummary =>
  ({ name, path: '', project: name, origin: 'managed', status, serviceCount: 1, running: 1, health: {}, ports, libraryUpdates: 0 }) as unknown as StackSummary;

const ctr = (name: string, over: Partial<ContainerRow> = {}): ContainerRow =>
  ({ id: name, name, image: 'x', imageId: '', state: 'running', status: '', health: 'none', exitCode: null, createdAt: '', project: '', service: '', stack: '', managed: 'none', ports: [], networks: {}, cpu: null, mem: null, memLimit: null, restarts24h: 0, ...over }) as ContainerRow;

describe('portRows', () => {
  it('port ranges expand and sort numerically', () => {
    const rows = portRows([stack('a', ['9000-9002', '80']), stack('b', ['8080'])], []);
    expect(rows.map((r) => [r.port, r.last])).toEqual([
      [80, 80],
      [8080, 8080],
      [9000, 9002],
    ]);
  });

  it('unmanaged containers are listed with no stack link', () => {
    const rows = portRows([], [ctr('redis', { ports: ['0.0.0.0:6379->6379/tcp'] })]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ port: 6379, container: 'redis', proto: 'tcp', bind: '0.0.0.0' });
    expect(rows[0].stack).toBeUndefined();
  });

  it('a running stack container replaces the declared port and adds its service', () => {
    const rows = portRows([stack('web', ['8080'])], [ctr('web-app-1', { stack: 'web', managed: 'stack', service: 'app', ports: ['0.0.0.0:8080->80/tcp'] })]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ stack: 'web', service: 'app', port: 8080 });
  });

  it('keeps a declared port nothing is publishing', () => {
    const rows = portRows([stack('web', ['8080'], 'stopped')], []);
    expect(rows[0]).toMatchObject({ stack: 'web', port: 8080, state: 'stopped' });
  });

  it('parses bracketed IPv6 binds', () => {
    const rows = portRows([], [ctr('x', { ports: ['[::1]:5000->5000/udp'] })]);
    expect(rows[0]).toMatchObject({ port: 5000, bind: '[::1]', proto: 'udp' });
  });
});

describe('nextFreePort', () => {
  it('nextFreePort skips used and range ports', () => {
    const rows = portRows([stack('a', ['8001-8003', '8005'])], []);
    expect(nextFreePort(rows, 8000)).toBe(8004);
    expect(nextFreePort(rows, 8005)).toBe(8006);
    expect(nextFreePort([], 8000)).toBe(8001);
  });
  it('returns 0 when nothing above is free', () => {
    expect(nextFreePort([], 65535)).toBe(0);
  });
});
