import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Sample } from '../api/types';
import type { SeriesPoint } from '../metrics/series';
import type { SSEOptions } from '../sse/client';
import { createStackLiveMetrics, totalsNow } from './liveMetrics';

const T0 = Date.parse('2026-10-14T10:00:00Z');
const iso = (sec: number) => new Date(T0 + sec * 1000).toISOString().replace('.000Z', 'Z');
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

function harness() {
  const opens: { opts: SSEOptions; close: ReturnType<typeof vi.fn> }[] = [];
  const open = vi.fn((opts: SSEOptions) => {
    const close = vi.fn();
    opens.push({ opts, close });
    return { close, status: 'open' as const };
  });
  const live = createStackLiveMetrics('web', { open });
  const send = (s: Sample | Sample[], i = opens.length - 1) =>
    opens[i].opts.onMessage({ event: 'sample', data: JSON.stringify(s) });
  return { live, open, opens, send };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('createStackLiveMetrics', () => {
  it('acquire is ref-counted', () => {
    const h = harness();
    const a = h.live.acquire();
    const b = h.live.acquire();
    expect(h.open).toHaveBeenCalledTimes(1);
    expect(h.opens[0].opts.url).toBe('/api/stacks/web/metrics/live');
    a();
    expect(h.opens[0].close).not.toHaveBeenCalled();
    b();
    expect(h.opens[0].close).toHaveBeenCalledTimes(1);
  });

  it('release is idempotent per holder', () => {
    const h = harness();
    const a = h.live.acquire();
    const b = h.live.acquire();
    a();
    a();
    expect(h.opens[0].close).not.toHaveBeenCalled();
    b();
    expect(h.opens[0].close).toHaveBeenCalledTimes(1);
  });

  it('latest keeps the newest point per service', () => {
    const h = harness();
    h.live.acquire();
    h.send(sample('web', 'c1', 10, { cpu: 1 }));
    h.send(sample('web', 'c1', 15, { cpu: 2 }));
    h.send(sample('db', 'c2', 15, { cpu: 7 }));
    h.send(sample('web', 'c1', 20, { cpu: 3 }));
    const latest = get(h.live.latest);
    expect(latest.get('web')?.cpu).toBe(2);
    expect(latest.get('web')?.t).toBe(T0 + 15_000);
    vi.advanceTimersByTime(1500);
    expect(get(h.live.latest).get('web')?.cpu).toBe(3);
    expect(get(h.live.latest).get('db')?.cpu).toBe(7);
  });

  it('onPoints receives every aggregated point and can be unsubscribed', () => {
    const h = harness();
    h.live.acquire();
    const seen: [string, number][] = [];
    const off = h.live.onPoints((s, pts) => pts.forEach((p) => seen.push([s, p.cpu])));
    h.send(sample('web', 'c1', 10, { cpu: 1 }));
    h.send(sample('web', 'c1', 15, { cpu: 2 }));
    expect(seen).toEqual([['web', 1]]);
    off();
    h.send(sample('web', 'c1', 20, { cpu: 3 }));
    expect(seen).toHaveLength(1);
  });

  it('dropped reconnects once', async () => {
    const h = harness();
    h.live.acquire();
    h.opens[0].opts.onMessage({ event: 'dropped', data: '' });
    h.opens[0].opts.onMessage({ event: 'dropped', data: '' });
    expect(h.opens[0].close).toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1100);
    expect(h.opens).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(5000);
    expect(h.opens).toHaveLength(2);
  });

  it('does not reconnect after the last release', async () => {
    const h = harness();
    const release = h.live.acquire();
    h.opens[0].opts.onMessage({ event: 'dropped', data: '' });
    release();
    await vi.advanceTimersByTimeAsync(5000);
    expect(h.opens).toHaveLength(1);
  });

  it('close() is terminal and leaves outstanding releases inert', () => {
    const h = harness();
    const a = h.live.acquire();
    const b = h.live.acquire();
    h.live.close();
    a();
    b();
    const c = h.live.acquire();
    c();
    expect(h.open).toHaveBeenCalledTimes(1);
  });

  it('mirrors the stream status and close() ends everything', () => {
    const h = harness();
    h.live.acquire();
    h.opens[0].opts.onStatus?.('reconnecting');
    expect(get(h.live.status)).toBe('reconnecting');
    h.live.close();
    expect(h.opens[0].close).toHaveBeenCalled();
  });
});

describe('totalsNow', () => {
  const pt = (over: Partial<SeriesPoint>): SeriesPoint => ({ t: 0, cpu: 0, mem: 0, memLimit: 0, ...over });

  it('sums services and leaves rates undefined until present', () => {
    expect(totalsNow(new Map())).toEqual({ cpu: undefined, mem: undefined, netRx: undefined, netTx: undefined });
    const noRates = new Map([
      ['a', pt({ cpu: 1.5, mem: 100 })],
      ['b', pt({ cpu: 2, mem: 50 })],
    ]);
    expect(totalsNow(noRates)).toEqual({ cpu: 3.5, mem: 150, netRx: undefined, netTx: undefined });
    const someRates = new Map([
      ['a', pt({ cpu: 1, mem: 1, netRx: 10, netTx: 4 })],
      ['b', pt({ cpu: 1, mem: 1 })],
      ['c', pt({ cpu: 1, mem: 1, netRx: 5, netTx: 1 })],
    ]);
    expect(totalsNow(someRates)).toMatchObject({ cpu: 3, mem: 3, netRx: 15, netTx: 5 });
  });
});
