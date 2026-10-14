import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Status } from '../api/types';
import { installFakeStream, type FakeStream } from '../../vitest-setup';
import { changes, live, refreshStatus, startLive, stopLive } from './live';

let fake: FakeStream;
const status: Status = { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false };
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
    await tick();
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
});
