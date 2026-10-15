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
    expect(byId(h, 'memory')?.sub).toBe('containers only');
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
