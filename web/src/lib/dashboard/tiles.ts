import type { HostSnapshot } from '../api/types';
import { formatBytes, formatDuration, formatPercent, formatRate } from '../format';

export interface TileModel {
  id: string;
  label: string;
  value: string;
  sub?: string;
  subTone?: 'muted' | 'success' | 'warning' | 'danger';
  /** 0..1, draws the bar. */
  ratio?: number;
}

const clamp01 = (x: number) => (Number.isFinite(x) ? Math.min(1, Math.max(0, x)) : 0);
const rate = (v: number | null) => (v === null ? '—' : formatRate(v));
const CONTAINERS_ONLY = 'containers only';

/** One tile per non-null section, in the mock-up's order. */
export function hostTiles(h: HostSnapshot): TileModel[] {
  const tiles: TileModel[] = [];
  const docker = h.source === 'docker';

  if (h.containers) {
    const { running, total, bad } = h.containers;
    tiles.push({
      id: 'containers',
      label: 'Containers',
      value: `${running}/${total}`,
      ...(bad > 0 ? { sub: `${bad} bad`, subTone: 'danger' as const } : {}),
      ratio: total > 0 ? clamp01(running / total) : 0,
    });
  }
  if (h.cpu) {
    tiles.push({
      id: 'cpu',
      label: 'CPU',
      value: h.cpu.percent === null ? '—' : formatPercent(h.cpu.percent),
      sub: docker ? CONTAINERS_ONLY : `${h.cpu.cores} cores`,
      ratio: clamp01((h.cpu.percent ?? 0) / 100),
    });
  }
  if (h.memory) {
    const m = h.memory;
    const sub = [`of ${formatBytes(m.total)}`];
    if (docker) sub.push(CONTAINERS_ONLY);
    else sub.push(m.swapTotal > 0 ? `swap ${formatBytes(m.swapUsed)}/${formatBytes(m.swapTotal)}` : 'no swap');
    tiles.push({
      id: 'memory',
      label: 'Memory',
      value: formatBytes(m.used),
      sub: sub.join(' · '),
      ratio: m.total > 0 ? clamp01(m.used / m.total) : 0,
    });
  }
  if (h.disk) {
    tiles.push({
      id: 'disk',
      label: 'Disk',
      value: formatBytes(h.disk.free),
      sub: `free of ${formatBytes(h.disk.total)}`,
      ratio: h.disk.total > 0 ? clamp01((h.disk.total - h.disk.free) / h.disk.total) : 0,
    });
  }
  // A rate pair never fits the 17 px value ("0 B/s / 0 B/s" is already 13 characters):
  // the first rate is the value, the second leads the sub-line.
  if (h.network) {
    const { rxBytesPerSec: rx, txBytesPerSec: tx } = h.network;
    const bothNull = rx === null && tx === null;
    tiles.push({
      id: 'network',
      label: 'Network',
      value: bothNull ? '—' : rate(rx),
      sub: bothNull ? h.network.interface : `↑ ${rate(tx)} · ${h.network.interface}`,
    });
  }
  if (h.diskIO) {
    const { readBytesPerSec: r, writeBytesPerSec: w } = h.diskIO;
    const bothNull = r === null && w === null;
    const parts = bothNull ? [] : [`w ${rate(w)}`];
    if (docker) parts.push(CONTAINERS_ONLY);
    tiles.push({
      id: 'diskio',
      label: 'Disk I/O',
      value: bothNull ? '—' : rate(r),
      ...(parts.length > 0 ? { sub: parts.join(' · ') } : {}),
    });
  }
  if (h.volumes) {
    const v = h.volumes;
    tiles.push({
      id: 'volumes',
      label: 'Volumes',
      value: formatBytes(v.size),
      sub: v.dangling > 0 ? `${v.count} · ${v.dangling} dangling` : `${v.count} volumes`,
      ...(v.dangling > 0 ? { subTone: 'warning' as const } : {}),
    });
  }
  if (h.uptime) {
    const u = h.uptime;
    tiles.push({
      id: 'uptime',
      label: 'Uptime',
      value: formatDuration(u.seconds * 1000),
      sub: `load ${u.load1.toFixed(2)} ${u.load5.toFixed(2)} ${u.load15.toFixed(2)}`,
    });
  }
  if (h.restarts24h) {
    const r = h.restarts24h;
    tiles.push({
      id: 'restarts',
      label: 'Restarts · 24h',
      value: String(r.total),
      ...(r.top.length > 0 ? { sub: r.top.map((t) => `${t.container} ×${t.count}`).join(', '), subTone: 'warning' as const } : {}),
    });
  }
  return tiles;
}
