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

const RANGES: Record<Range, { ms: number; step: string }> = {
  '1h': { ms: 3_600_000, step: '5s' },
  '6h': { ms: 6 * 3_600_000, step: '30s' },
  '24h': { ms: 24 * 3_600_000, step: '2m' },
  '72h': { ms: 72 * 3_600_000, step: '6m' },
};

const wholeSecondsUTC = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z');

/** Query for the history endpoint; step keeps range/step at or below 720 points. */
export function rangeToQuery(range: Range, now: Date): { from: string; to: string; step: string } {
  const { ms, step } = RANGES[range];
  const toMs = Math.floor(now.getTime() / 1000) * 1000;
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

type Counters = Pick<Sample, 'netRx' | 'netTx' | 'blkRead' | 'blkWrite'>;
const COUNTERS = ['netRx', 'netTx', 'blkRead', 'blkWrite'] as const;

interface Prev {
  t: number;
  counters: Counters;
}

/**
 * Turns raw per-container samples into per-service points the way the server
 * buckets history: gauges averaged across containers, counter rates computed
 * per container then MAX across replicas, a counter decrease (restart) -> 0.
 */
export function createLiveAggregator() {
  const prev = new Map<string, Prev>();

  function ingest(samples: Sample[]): SeriesPoint[] {
    const timed = samples
      .map((s) => ({ s, t: Date.parse(s.time) }))
      .filter((x) => Number.isFinite(x.t))
      .sort((a, b) => a.t - b.t);

    const out: SeriesPoint[] = [];
    let i = 0;
    while (i < timed.length) {
      const tickT = timed[i].t;
      let j = i;
      while (j < timed.length && timed[j].t - tickT < 1000) j++;
      out.push(...aggregateTick(tickT, timed.slice(i, j).map((x) => ({ s: x.s, t: x.t }))));
      i = j;
    }
    return out;
  }

  function aggregateTick(tickT: number, items: { s: Sample; t: number }[]): SeriesPoint[] {
    const byService = new Map<string, { n: number; p: SeriesPoint }>();
    for (const { s, t } of items) {
      let agg = byService.get(s.service);
      if (!agg) {
        agg = { n: 0, p: { t: tickT, cpu: 0, mem: 0, memLimit: 0 } };
        byService.set(s.service, agg);
      }
      agg.n++;
      agg.p.cpu += s.cpu;
      agg.p.mem += s.mem;
      agg.p.memLimit = Math.max(agg.p.memLimit, s.memLimit);

      const before = prev.get(s.container);
      const dt = before ? (t - before.t) / 1000 : 0;
      if (before && dt > 0) {
        for (const key of COUNTERS) {
          const delta = s[key] - before.counters[key];
          const rate = delta < 0 ? 0 : delta / dt;
          agg.p[key] = Math.max(agg.p[key] ?? 0, rate);
        }
      }
      if (!before || dt > 0) {
        prev.set(s.container, {
          t,
          counters: { netRx: s.netRx, netTx: s.netTx, blkRead: s.blkRead, blkWrite: s.blkWrite },
        });
      }
    }
    return [...byService.values()].map(({ n, p }) => ({ ...p, cpu: p.cpu / n, mem: p.mem / n }));
  }

  return { ingest, reset: () => prev.clear() };
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
