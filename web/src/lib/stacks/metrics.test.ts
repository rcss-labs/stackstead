import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MetricPoint, Sample } from '../api/types';
import type { SSEOptions } from '../sse/client';
import { createMetricsController, serviceColorIndex } from './metrics';

const NOW = new Date('2026-10-14T10:03:47Z');
const T0 = Date.parse('2026-10-14T10:00:00Z');
const iso = (sec: number) => new Date(T0 + sec * 1000).toISOString().replace('.000Z', 'Z');

const hp = (service: string, sec: number, cpu: number): MetricPoint => ({
  time: iso(sec),
  service,
  cpu,
  mem: 100,
  memLimit: 1000,
});

const sample = (service: string, container: string, sec: number, over: Partial<Sample> = {}): Sample => ({
  time: iso(sec),
  service,
  container,
  cpu: 10,
  mem: 100,
  memLimit: 1000,
  netRx: 0,
  netTx: 0,
  blkRead: 0,
  blkWrite: 0,
  ...over,
});

function harness(points: MetricPoint[] = []) {
  const opens: { opts: SSEOptions; close: ReturnType<typeof vi.fn> }[] = [];
  const history = vi.fn(async (_q: { from: string; to: string; step: string }, _s?: AbortSignal) => ({ points }));
  const open = vi.fn((opts: SSEOptions) => {
    const close = vi.fn();
    opens.push({ opts, close });
    return { close, status: 'open' as const };
  });
  const ctl = createMetricsController('web', { history, open, now: () => NOW });
  const send = (s: Sample | Sample[], i = opens.length - 1) =>
    opens[i].opts.onMessage({ event: 'sample', data: JSON.stringify(s) });
  return { ctl, history, open, opens, send };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('createMetricsController', () => {
  it('loads history, then opens the live stream and appends samples', async () => {
    const h = harness([hp('web', 0, 1), hp('web', 5, 2)]);
    await h.ctl.reload();
    expect(h.history.mock.calls[0][0]).toEqual({ from: '2026-10-14T09:03:45Z', to: '2026-10-14T10:03:45Z', step: '5s' });
    expect(h.opens[0].opts.url).toBe('/api/stacks/web/metrics/live');
    expect(get(h.ctl.series).get('web')).toHaveLength(2);

    h.send(sample('web', 'c1', 10, { cpu: 50 }));
    h.send(sample('web', 'c1', 12, { cpu: 60 })); // finalises the tick at +10s
    const pts = get(h.ctl.series).get('web')!;
    expect(pts.map((p) => p.cpu)).toEqual([1, 2, 50]);
  });

  it('flushes the pending tick after ~1s without new samples', async () => {
    const h = harness();
    await h.ctl.reload();
    h.send(sample('web', 'c1', 10, { cpu: 42 }));
    expect(get(h.ctl.series).get('web')).toBeUndefined();
    vi.advanceTimersByTime(1500);
    expect(get(h.ctl.series).get('web')?.[0].cpu).toBe(42);
  });

  it('range change refetches with the new step and aborts the previous request', async () => {
    const h = harness();
    let firstSignal: AbortSignal | undefined;
    h.history.mockImplementationOnce(async (_q, s) => {
      firstSignal = s;
      return new Promise(() => {});
    });
    void h.ctl.reload();
    h.ctl.range.set('24h');
    await vi.advanceTimersByTimeAsync(0);
    expect(firstSignal?.aborted).toBe(true);
    expect(h.history.mock.calls.at(-1)![0].step).toBe('2m');
  });

  it('ignores a history failure that was caused by abort, and flags degraded on real failures', async () => {
    const h = harness();
    h.history.mockRejectedValueOnce(new Error('boom'));
    await h.ctl.reload();
    expect(get(h.ctl.degraded)).toBe(true);
    await h.ctl.reload();
    expect(get(h.ctl.degraded)).toBe(false);
  });

  it('TestDroppedReconnectsWithoutDuplicates', async () => {
    const h = harness();
    await h.ctl.reload();
    h.send(sample('web', 'c1', 10, { netRx: 0 }));
    h.send(sample('web', 'c1', 15, { netRx: 1000 }));
    h.opens[0].opts.onMessage({ event: 'dropped', data: '' });
    expect(h.opens[0].close).toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1100);
    expect(h.opens).toHaveLength(2);
    // replayed sample is ignored; next sample still computes a rate from kept counters
    h.send(sample('web', 'c1', 15, { netRx: 1000 }));
    h.send(sample('web', 'c1', 20, { netRx: 3000 }));
    h.send(sample('web', 'c1', 25, { netRx: 3000 }));
    const pts = get(h.ctl.series).get('web')!;
    expect(pts.map((p) => p.t)).toEqual([T0 + 10_000, T0 + 15_000, T0 + 20_000]);
    expect(pts[2].netRx).toBe(400);
  });

  it('derives services from the union of history and live, sorted', async () => {
    const h = harness([hp('db', 0, 1)]);
    await h.ctl.reload();
    h.send(sample('api', 'c1', 10));
    h.send(sample('api', 'c1', 12));
    expect(get(h.ctl.services)).toEqual(['api', 'db']);
  });

  it('close() closes the stream, cancels timers and aborts requests', async () => {
    const h = harness();
    await h.ctl.reload();
    h.send(sample('web', 'c1', 10));
    h.ctl.close();
    expect(h.opens[0].close).toHaveBeenCalled();
    vi.advanceTimersByTime(5000);
    expect(get(h.ctl.series).get('web')).toBeUndefined();
  });

  it('mirrors the stream status', async () => {
    const h = harness();
    await h.ctl.reload();
    h.opens[0].opts.onStatus?.('reconnecting');
    expect(get(h.ctl.status)).toBe('reconnecting');
  });
});

describe('serviceColorIndex', () => {
  it('is the position in the sorted service list, wrapping after 8 slots', () => {
    expect(serviceColorIndex('web', ['api', 'db', 'web'])).toBe(2);
    const many = Array.from({ length: 10 }, (_, i) => `s${i}`);
    expect(serviceColorIndex('s9', many)).toBe(1);
  });
});
