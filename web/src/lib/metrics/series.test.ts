import { describe, expect, it } from 'vitest';
import type { MetricPoint, Sample } from '../api/types';
import {
  appendLive,
  createLiveAggregator,
  historyToSeries,
  joinHistoryAndLive,
  rangeToQuery,
  toUplotData,
  type SeriesPoint,
  type SeriesSet,
} from './series';

const T0 = Date.parse('2026-10-14T10:00:00Z');
const iso = (offsetSec: number) => new Date(T0 + offsetSec * 1000).toISOString().replace('.000Z', 'Z');

function sample(over: Partial<Sample> & { container: string }, sec = 0): Sample {
  return {
    time: iso(sec),
    service: 'web',
    cpu: 0,
    mem: 0,
    memLimit: 0,
    netRx: 0,
    netTx: 0,
    blkRead: 0,
    blkWrite: 0,
    ...over,
  };
}

const pt = (t: number, cpu = 1): SeriesPoint => ({ t, cpu, mem: 0, memLimit: 0 });

describe('rangeToQuery', () => {
  const now = new Date('2026-10-14T10:00:00.789Z');
  it.each([
    ['1h', '2026-10-14T09:00:00Z', '5s'],
    ['6h', '2026-10-14T04:00:00Z', '30s'],
    ['24h', '2026-10-13T10:00:00Z', '2m'],
    ['72h', '2026-10-11T10:00:00Z', '6m'],
  ] as const)('%s', (range, from, step) => {
    expect(rangeToQuery(range, now)).toEqual({ from, to: '2026-10-14T10:00:00Z', step });
  });
});

describe('historyToSeries', () => {
  it('groups by service, sorts by time and maps counter names', () => {
    const points: MetricPoint[] = [
      { time: iso(10), service: 'web', cpu: 2, mem: 20, memLimit: 100, netRxBytesPerSec: 5, blkWriteBytesPerSec: 7 },
      { time: iso(0), service: 'web', cpu: 1, mem: 10, memLimit: 100 },
      { time: iso(0), service: 'db', cpu: 3, mem: 30, memLimit: 200 },
    ];
    const set = historyToSeries(points);
    expect([...set.keys()].sort()).toEqual(['db', 'web']);
    const web = set.get('web')!;
    expect(web.map((p) => p.t)).toEqual([T0, T0 + 10_000]);
    expect(web[0].netRx).toBeUndefined();
    expect(web[1]).toMatchObject({ cpu: 2, mem: 20, memLimit: 100, netRx: 5, blkWrite: 7 });
    expect(web[1].netTx).toBeUndefined();
  });
});

