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
  const now = new Date('2026-10-14T10:03:47.789Z');
  it.each([
    ['1h', '2026-10-14T09:03:45Z', '2026-10-14T10:03:45Z', '5s'],
    ['6h', '2026-10-14T04:03:30Z', '2026-10-14T10:03:30Z', '30s'],
    ['24h', '2026-10-13T10:02:00Z', '2026-10-14T10:02:00Z', '2m'],
    ['72h', '2026-10-11T10:00:00Z', '2026-10-14T10:00:00Z', '6m'],
  ] as const)('%s aligns to when the step', (range, from, to, step) => {
    expect(rangeToQuery(range, now)).toEqual({ from, to, step });
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
  /** ingest each tick then a later sample so the pending tick is emitted */
  function run(agg: ReturnType<typeof createLiveAggregator>, ...ticks: Sample[][]): SeriesPoint[] {
    const out: SeriesPoint[] = [];
    for (const t of ticks) out.push(...agg.ingest(t));
    out.push(...agg.flush());
    return out;
  }

  it('has no rate for the first tick of a service', () => {
    const agg = createLiveAggregator();
    const [p] = run(agg, [sample({ container: 'a', cpu: 4, mem: 8, memLimit: 16, netRx: 1000 })]);
    expect(p).toMatchObject({ t: T0, cpu: 4, mem: 8, memLimit: 16 });
    expect(p.netRx).toBeUndefined();
  });

  it('TestCounterResetGivesZeroRate', () => {
    const agg = createLiveAggregator();
    const [, p] = run(
      agg,
      [sample({ container: 'a', netRx: 5000, blkRead: 900 }, 0)],
      [sample({ container: 'a', netRx: 100, blkRead: 100 }, 2)],
    );
    expect(p.netRx).toBe(0);
    expect(p.blkRead).toBe(0);
  });

  it('computes per-second rates between consecutive ticks', () => {
    const agg = createLiveAggregator();
    const [, p] = run(
      agg,
      [sample({ container: 'a', netRx: 1000, netTx: 0, blkWrite: 10 }, 0)],
      [sample({ container: 'a', netRx: 3000, netTx: 500, blkWrite: 10 }, 2)],
    );
    expect(p).toMatchObject({ netRx: 1000, netTx: 250, blkWrite: 0 });
  });

  it('TestReplicaAggregationMatchesServer', () => {
    const agg = createLiveAggregator();
    const out = run(
      agg,
      [
        sample({ container: 'a', cpu: 10, mem: 100, memLimit: 500, netRx: 0 }, 0),
        sample({ container: 'b', cpu: 30, mem: 300, memLimit: 800, netRx: 0 }, 0),
      ],
      [
        sample({ container: 'a', cpu: 10, mem: 100, memLimit: 500, netRx: 200 }, 1),
        sample({ container: 'b', cpu: 30, mem: 300, memLimit: 800, netRx: 900 }, 1),
      ],
    );
    expect(out).toHaveLength(2);
    // gauges (including memLimit) averaged; counter rates are the total across replicas (200 + 900)
    expect(out[1]).toMatchObject({ t: T0 + 1000, cpu: 20, mem: 200, memLimit: 650, netRx: 1100 });
  });

  it('two replicas at 1 MB/s each give 2 MB/s', () => {
    const MB = 1_000_000;
    const agg = createLiveAggregator();
    const [, p] = run(
      agg,
      [sample({ container: 'a', netTx: 0 }, 0), sample({ container: 'b', netTx: 5 * MB }, 0)],
      [sample({ container: 'a', netTx: MB }, 1), sample({ container: 'b', netTx: 6 * MB }, 1)],
    );
    expect(p.netTx).toBe(2 * MB);
  });

  it('a restarted replica contributes 0 while the other still counts', () => {
    const agg = createLiveAggregator();
    const [, p] = run(
      agg,
      [sample({ container: 'a', netRx: 10_000 }, 0), sample({ container: 'b', netRx: 0 }, 0)],
      [sample({ container: 'a', netRx: 5 }, 1), sample({ container: 'b', netRx: 40 }, 1)],
    );
    expect(p.netRx).toBe(40);
  });

  it('a new replica contributes nothing on its first tick', () => {
    const agg = createLiveAggregator();
    const [, p] = run(
      agg,
      [sample({ container: 'a', netRx: 0 }, 0)],
      [sample({ container: 'a', netRx: 100 }, 1), sample({ container: 'b', netRx: 999_999 }, 1)],
    );
    expect(p.netRx).toBe(100);
  });

  it('a service whose containers are all on their first tick has no rate', () => {
    const agg = createLiveAggregator();
    const [, p] = run(
      agg,
      [sample({ container: 'a', netRx: 0 }, 0)],
      [sample({ container: 'b', netRx: 500 }, 1), sample({ container: 'c', netRx: 500 }, 1)],
    );
    expect(p.netRx).toBeUndefined();
  });

  it('forgets containers not seen for more than 3 ticks', () => {
    const agg = createLiveAggregator();
    const out = run(
      agg,
      [sample({ container: 'a', netRx: 0 }, 0)],
      [sample({ container: 'z' }, 1)],
      [sample({ container: 'z' }, 2)],
      [sample({ container: 'z' }, 3)],
      [sample({ container: 'z' }, 4)],
      [sample({ container: 'a', netRx: 1000 }, 5)],
    );
    expect(out[out.length - 1].netRx).toBeUndefined();
  });

  it('merges per-container sample events of one tick into one point', () => {
    const agg = createLiveAggregator();
    const out = [
      ...agg.ingest([sample({ container: 'a', cpu: 10, netRx: 0 }, 0)]),
      ...agg.ingest([sample({ container: 'b', cpu: 30, netRx: 0 }, 0)]),
    ];
    expect(out).toEqual([]);
    const next = agg.ingest([sample({ container: 'a', cpu: 1 }, 1)]);
    expect(next).toHaveLength(1);
    expect(next[0]).toMatchObject({ t: T0, cpu: 20 });
    expect(agg.flush()).toHaveLength(1);
    expect(agg.flush()).toEqual([]);
  });

  it('emits one point per service per tick and joins samples less than 1s apart', () => {
    const agg = createLiveAggregator();
    const out = run(agg, [
      sample({ container: 'a', service: 'web' }, 0),
      sample({ container: 'b', service: 'db' }, 0),
      { ...sample({ container: 'c', service: 'web' }, 0), time: '2026-10-14T10:00:00.4Z' },
    ]);
    expect(out).toHaveLength(2);
  });

  it('a tick with no later time and dt <= 0 gives no rate and keeps previous state', () => {
    const agg = createLiveAggregator();
    const out = run(
      agg,
      [sample({ container: 'a', netRx: 0 }, 5)],
      [sample({ container: 'a', netRx: 500 }, 3)],
      [sample({ container: 'a', netRx: 1500 }, 6)],
    );
    expect(out[1].netRx).toBeUndefined();
    expect(out[2].netRx).toBe(1500);
  });

  it('reset forgets previous counters and the pending tick', () => {
    const agg = createLiveAggregator();
    agg.ingest([sample({ container: 'a', netRx: 0 }, 0)]);
    agg.reset();
    expect(agg.flush()).toEqual([]);
    const [p] = run(agg, [sample({ container: 'a', netRx: 9000 }, 1)]);
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
