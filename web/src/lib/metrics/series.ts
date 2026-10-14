import type { MetricPoint, Sample } from '../api/types';

export type Range = '1h' | '6h' | '24h' | '72h';

export interface SeriesPoint {
  /** unix milliseconds */
  t: number;
  cpu: number;
  mem: number;
  memLimit: number;
  netRx?: number;
  netTx?: number;
  blkRead?: number;
  blkWrite?: number;
}

/** service name -> points sorted by t ascending */
export type SeriesSet = Map<string, SeriesPoint[]>;

const RANGES: Record<Range, { ms: number; step: string; stepSec: number }> = {
  '1h': { ms: 3_600_000, step: '5s', stepSec: 5 },
  '6h': { ms: 6 * 3_600_000, step: '30s', stepSec: 30 },
  '24h': { ms: 24 * 3_600_000, step: '2m', stepSec: 120 },
  '72h': { ms: 72 * 3_600_000, step: '6m', stepSec: 360 },
};

const wholeSecondsUTC = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z');

/**
 * Query for the history endpoint. `to` is aligned down to a multiple of the
 * step so the last bucket is complete (no rate dip at the live seam) and the
 * point count is at most range/step <= 720.
 */
export function rangeToQuery(range: Range, now: Date): { from: string; to: string; step: string } {
  const { ms, step, stepSec } = RANGES[range];
  const toMs = Math.floor(now.getTime() / 1000 / stepSec) * stepSec * 1000;
  return { from: wholeSecondsUTC(toMs - ms), to: wholeSecondsUTC(toMs), step };
}

export function historyToSeries(points: MetricPoint[]): SeriesSet {
  const set: SeriesSet = new Map();
  for (const m of points) {
    const p: SeriesPoint = { t: Date.parse(m.time), cpu: m.cpu, mem: m.mem, memLimit: m.memLimit };
    if (m.netRxBytesPerSec !== undefined) p.netRx = m.netRxBytesPerSec;
    if (m.netTxBytesPerSec !== undefined) p.netTx = m.netTxBytesPerSec;
    if (m.blkReadBytesPerSec !== undefined) p.blkRead = m.blkReadBytesPerSec;
    if (m.blkWriteBytesPerSec !== undefined) p.blkWrite = m.blkWriteBytesPerSec;
    const list = set.get(m.service);
    if (list) list.push(p);
    else set.set(m.service, [p]);
  }
  for (const list of set.values()) list.sort((a, b) => a.t - b.t);
  return set;
}

const COUNTERS = ['netRx', 'netTx', 'blkRead', 'blkWrite'] as const;
type Counter = (typeof COUNTERS)[number];
type Counters = Record<Counter, number>;

interface Prev {
  t: number;
  counters: Counters;
}

/**
 * Turns raw per-container samples into per-service points exactly as the
 * server's history does: per service per tick, gauges (cpu, mem, memLimit) are
 * averaged across containers and each cumulative counter is the MAX across
 * containers; a rate is then taken between consecutive ticks of the service,
 * and is 0 when the counter did not increase (restart).
 *
 * The live stream sends one event per container, so the current tick is held
 * back until a sample at least 1s later arrives (or flush() is called). Points
 * are therefore emitted one tick late.
 */
export function createLiveAggregator() {
  const prev = new Map<string, Prev>(); // by service
  let pendingT = 0;
  let pending = new Map<string, Sample>(); // by container

  function finalize(): SeriesPoint[] {
    if (pending.size === 0) return [];
    const byService = new Map<string, Sample[]>();
    for (const s of pending.values()) {
      const list = byService.get(s.service);
      if (list) list.push(s);
      else byService.set(s.service, [s]);
    }
    const out: SeriesPoint[] = [];
    for (const [service, list] of byService) {
      const n = list.length;
      const p: SeriesPoint = {
        t: pendingT,
        cpu: list.reduce((a, s) => a + s.cpu, 0) / n,
        mem: list.reduce((a, s) => a + s.mem, 0) / n,
        memLimit: list.reduce((a, s) => a + s.memLimit, 0) / n,
      };
      const counters = {} as Counters;
      for (const k of COUNTERS) counters[k] = Math.max(...list.map((s) => s[k]));
      const before = prev.get(service);
      const dt = before ? (pendingT - before.t) / 1000 : 0;
      if (before && dt > 0) {
        for (const k of COUNTERS) {
          p[k] = counters[k] > before.counters[k] ? (counters[k] - before.counters[k]) / dt : 0;
        }
      }
      if (!before || dt > 0) prev.set(service, { t: pendingT, counters });
      out.push(p);
    }
    pending = new Map();
    return out;
  }

  function ingest(samples: Sample[]): SeriesPoint[] {
    const timed = samples
      .map((s) => ({ s, t: Date.parse(s.time) }))
      .filter((x) => Number.isFinite(x.t))
      .sort((a, b) => a.t - b.t);
    const out: SeriesPoint[] = [];
    for (const { s, t } of timed) {
      if (pending.size > 0 && t - pendingT >= 1000) out.push(...finalize());
      if (pending.size === 0) pendingT = t;
      pending.set(s.container, s);
    }
    return out;
  }

  return {
    ingest,
    /** Emit the pending tick (e.g. when the stream ends). */
    flush: finalize,
    reset() {
      prev.clear();
      pending = new Map();
    },
  };
}

/**
 * Appends a live point, ignoring it unless newer than the last one, and drops
 * points older than windowMs behind the newest point (the stream's own clock,
 * so a skewed browser clock cannot wipe the chart).
 */
export function appendLive(set: SeriesSet, service: string, p: SeriesPoint, windowMs: number): void {
  const list = set.get(service) ?? [];
  if (list.length > 0 && p.t <= list[list.length - 1].t) return;
  list.push(p);
  const cutoff = p.t - windowMs;
  let drop = 0;
  while (drop < list.length && list[drop].t < cutoff) drop++;
  if (drop > 0) list.splice(0, drop);
  set.set(service, list);
}

/** History plus live; per service, live points not after the last history point are dropped. */
export function joinHistoryAndLive(history: SeriesSet, live: SeriesSet): SeriesSet {
  const joined: SeriesSet = new Map();
  for (const [service, points] of history) joined.set(service, [...points]);
  for (const [service, points] of live) {
    const base = joined.get(service) ?? [];
    const lastT = base.length > 0 ? base[base.length - 1].t : -Infinity;
    const fresh = points.filter((p) => p.t > lastT);
    joined.set(service, base.concat(fresh));
  }
  return joined;
}

/**
 * uPlot layout [xs, ys...]: xs are unix MILLISECONDS (configure uPlot with
 * `ms: 1`, or divide by 1000), one y array per requested service, null where
 * a service has no value at a timestamp.
 */
export function toUplotData(
  set: SeriesSet,
  metric: keyof SeriesPoint,
  services: string[],
): [number[], ...(number | null)[][]] {
  const stamps = new Set<number>();
  for (const s of services) for (const p of set.get(s) ?? []) stamps.add(p.t);
  const xs = [...stamps].sort((a, b) => a - b);
  const ys = services.map((s) => {
    const byT = new Map<number, number | undefined>();
    for (const p of set.get(s) ?? []) byT.set(p.t, p[metric]);
    return xs.map((t) => byT.get(t) ?? null);
  });
  return [xs, ...ys];
}
