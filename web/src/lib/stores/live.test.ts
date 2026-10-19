import { get } from '$lib/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Status } from '../api/types';
import { installFakeStream, type FakeStream } from '../../vitest-setup';
import { changes, events, live, refreshStatus, startLive, stopLive } from './live';

let fake: FakeStream;
const status: Status = {
  docker: 'ok',
  dockerVersion: '27.1.1',
  compose: '2.29.0',
  events: 'connected',
  metricsDegraded: false,
  metricsIntervalSeconds: 10,
  metricsRetentionSeconds: 259200,
};
let fetchStatus: ReturnType<typeof vi.fn<() => Promise<Status>>>;
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => {
  vi.useFakeTimers();
  fetchStatus = vi.fn(async () => status);
});
afterEach(() => {
  stopLive();
  fake?.restore();
  vi.useRealTimers();
});

describe('live store', () => {
  it('TestStartLiveIsIdempotent', async () => {
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    startLive({ fetchStatus });
    await tick();
    expect(fake.requests).toHaveLength(1);
    expect(get(live).connection).toBe('open');
  });

  it('can be started again after stopLive', async () => {
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    await tick();
    stopLive();
    expect(get(live).connection).toBe('closed');
    startLive({ fetchStatus });
    await tick();
    expect(fake.requests).toHaveLength(2);
  });

  it('maps status events to flags and change events to the changes store', async () => {
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    await tick();
    expect(get(live).events).toBe('unknown');
    fake.push('/api/events', 'event: status\ndata: {"events":"connected","metricsDegraded":true}\n\n');
    await tick();
    expect(get(live).events).toBe('connected');
    expect(get(live).metricsDegraded).toBe(true);
    fake.push('/api/events', 'event: change\ndata: {"project":"web","seq":4}\n\n');
    await tick();
    expect(get(changes)).toEqual({ project: 'web', seq: 4 });
  });

  it('refreshes docker and compose from fetchStatus on open', async () => {
    fake = installFakeStream({ '/api/events': [] });
    fetchStatus.mockResolvedValue({ ...status, docker: 'unavailable', compose: 'unavailable' });
    startLive({ fetchStatus });
    expect(get(live).docker).toBe('unknown');
    await tick();
    expect(fetchStatus).toHaveBeenCalledTimes(1);
    expect(get(live).docker).toBe('unavailable');
    expect(get(live).compose).toBe('unavailable');
  });

  it('survives a failing fetchStatus', async () => {
    fake = installFakeStream({ '/api/events': [] });
    fetchStatus.mockRejectedValue(new Error('boom'));
    startLive({ fetchStatus });
    await tick();
    expect(get(live).docker).toBe('unknown');
    expect(get(live).connection).toBe('open');
  });

  it('TestReconnectEmitsResync', async () => {
    fake = installFakeStream({ '/api/events': { responses: [{ chunks: [] }, { chunks: [] }] } });
    const seen: unknown[] = [];
    const unsub = changes.subscribe((c) => seen.push(c));
    startLive({ fetchStatus });
    await tick();
    expect(seen.filter((c) => (c as { resync?: boolean } | null)?.resync)).toHaveLength(1);
    fake.error('/api/events', new TypeError('network'));
    await tick(2000);
    expect(fake.requests).toHaveLength(2);
    expect(seen.filter((c) => (c as { resync?: boolean } | null)?.resync)).toHaveLength(2);
    expect(fetchStatus).toHaveBeenCalledTimes(2);
    unsub();
  });

  it('TestDroppedReopensImmediately', async () => {
    fake = installFakeStream({ '/api/events': { responses: [{ chunks: [] }, { chunks: [] }] } });
    startLive({ fetchStatus });
    await tick(10_000);
    fake.push('/api/events', 'event: dropped\ndata: {}\n\n');
    await tick(0); // no backoff wait
    expect(fake.requests).toHaveLength(2);
    expect(fake.requests[0].signal?.aborted).toBe(true);
    expect(get(live).connection).toBe('open');
  });

  it('TestStopsOn401 and may be restarted after login', async () => {
    fake = installFakeStream({ '/api/events': { responses: [{ status: 401, body: { error: 'x' } }, { chunks: [] }] } });
    startLive({ fetchStatus });
    await tick(60_000);
    expect(fake.requests).toHaveLength(1);
    expect(get(live).connection).toBe('unauthorized');
    startLive({ fetchStatus });
    await tick();
    expect(fake.requests).toHaveLength(2);
    expect(get(live).connection).toBe('open');
  });

  it('calls fetchStatus on open and on events flips only', async () => {
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    await tick();
    const send = (events: string) =>
      fake.push('/api/events', `event: status\ndata: {"events":"${events}","metricsDegraded":false}\n\n`);
    send('connected'); // unknown -> connected is not a flip
    await tick();
    fake.push('/api/events', 'event: change\ndata: {"project":"a","seq":1}\n\n');
    await tick();
    expect(fetchStatus).toHaveBeenCalledTimes(1);
    send('disconnected');
    await tick();
    expect(fetchStatus).toHaveBeenCalledTimes(2);
    send('disconnected');
    await tick();
    expect(fetchStatus).toHaveBeenCalledTimes(2);
    send('connected');
    await tick();
    expect(fetchStatus).toHaveBeenCalledTimes(3);
  });

  it('refreshStatus refetches, and is a no-op when not started', async () => {
    await refreshStatus();
    expect(fetchStatus).not.toHaveBeenCalled();
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    await tick();
    await refreshStatus();
    expect(fetchStatus).toHaveBeenCalledTimes(2);
  });

  it('refreshStatus fills dockerVersion and metrics settings', async () => {
    fake = installFakeStream({ '/api/events': [] });
    expect(get(live).dockerVersion).toBe('');
    startLive({ fetchStatus });
    await tick();
    expect(get(live).dockerVersion).toBe('27.1.1');
    expect(get(live).metricsIntervalSeconds).toBe(10);
    expect(get(live).metricsRetentionSeconds).toBe(259200);
  });

  it('stopLive clears dockerVersion', async () => {
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    await tick();
    stopLive();
    expect(get(live).dockerVersion).toBe('');
    expect(get(live).metricsIntervalSeconds).toBeUndefined();
  });

  it('a dropped right after open reconnects via backoff, not immediately', async () => {
    fake = installFakeStream({ '/api/events': { responses: [{ chunks: [] }, { chunks: [] }] } });
    startLive({ fetchStatus });
    await tick();
    fake.push('/api/events', 'event: dropped\ndata: {}\n\n');
    fake.close('/api/events'); // the server ends the stream after dropped
    await tick(0);
    expect(fake.requests).toHaveLength(1);
    await tick(2000);
    expect(fake.requests).toHaveLength(2);
  });

  it('a dropped long after open reopens immediately', async () => {
    fake = installFakeStream({ '/api/events': { responses: [{ chunks: [] }, { chunks: [] }] } });
    startLive({ fetchStatus });
    await tick(10_000);
    fake.push('/api/events', 'event: dropped\ndata: {}\n\n');
    await tick(0);
    expect(fake.requests).toHaveLength(2);
  });

  it('ignores a fetchStatus reply that lands after stopLive', async () => {
    fake = installFakeStream({ '/api/events': [] });
    let resolve!: (s: Status) => void;
    fetchStatus.mockImplementation(() => new Promise<Status>((r) => (resolve = r)));
    startLive({ fetchStatus });
    await tick();
    stopLive();
    resolve({ ...status, docker: 'unavailable' });
    await tick();
    expect(get(live).docker).toBe('unknown');
    await refreshStatus();
    expect(fetchStatus).toHaveBeenCalledTimes(1); // deps cleared
  });

  it('ignores an older overlapping fetchStatus reply', async () => {
    fake = installFakeStream({ '/api/events': [] });
    const resolvers: Array<(s: Status) => void> = [];
    fetchStatus.mockImplementation(() => new Promise<Status>((r) => resolvers.push(r)));
    startLive({ fetchStatus });
    await tick();
    void refreshStatus();
    resolvers[1]({ ...status, docker: 'ok' });
    await tick();
    resolvers[0]({ ...status, docker: 'unavailable' });
    await tick();
    expect(get(live).docker).toBe('ok');
  });

  it('401 clears the status flags but keeps connection unauthorized', async () => {
    fake = installFakeStream({
      '/api/events': { responses: [{ chunks: ['event: status\ndata: {"events":"connected","metricsDegraded":true}\n\n'] }, { status: 401, body: {} }] },
    });
    startLive({ fetchStatus });
    await tick();
    expect(get(live).metricsDegraded).toBe(true);
    fake.close('/api/events');
    await tick(5000);
    const s = get(live);
    expect(s.connection).toBe('unauthorized');
    expect([s.events, s.docker, s.compose, s.metricsDegraded]).toEqual(['unknown', 'unknown', 'unknown', false]);
  });

  const ev = (n: number) =>
    `event: event\ndata: ${JSON.stringify({ action: 'start', container: `c${n}`, project: 'p', service: 's', image: 'i', time: '2026-10-15T12:05:41.123456789Z' })}\n\n`;

  it('event messages prepend and cap at 50', async () => {
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    await tick();
    for (let i = 0; i < 55; i++) fake.push('/api/events', ev(i));
    await tick();
    const list = get(events);
    expect(list).toHaveLength(50);
    expect(list[0].container).toBe('c54');
    expect(list[49].container).toBe('c5');
  });

  it('malformed event ignored', async () => {
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    await tick();
    fake.push('/api/events', 'event: event\ndata: {nope\n\n');
    await tick();
    expect(get(events)).toEqual([]);
  });

  it('stopLive clears events', async () => {
    fake = installFakeStream({ '/api/events': [] });
    startLive({ fetchStatus });
    await tick();
    fake.push('/api/events', ev(1));
    await tick();
    expect(get(events)).toHaveLength(1);
    stopLive();
    expect(get(events)).toEqual([]);
  });
});
