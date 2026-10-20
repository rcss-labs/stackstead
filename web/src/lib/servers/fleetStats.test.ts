import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import type { HostSnapshot, MetricsSummary } from '$lib/api/types';
import { createFleetStats } from './fleetStats';

const summary = (tag: string) => ({ window: '15m', stacks: { [tag]: {} } }) as unknown as MetricsSummary;
const host = { hostname: 'h' } as HostSnapshot;

function fakeDoc(state: 'visible' | 'hidden' = 'visible') {
  const doc = new EventTarget() as EventTarget & { visibilityState: string };
  doc.visibilityState = state;
  return {
    doc: doc as unknown as Document,
    set(next: 'visible' | 'hidden') {
      doc.visibilityState = next;
      doc.dispatchEvent(new Event('visibilitychange'));
    },
  };
}

describe('fleet stats', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('keeps each server\'s summary and host apart, and one failing endpoint only blanks that server\'s part', async () => {
    const loadSummary = vi.fn(async (id: string) => {
      if (id === 'b1') throw new Error('boom');
      return summary(id);
    });
    const loadHost = vi.fn(async () => host);
    const { doc } = fakeDoc();
    const stats = createFleetStats({ loadSummary, loadHost, doc });
    stats.setServers(['a0', 'b1']);
    stats.start();
    await vi.advanceTimersByTimeAsync(0);
    const state = get(stats.state);
    expect(state.a0.summary).toEqual(summary('a0'));
    expect(state.b1.summary).toBeUndefined();
    expect(state.b1.host).toBe(host);
    stats.stop();
  });

  it('records a failed host as an error and drops its old snapshot', async () => {
    let fail = false;
    const loadHost = vi.fn(async () => {
      if (fail) throw new Error('timeout');
      return host;
    });
    const stats = createFleetStats({ loadSummary: async () => summary('x'), loadHost, doc: fakeDoc().doc });
    stats.setServers(['a0']);
    stats.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(get(stats.state).a0.host).toBe(host);
    fail = true;
    await vi.advanceTimersByTimeAsync(5_000);
    expect(get(stats.state).a0.host).toBeUndefined();
    expect(get(stats.state).a0.hostError).toBe('timeout');
    stats.stop();
  });

  it('polls every 5 s only while visible and not after stop', async () => {
    const loadSummary = vi.fn(async () => summary('x'));
    const { doc, set } = fakeDoc();
    const stats = createFleetStats({ loadSummary, loadHost: async () => host, doc });
    stats.setServers(['a0']);
    stats.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(loadSummary).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(loadSummary).toHaveBeenCalledTimes(2);
    set('hidden');
    await vi.advanceTimersByTimeAsync(60_000);
    expect(loadSummary).toHaveBeenCalledTimes(2);
    set('visible');
    await vi.advanceTimersByTimeAsync(0);
    expect(loadSummary).toHaveBeenCalledTimes(3);
    stats.stop();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(loadSummary).toHaveBeenCalledTimes(3);
  });

  it('loads a newly reachable server at once and forgets one that went away', async () => {
    const loadSummary = vi.fn(async (id: string) => summary(id));
    const stats = createFleetStats({ loadSummary, loadHost: async () => host, doc: fakeDoc().doc });
    stats.setServers(['a0']);
    stats.start();
    await vi.advanceTimersByTimeAsync(0);
    stats.setServers(['a0', 'b1']);
    await vi.advanceTimersByTimeAsync(0);
    expect(get(stats.state).b1.summary).toEqual(summary('b1'));
    stats.setServers(['b1']);
    expect(get(stats.state).a0).toBeUndefined();
    stats.stop();
  });
});
