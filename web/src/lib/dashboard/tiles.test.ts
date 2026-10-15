import { describe, expect, it } from 'vitest';
import type { HostSnapshot } from '../api/types';
import { hostTiles } from './tiles';

const host = (over: Partial<HostSnapshot> = {}): HostSnapshot => ({
  hostname: 'box',
  os: 'linux',
  source: 'proc',
  sampledAt: '2026-10-15T10:00:00Z',
  containers: { total: 10, running: 8, exited: 2, bad: 0 },
  cpu: { percent: 18, cores: 8 },
  memory: { used: 4 * 1024 ** 3, total: 16 * 1024 ** 3, swapUsed: 0, swapTotal: 0 },
  disk: { path: '/srv/stacks', free: 400 * 1024 ** 3, total: 1000 * 1024 ** 3 },
  network: { rxBytesPerSec: 2048, txBytesPerSec: 1024, interface: 'eth0' },
  diskIO: { readBytesPerSec: 1024, writeBytesPerSec: 512 },
  volumes: { count: 14, size: 1024 ** 3, dangling: 0 },
  uptime: { seconds: 90061, load1: 1.42, load5: 1.1, load15: 0.9 },
  restarts24h: { total: 3, top: [{ container: 'loki', service: 'loki', project: 'mon', count: 2 }] },
  ...over,
});
const byId = (h: HostSnapshot, id: string) => hostTiles(h).find((t) => t.id === id);

describe('hostTiles', () => {
  it('null sections are omitted', () => {
    const ids = hostTiles(host({ containers: null, volumes: null, restarts24h: null })).map((t) => t.id);
    expect(ids).toEqual(['cpu', 'memory', 'disk', 'network', 'diskio', 'uptime']);
  });

  it('docker source shows containers only sub-line', () => {
    const h = host({ source: 'docker' });
    expect(byId(h, 'cpu')?.sub).toBe('containers only');
    expect(byId(h, 'memory')?.sub).toContain('containers only');
  });

  it('memory value is just used, with total and swap on the sub-line', () => {
    expect(byId(host(), 'memory')?.value).toBe('4.0 GiB');
    expect(byId(host(), 'memory')?.sub).toBe('of 16.0 GiB · no swap');
    const swap = { used: 4 * 1024 ** 3, total: 16 * 1024 ** 3, swapUsed: 1024 ** 3, swapTotal: 2 * 1024 ** 3 };
    expect(byId(host({ memory: swap }), 'memory')?.sub).toBe('of 16.0 GiB · swap 1.0 GiB/2.0 GiB');
    expect(byId(host({ source: 'docker' }), 'memory')?.sub).toBe('of 16.0 GiB · containers only');
  });

  it('disk value is free space, sub-line carries the total', () => {
    expect(byId(host(), 'disk')?.value).toBe('400.0 GiB');
    expect(byId(host(), 'disk')?.sub).toBe('free of 1000.0 GiB');
  });

  it('rate pairs keep the value short: receive on top, transmit on the sub-line', () => {
    const h = host({ network: { rxBytesPerSec: 123.4 * 1024 ** 2, txBytesPerSec: 56.7 * 1024 ** 2, interface: 'eth0' } });
    expect(byId(h, 'network')?.value).toBe('123.4 MiB/s');
    expect(byId(h, 'network')?.sub).toBe('↑ 56.7 MiB/s · eth0');
  });

  it('docker disk I/O says containers only, idle rates are 0 B/s', () => {
    const h = host({ source: 'docker', diskIO: { readBytesPerSec: 0, writeBytesPerSec: 0 }, network: { rxBytesPerSec: 0, txBytesPerSec: 0, interface: 'containers' } });
    expect(byId(h, 'diskio')?.value).toBe('0 B/s');
    expect(byId(h, 'diskio')?.sub).toBe('w 0 B/s · containers only');
    expect(byId(host({ source: 'docker', diskIO: { readBytesPerSec: 0, writeBytesPerSec: 0 } }), 'diskio')?.sub).toContain('containers only');
    expect(byId(h, 'network')?.value).toBe('0 B/s');
    expect(byId(h, 'network')?.sub).toBe('↑ 0 B/s · containers');
  });

  it('restarts sub-line is a warning with counts', () => {
    const t = byId(host({ restarts24h: { total: 3, top: [{ container: 'loki', service: 'loki', project: 'mon', count: 2 }, { container: 'web', service: 'web', project: 'a', count: 1 }] } }), 'restarts')!;
    expect(t.sub).toBe('loki ×2, web ×1');
    expect(t.subTone).toBe('warning');
  });

  it('volumes value is the size, count and dangling go on the sub-line', () => {
    expect(byId(host(), 'volumes')?.value).toBe('1.0 GiB');
    expect(byId(host(), 'volumes')?.sub).toBe('14 volumes');
    const t = byId(host({ volumes: { count: 14, size: 1024 ** 3, dangling: 3 } }), 'volumes')!;
    expect(t.sub).toBe('14 · 3 dangling');
    expect(t.subTone).toBe('warning');
  });

  it('rates null render dash but tile stays', () => {
    const h = host({ network: { rxBytesPerSec: null, txBytesPerSec: null, interface: 'eth0' }, diskIO: { readBytesPerSec: null, writeBytesPerSec: null }, cpu: { percent: null, cores: 4 } });
    expect(byId(h, 'network')?.value).toBe('—');
    expect(byId(h, 'diskio')?.value).toBe('—');
    expect(byId(h, 'cpu')?.value).toBe('—');
    expect(byId(h, 'network')?.sub).toBe('eth0');
  });

  it('bad containers sub-line is danger', () => {
    const t = byId(host({ containers: { total: 10, running: 8, exited: 1, bad: 2 } }), 'containers')!;
    expect(t.value).toBe('8/10');
    expect(t.sub).toBe('2 bad');
    expect(t.subTone).toBe('danger');
    expect(byId(host(), 'containers')?.sub).toBeUndefined();
  });

  it('ratios clamp to 0..1', () => {
    expect(byId(host({ cpu: { percent: 340, cores: 2 } }), 'cpu')?.ratio).toBe(1);
    expect(byId(host({ cpu: { percent: -3, cores: 2 } }), 'cpu')?.ratio).toBe(0);
    expect(byId(host({ memory: { used: 5, total: 0, swapUsed: 0, swapTotal: 0 } }), 'memory')?.ratio).toBe(0);
    expect(byId(host(), 'disk')?.ratio).toBeCloseTo(0.6);
  });
});
