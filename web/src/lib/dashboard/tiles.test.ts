import { describe, expect, it } from 'vitest';
import type { HostSnapshot } from '../api/types';
import type { HostSample } from '../stores/host';
import { hostStats, hostTiles } from './tiles';

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

  it('container and volume tiles link to their inventory', () => {
    const tiles = hostTiles(host());
    expect(tiles.filter((t) => t.href).map((t) => [t.id, t.href])).toEqual([['containers', '/containers'], ['volumes', '/storage?tab=volumes']]);
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

describe('hostStats', () => {
  const now = new Date(2026, 9, 15, 12, 0, 0);
  const history: HostSample[] = [
    { t: now.getTime() - 20_000, cpu: 10, mem: 100, rx: 5 },
    { t: now.getTime() - 10_000, cpu: null, mem: 200, rx: 6 },
  ];
  const stat = (h: HostSnapshot, id: string) => hostStats(h, history, now).find((x) => x.id === id);

  it('six stats in README order, null sections omitted', () => {
    expect(hostStats(host(), history, now).map((x) => x.id)).toEqual(['cpu', 'memory', 'disk', 'network', 'containers', 'uptime']);
    const ids = hostStats(host({ containers: null, network: null, uptime: null }), history, now).map((x) => x.id);
    expect(ids).toEqual(['cpu', 'memory', 'disk']);
  });

  it('series come from history and are empty for disk, containers and uptime', () => {
    const h = host();
    expect(stat(h, 'cpu')?.series).toEqual([10, null]);
    expect(stat(h, 'memory')?.series).toEqual([100, 200]);
    expect(stat(h, 'network')?.series).toEqual([5, 6]);
    for (const id of ['disk', 'containers', 'uptime']) expect(stat(h, id)?.series).toEqual([]);
  });

  it('cpu and memory values, aux and sub', () => {
    const h = host({ memory: { used: 4 * 1024 ** 3, total: 16 * 1024 ** 3, swapUsed: 1024 ** 3, swapTotal: 2 * 1024 ** 3 } });
    expect(stat(h, 'cpu')).toMatchObject({ label: 'CPU', aux: '8 cores', value: '18.0%', sub: 'load 1.42 1.10 0.90', tone: 'accent' });
    expect(stat(h, 'memory')).toMatchObject({ aux: '16.0 GiB', value: '4.0 GiB', sub: 'swap 1.0 GiB / 2.0 GiB', tone: 'info' });
    expect(stat(host(), 'memory')?.sub).toBe('no swap');
    expect(stat(host({ uptime: null }), 'cpu')?.sub).toBeUndefined();
  });

  it('disk turns warning at 75 % used', () => {
    const disk = (free: number) => host({ disk: { path: '/srv/stacks', free, total: 100 } });
    expect(stat(disk(26), 'disk')).toMatchObject({ aux: '100 B', value: '26 B', sub: 'free · /srv/stacks 74% used', tone: 'accent' });
    expect(stat(disk(25), 'disk')).toMatchObject({ sub: 'free · /srv/stacks 75% used', tone: 'warning' });
  });

  it('disk tone follows the rounded percentage that is shown', () => {
    const disk = host({ disk: { path: '/srv/stacks', free: 254, total: 1000 } });
    expect(stat(disk, 'disk')).toMatchObject({ sub: 'free · /srv/stacks 75% used', tone: 'warning' });
  });

  it('a stale sample is dropped from the series', () => {
    const stale: HostSample = { t: now.getTime() - 31 * 10_000, cpu: 99, mem: 999, rx: 9 };
    const fresh: HostSample = { t: now.getTime() - 30 * 10_000, cpu: 11, mem: 111, rx: 7 };
    const stats = hostStats(host(), [stale, fresh, ...history], now);
    expect(stats.find((x) => x.id === 'cpu')?.series).toEqual([11, 10, null]);
    expect(stats.find((x) => x.id === 'memory')?.series).toEqual([111, 100, 200]);
  });

  it('network falls back to dashes', () => {
    expect(stat(host(), 'network')).toMatchObject({ aux: 'eth0', value: '2.0 KiB/s', sub: '↑ 1.0 KiB/s', tone: 'info' });
    const none = host({ network: { rxBytesPerSec: null, txBytesPerSec: null, interface: 'eth0' } });
    expect(stat(none, 'network')).toMatchObject({ value: '—', sub: '—' });
  });

  it('containers aux is danger with bad > 0 and the sub carries restarts', () => {
    expect(stat(host(), 'containers')).toMatchObject({ value: '8/10', sub: '3 restarts · 24h', href: '/containers', tone: 'accent' });
    expect(stat(host(), 'containers')?.aux).toBeUndefined();
    const sick = host({ containers: { total: 10, running: 7, exited: 2, bad: 1 }, restarts24h: null });
    expect(stat(sick, 'containers')).toMatchObject({ aux: '1 bad', auxTone: 'danger' });
    expect(stat(sick, 'containers')?.sub).toBeUndefined();
  });

  it('uptime sub is since <date>', () => {
    // 90061 s = 1 d 1 h 1 m 1 s before 15 Oct 12:00 local
    expect(stat(host(), 'uptime')).toMatchObject({ value: '1d 1h', sub: 'since 14 Oct 10:58' });
  });

  it('docker source says containers only on cpu and memory', () => {
    const h = host({ source: 'docker' });
    expect(stat(h, 'cpu')?.aux).toBe('containers only');
    expect(stat(h, 'memory')?.sub).toBe('containers only');
  });
});