describe('createLiveAggregator', () => {
  it('has no rate for the first sample of a container', () => {
    const agg = createLiveAggregator();
    const [p] = agg.ingest([sample({ container: 'a', cpu: 4, mem: 8, memLimit: 16, netRx: 1000 })]);
    expect(p).toMatchObject({ t: T0, cpu: 4, mem: 8, memLimit: 16 });
    expect(p.netRx).toBeUndefined();
  });

  it('TestCounterResetGivesZeroRate', () => {
    const agg = createLiveAggregator();
    agg.ingest([sample({ container: 'a', netRx: 5000, blkRead: 900 }, 0)]);
    const [p] = agg.ingest([sample({ container: 'a', netRx: 100, blkRead: 100 }, 2)]);
    expect(p.netRx).toBe(0);
    expect(p.blkRead).toBe(0);
  });

  it('computes per-second rates from consecutive samples', () => {
    const agg = createLiveAggregator();
    agg.ingest([sample({ container: 'a', netRx: 1000, netTx: 0, blkWrite: 10 }, 0)]);
    const [p] = agg.ingest([sample({ container: 'a', netRx: 3000, netTx: 500, blkWrite: 10 }, 2)]);
    expect(p).toMatchObject({ netRx: 1000, netTx: 250, blkWrite: 0 });
  });

  it('TestReplicaAggregationMatchesServer', () => {
    const agg = createLiveAggregator();
    agg.ingest([
      sample({ container: 'a', cpu: 10, mem: 100, memLimit: 500, netRx: 0 }, 0),
      sample({ container: 'b', cpu: 30, mem: 300, memLimit: 800, netRx: 0 }, 0),
    ]);
    const out = agg.ingest([
      sample({ container: 'a', cpu: 10, mem: 100, memLimit: 500, netRx: 200 }, 1),
      sample({ container: 'b', cpu: 30, mem: 300, memLimit: 800, netRx: 900 }, 1),
    ]);
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ t: T0 + 1000, cpu: 20, mem: 200, memLimit: 800, netRx: 900 });
  });

  it('ignores a restarted replica for the max when it resets, keeps others', () => {
    const agg = createLiveAggregator();
    agg.ingest([
      sample({ container: 'a', netRx: 10_000 }, 0),
      sample({ container: 'b', netRx: 0 }, 0),
    ]);
    const [p] = agg.ingest([sample({ container: 'a', netRx: 5 }, 1), sample({ container: 'b', netRx: 40 }, 1)]);
    expect(p.netRx).toBe(40);
  });

  it('emits one point per service and joins samples less than 1s apart into one tick', () => {
    const agg = createLiveAggregator();
    const out = agg.ingest([
      sample({ container: 'a', service: 'web' }, 0),
      sample({ container: 'b', service: 'db' }, 0),
      { ...sample({ container: 'c', service: 'web' }, 0), time: '2026-10-14T10:00:00.4Z' },
    ]);
    expect(out).toHaveLength(2);
  });

  it('reset forgets previous counters', () => {
    const agg = createLiveAggregator();
    agg.ingest([sample({ container: 'a', netRx: 0 }, 0)]);
    agg.reset();
    const [p] = agg.ingest([sample({ container: 'a', netRx: 9000 }, 1)]);
    expect(p.netRx).toBeUndefined();
  });
});

describe('appendLive', () => {
  it('trims points older than the window and ignores out-of-order points', () => {
    const set: SeriesSet = new Map();
    appendLive(set, 'web', pt(1000), 5000);
    appendLive(set, 'web', pt(3000), 5000);
    appendLive(set, 'web', pt(3000), 5000);
    appendLive(set, 'web', pt(2000), 5000);
    expect(set.get('web')!.map((p) => p.t)).toEqual([1000, 3000]);
    appendLive(set, 'web', pt(7000), 5000);
    expect(set.get('web')!.map((p) => p.t)).toEqual([3000, 7000]);
  });
});

describe('joinHistoryAndLive', () => {
  it('TestJoinDropsOverlap', () => {
    const history: SeriesSet = new Map([['web', [pt(1000), pt(2000)]]]);
    const live: SeriesSet = new Map([
      ['web', [pt(1500, 9), pt(2000, 9), pt(3000, 9)]],
      ['new', [pt(500, 4)]],
    ]);
    const joined = joinHistoryAndLive(history, live);
    expect(joined.get('web')!.map((p) => [p.t, p.cpu])).toEqual([
      [1000, 1],
      [2000, 1],
      [3000, 9],
    ]);
    expect(joined.get('new')!.map((p) => p.t)).toEqual([500]);
    expect(history.get('web')).toHaveLength(2);
  });
});

describe('toUplotData', () => {
  it('aligns on the union of timestamps with null gaps', () => {
    const set: SeriesSet = new Map([
      ['a', [pt(1000, 1), pt(3000, 3)]],
      ['b', [pt(2000, 20), pt(3000, 30)]],
    ]);
    expect(toUplotData(set, 'cpu', ['a', 'b'])).toEqual([
      [1000, 2000, 3000],
      [1, null, 3],
      [null, 20, 30],
    ]);
  });

  it('gives null for an optional metric that is absent and for unknown services', () => {
    const set: SeriesSet = new Map([['a', [pt(1000), { ...pt(2000), netRx: 7 }]]]);
    expect(toUplotData(set, 'netRx', ['a', 'zzz'])).toEqual([[1000, 2000], [null, 7], [null, null]]);
  });
});
