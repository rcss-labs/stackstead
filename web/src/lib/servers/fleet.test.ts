import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import type { FleetStacks } from '$lib/api/types';
import { createFleet } from './fleet';

const data: FleetStacks = { servers: [], stacks: [], events: [] };

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

describe('fleet', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('polls every 5 s only while visible', async () => {
    const load = vi.fn(async () => data);
    const { doc, set } = fakeDoc();
    const fleet = createFleet({ load, doc });
    fleet.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(load).toHaveBeenCalledTimes(1);
    expect(get(fleet.state)).toEqual({ data, loading: false, error: '' });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(load).toHaveBeenCalledTimes(2);

    set('hidden');
    await vi.advanceTimersByTimeAsync(60_000);
    expect(load).toHaveBeenCalledTimes(2);

    set('visible');
    await vi.advanceTimersByTimeAsync(0);
    expect(load).toHaveBeenCalledTimes(3);

    fleet.stop();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(load).toHaveBeenCalledTimes(3);
  });

  it('keeps the last data and records the error when a poll fails', async () => {
    const load = vi.fn<() => Promise<FleetStacks>>().mockResolvedValueOnce(data).mockRejectedValueOnce(new Error('boom'));
    const fleet = createFleet({ load, doc: fakeDoc().doc });
    fleet.start();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(get(fleet.state)).toEqual({ data, loading: false, error: 'boom' });
    fleet.stop();
  });

  it('drops a response that lands after stop', async () => {
    let resolve!: (v: FleetStacks) => void;
    const load = vi.fn(() => new Promise<FleetStacks>((r) => (resolve = r)));
    const fleet = createFleet({ load, doc: fakeDoc().doc });
    fleet.start();
    fleet.stop();
    resolve(data);
    await vi.advanceTimersByTimeAsync(0);
    expect(get(fleet.state).data).toBeUndefined();
  });
});
